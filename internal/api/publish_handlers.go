package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"instagrampanel/internal/models"
	"instagrampanel/internal/queue"
)

const maxUploadBytes = 200 << 20

var uploadMediaTypes = map[string]string{
	".jpg": "IMAGE", ".jpeg": "IMAGE", ".png": "IMAGE", ".webp": "IMAGE",
	".mp4": "REELS", ".mov": "REELS",
}

// handleUpload stores a photo or video chosen in the panel so it can be
// published to one or more accounts.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("dosya okunamadı veya 200 MB'tan büyük"))
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("dosya seçilmedi"))
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	mediaType, ok := uploadMediaTypes[ext]
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("desteklenen dosyalar: JPG, PNG, WebP görsel veya MP4, MOV video"))
		return
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	id := hex.EncodeToString(b) + ext
	if err := os.MkdirAll(queue.UploadDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dst, err := os.Create(filepath.Join(queue.UploadDir, id))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	size, err := io.Copy(dst, file)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(filepath.Join(queue.UploadDir, id))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"upload_id": id, "media_type": mediaType, "file_name": header.Filename, "size": size,
	})
}

type bulkPublishRequest struct {
	UploadID   string `json:"upload_id"`
	FileName   string `json:"file_name"`
	Caption    string `json:"caption"`
	AccountIDs []uint `json:"account_ids"`
	DelaySec   int    `json:"delay_sec"` // wait between two accounts
}

// handleBulkPublishJob starts publishing one uploaded post to the selected
// accounts, one after another.
func (s *Server) handleBulkPublishJob(w http.ResponseWriter, r *http.Request) {
	var req bulkPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := queue.UploadPath(req.UploadID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len([]rune(req.Caption)) > 2200 {
		writeErr(w, http.StatusBadRequest, errors.New("açıklama en fazla 2200 karakter olabilir"))
		return
	}
	if req.DelaySec < 0 || req.DelaySec > 3600 {
		writeErr(w, http.StatusBadRequest, errors.New("hesaplar arası bekleme 0-3600 saniye olmalı"))
		return
	}

	// Keep the panel's order, drop duplicates and accounts that no longer exist.
	var existing []uint
	s.db.Model(&models.InstagramAccount{}).Where("id IN ?", req.AccountIDs).Pluck("id", &existing)
	known := map[uint]bool{}
	for _, id := range existing {
		known[id] = true
	}
	var ids []uint
	for _, id := range req.AccountIDs {
		if known[id] {
			ids = append(ids, id)
			delete(known, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("paylaşılacak en az bir hesap seçin"))
		return
	}

	job, err := s.q.Enqueue(r.Context(), 0, "", models.JobBulkPublish, 1, req.DelaySec*1000, queue.BulkPublishParams{
		UploadID:   req.UploadID,
		FileName:   req.FileName,
		MediaType:  uploadMediaTypes[strings.ToLower(filepath.Ext(req.UploadID))],
		Caption:    req.Caption,
		AccountIDs: ids,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleListBulkPublishJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.q.ListJobsByType(r.Context(), models.JobBulkPublish)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list jobs: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}
