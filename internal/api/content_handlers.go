package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"instagrampanel/internal/instagram"
	"instagrampanel/internal/models"
	"instagrampanel/internal/queue"
)

func (s *Server) handleListMedia(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	media, err := s.ig.ListMedia(r.Context(), id, limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, media)
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	comments, err := s.ig.ListComments(r.Context(), id, chi.URLParam(r, "mediaID"), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

type replyRequest struct {
	Message string `json:"message"`
}

func (s *Server) handleReplyComment(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req replyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeErr(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	replyID, err := s.ig.ReplyComment(r.Context(), id, chi.URLParam(r, "mediaID"), chi.URLParam(r, "commentID"), req.Message)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": replyID})
}

// jobControlRequest is the shared worker/delay part of job requests.
type jobControlRequest struct {
	WorkerCount int `json:"worker_count"`
	DelayMs     int `json:"delay_ms"`
}

type moderateCommentsRequest struct {
	jobControlRequest
	MediaID    string   `json:"media_id"`
	CommentIDs []string `json:"comment_ids"`
	Action     string   `json:"action"`
}

func (s *Server) handleModerateCommentsJob(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req moderateCommentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Action == "" {
		req.Action = "delete"
	}
	if req.Action != "delete" {
		writeErr(w, http.StatusBadRequest, errors.New("action must be delete"))
		return
	}
	if req.MediaID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("media_id is required"))
		return
	}
	if len(req.CommentIDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("comment_ids cannot be empty"))
		return
	}
	job, err := s.q.Enqueue(r.Context(), id, req.MediaID, models.JobModerateComments, req.WorkerCount, req.DelayMs,
		queue.ModerateCommentsParams{CommentIDs: req.CommentIDs, Action: req.Action})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handlePublishJob(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req instagram.PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.MediaType = strings.ToUpper(strings.TrimSpace(req.MediaType))
	req.URL = strings.TrimSpace(req.URL)
	if err := req.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.q.Enqueue(r.Context(), id, "", models.JobPublishMedia, 1, 0, req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

type exportCommentsRequest struct {
	MediaID     string `json:"media_id"`
	MaxComments int    `json:"max_comments"`
}

func (s *Server) handleExportCommentsJob(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req exportCommentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.MediaID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("media_id is required"))
		return
	}
	job, err := s.q.Enqueue(r.Context(), id, req.MediaID, models.JobExportComments, 1, 0,
		queue.ExportCommentsParams{MaxComments: req.MaxComments})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}
