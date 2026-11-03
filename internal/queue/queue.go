// Package queue implements the background job engine: a configurable
// worker pool per job (concurrency + delay between requests), pause/resume/
// cancel control, persisted progress, and real-time broadcasting of
// progress and log events.
package queue

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"instagrampanel/internal/instagram"
	"instagrampanel/internal/models"
	"instagrampanel/internal/realtime"
)

// MaxWorkers keeps per-account concurrency within Instagram's rate limits.
const MaxWorkers = 5

// ExportDir is where CSV job results are written.
const ExportDir = "data/exports"

// UploadDir keeps media files chosen in the panel for publishing. They are
// kept for a week so a bulk post can be retried on the accounts that failed.
const UploadDir = "data/uploads"

const uploadRetention = 7 * 24 * time.Hour

var uploadIDPattern = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|jpeg|png|webp|mp4|mov)$`)

// UploadPath resolves an upload id to the absolute path of its file.
func UploadPath(uploadID string) (string, error) {
	if !uploadIDPattern.MatchString(uploadID) {
		return "", errors.New("geçersiz dosya")
	}
	p, err := filepath.Abs(filepath.Join(UploadDir, uploadID))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("paylaşılacak dosya bulunamadı; gönderiyi tekrar seçin")
	}
	return p, nil
}

type Manager struct {
	db  *gorm.DB
	ig  *instagram.Manager
	hub *realtime.Hub

	mu      sync.Mutex
	handles map[uint]*jobHandle
}

type jobHandle struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	paused bool
	resume chan struct{}
}

func NewManager(db *gorm.DB, ig *instagram.Manager, hub *realtime.Hub) *Manager {
	m := &Manager{db: db, ig: ig, hub: hub, handles: map[uint]*jobHandle{}}
	m.failInterrupted()
	m.pruneUploads()
	return m
}

func (m *Manager) pruneUploads() {
	entries, err := os.ReadDir(UploadDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > uploadRetention {
			_ = os.Remove(filepath.Join(UploadDir, e.Name()))
		}
	}
}

// failInterrupted marks jobs left unfinished by a previous process as
// failed, since their in-memory workers no longer exist.
func (m *Manager) failInterrupted() {
	now := time.Now()
	m.db.Model(&models.Job{}).
		Where("status IN ?", []models.JobStatus{models.JobPending, models.JobRunning, models.JobPaused}).
		Updates(map[string]interface{}{"status": models.JobFailed, "finished_at": &now})
}

// ModerateCommentsParams is the params_json payload for JobModerateComments.
type ModerateCommentsParams struct {
	CommentIDs []string `json:"comment_ids"`
	Action     string   `json:"action"` // "delete"
}

// BulkPublishParams is the params_json payload for JobBulkPublish. The wait
// between accounts is the job's DelayMs.
type BulkPublishParams struct {
	UploadID   string `json:"upload_id"`
	FileName   string `json:"file_name"`
	MediaType  string `json:"media_type"` // IMAGE | REELS
	Caption    string `json:"caption"`
	AccountIDs []uint `json:"account_ids"` // in publishing order
}

// ExportCommentsParams is the params_json payload for JobExportComments.
type ExportCommentsParams struct {
	MaxComments int `json:"max_comments"`
}

// Enqueue creates a job row and immediately starts running it.
func (m *Manager) Enqueue(ctx context.Context, accountID uint, mediaID string, jobType models.JobType, workerCount, delayMs int, params interface{}) (*models.Job, error) {
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > MaxWorkers {
		workerCount = MaxWorkers
	}
	if delayMs < 0 {
		delayMs = 0
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal job params: %w", err)
	}

	job := &models.Job{
		AccountID:   accountID,
		MediaID:     mediaID,
		Type:        jobType,
		Status:      models.JobPending,
		ParamsJSON:  string(paramsBytes),
		WorkerCount: workerCount,
		DelayMs:     delayMs,
	}
	if err := m.db.WithContext(ctx).Create(job).Error; err != nil {
		return nil, err
	}

	go m.run(job.ID)
	return job, nil
}

func (m *Manager) Pause(jobID uint) error {
	m.mu.Lock()
	h, ok := m.handles[jobID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %d is not running", jobID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.paused {
		return nil
	}
	h.paused = true
	h.resume = make(chan struct{})
	m.updateStatus(jobID, models.JobPaused)
	return nil
}

func (m *Manager) Resume(jobID uint) error {
	m.mu.Lock()
	h, ok := m.handles[jobID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %d is not running", jobID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.paused {
		return nil
	}
	h.paused = false
	close(h.resume)
	m.updateStatus(jobID, models.JobRunning)
	return nil
}

func (m *Manager) Cancel(jobID uint) error {
	m.mu.Lock()
	h, ok := m.handles[jobID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %d is not running", jobID)
	}
	h.cancel()
	return nil
}

// CancelAccountJobs stops every running job of an account (used when the
// account is removed).
func (m *Manager) CancelAccountJobs(accountID uint) {
	var ids []uint
	m.db.Model(&models.Job{}).Where("account_id = ?", accountID).Pluck("id", &ids)
	for _, id := range ids {
		_ = m.Cancel(id)
	}
}

// waitIfPaused blocks the calling worker while the job is paused, and
// returns ctx.Err() if the job is cancelled while paused.
func (h *jobHandle) waitIfPaused(ctx context.Context) error {
	h.mu.Lock()
	paused := h.paused
	resume := h.resume
	h.mu.Unlock()
	if !paused {
		return nil
	}
	select {
	case <-resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) updateStatus(jobID uint, status models.JobStatus) {
	m.db.Model(&models.Job{}).Where("id = ?", jobID).Update("status", status)
	m.broadcastJob(jobID)
}

func (m *Manager) broadcastJob(jobID uint) {
	var job models.Job
	if err := m.db.First(&job, jobID).Error; err == nil {
		m.hub.JobUpdate(jobID, job)
	}
}

func (m *Manager) log(jobID uint, level models.JobLogLevel, targetID string, format string, args ...interface{}) {
	entry := models.JobLog{
		JobID:    jobID,
		Level:    level,
		Message:  fmt.Sprintf(format, args...),
		TargetID: targetID,
	}
	m.db.Create(&entry)
	m.hub.JobLog(jobID, entry)
}

func (m *Manager) run(jobID uint) {
	var job models.Job
	if err := m.db.First(&job, jobID).Error; err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	handle := &jobHandle{cancel: cancel, resume: make(chan struct{})}
	m.mu.Lock()
	m.handles[jobID] = handle
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.handles, jobID)
		m.mu.Unlock()
		cancel()
	}()

	now := time.Now()
	m.db.Model(&models.Job{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"status": models.JobRunning, "started_at": &now,
	})
	m.broadcastJob(jobID)
	m.log(jobID, models.LogInfo, "", "iş başlatıldı (worker=%d, delay=%dms)", job.WorkerCount, job.DelayMs)

	var runErr error
	switch job.Type {
	case models.JobModerateComments:
		runErr = m.runModerateComments(ctx, handle, job)
	case models.JobPublishMedia:
		runErr = m.runPublishMedia(ctx, job)
	case models.JobExportComments:
		runErr = m.runExportComments(ctx, job)
	case models.JobBulkPublish:
		runErr = m.runBulkPublish(ctx, handle, job)
	default:
		runErr = fmt.Errorf("unknown job type %q", job.Type)
	}

	finishedAt := time.Now()
	status := models.JobCompleted
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			status = models.JobCancelled
			m.log(jobID, models.LogWarn, "", "iş iptal edildi")
		} else {
			status = models.JobFailed
			m.log(jobID, models.LogError, "", "iş hata ile sonlandı: %v", runErr)
		}
	} else {
		m.log(jobID, models.LogInfo, "", "iş tamamlandı")
	}
	m.db.Model(&models.Job{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"status": status, "finished_at": &finishedAt,
	})
	m.broadcastJob(jobID)
}

func (m *Manager) incrProgress(jobID uint, succeeded bool) {
	col := "failed"
	if succeeded {
		col = "succeeded"
	}
	m.db.Model(&models.Job{}).Where("id = ?", jobID).
		Updates(map[string]interface{}{
			"processed": gorm.Expr("processed + 1"),
			col:         gorm.Expr(col + " + 1"),
		})
	m.broadcastJob(jobID)
}

func (m *Manager) setTotal(jobID uint, total int) {
	m.db.Model(&models.Job{}).Where("id = ?", jobID).Update("total", total)
	m.broadcastJob(jobID)
}

// runModerateComments deletes a list of comments using a configurable
// worker pool and per-request delay. A login problem (wrong password,
// security check) stops the whole job, since every further call would fail
// the same way.
func (m *Manager) runModerateComments(ctx context.Context, h *jobHandle, job models.Job) error {
	var params ModerateCommentsParams
	if err := json.Unmarshal([]byte(job.ParamsJSON), &params); err != nil {
		return err
	}
	m.setTotal(job.ID, len(params.CommentIDs))

	work := make(chan string)
	var wg sync.WaitGroup
	var fatalErr error
	var errMu sync.Mutex

	for i := 0; i < job.WorkerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for commentID := range work {
				if err := h.waitIfPaused(ctx); err != nil {
					return
				}
				if ctx.Err() != nil {
					return
				}

				var opErr error
				switch params.Action {
				case "delete":
					opErr = m.ig.DeleteComment(ctx, job.AccountID, job.MediaID, commentID)
				default:
					opErr = fmt.Errorf("unknown action %q", params.Action)
				}

				if opErr != nil {
					m.log(job.ID, models.LogError, commentID, "%s başarısız: %v", actionLabel(params.Action), opErr)
					m.incrProgress(job.ID, false)
					if instagram.IsAuthError(opErr) {
						errMu.Lock()
						if fatalErr == nil {
							fatalErr = fmt.Errorf("hesaba yeniden giriş gerekiyor (hesap sayfasında \"Yeniden Giriş Yap\"): %w", opErr)
						}
						errMu.Unlock()
						h.cancel()
						return
					}
				} else {
					m.log(job.ID, models.LogInfo, commentID, "%s başarılı", actionLabel(params.Action))
					m.incrProgress(job.ID, true)
				}

				if job.DelayMs > 0 {
					select {
					case <-time.After(time.Duration(job.DelayMs) * time.Millisecond):
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

feed:
	for _, id := range params.CommentIDs {
		select {
		case work <- id:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()

	if fatalErr != nil {
		return fatalErr
	}
	return ctx.Err()
}

func actionLabel(action string) string {
	switch action {
	case "delete":
		return "silme"
	}
	return action
}

// runPublishMedia publishes a single image or reel.
func (m *Manager) runPublishMedia(ctx context.Context, job models.Job) error {
	var params instagram.PublishRequest
	if err := json.Unmarshal([]byte(job.ParamsJSON), &params); err != nil {
		return err
	}
	m.setTotal(job.ID, 1)

	pub, err := m.ig.Publish(ctx, job.AccountID, params, func(msg string) {
		m.log(job.ID, models.LogInfo, "", "%s", msg)
	})
	if err != nil {
		m.incrProgress(job.ID, false)
		return err
	}
	m.incrProgress(job.ID, true)
	m.log(job.ID, models.LogInfo, pub.ID, "paylaşım yayınlandı %s", pub.URL)
	return m.db.Model(&models.Job{}).Where("id = ?", job.ID).Update("media_id", pub.ID).Error
}

// runBulkPublish publishes one post to several accounts strictly one after
// another, waiting job.DelayMs between accounts. An account that fails is
// logged and skipped; the remaining accounts still get the post.
//
// Logs about an account carry its ID as TargetID and read
// "<name>: paylaşılıyor" / "<name>: paylaşıldı <link>" / (error) "<name>:
// başarısız — ...", which the panel turns into a per-account status list.
func (m *Manager) runBulkPublish(ctx context.Context, h *jobHandle, job models.Job) error {
	var params BulkPublishParams
	if err := json.Unmarshal([]byte(job.ParamsJSON), &params); err != nil {
		return err
	}
	path, err := UploadPath(params.UploadID)
	if err != nil {
		return err
	}
	total := len(params.AccountIDs)
	m.setTotal(job.ID, total)

	for i, accID := range params.AccountIDs {
		if err := h.waitIfPaused(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		target := strconv.FormatUint(uint64(accID), 10)
		name := m.accountName(accID)
		m.log(job.ID, models.LogInfo, target, "%s: paylaşılıyor (%d/%d)", name, i+1, total)
		pub, err := m.ig.Publish(ctx, accID, instagram.PublishRequest{
			MediaType: params.MediaType, Path: path, Caption: params.Caption,
		}, func(string) {})
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			m.log(job.ID, models.LogError, target, "%s: başarısız — %v", name, err)
			m.incrProgress(job.ID, false)
		default:
			m.log(job.ID, models.LogInfo, target, "%s: paylaşıldı %s", name, pub.URL)
			m.incrProgress(job.ID, true)
		}

		if i < total-1 && job.DelayMs > 0 {
			m.log(job.ID, models.LogInfo, "", "sonraki hesaba geçmeden %d sn bekleniyor", job.DelayMs/1000)
			select {
			case <-time.After(time.Duration(job.DelayMs) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (m *Manager) accountName(accountID uint) string {
	var acc models.InstagramAccount
	if err := m.db.Select("label", "username").First(&acc, accountID).Error; err != nil {
		return fmt.Sprintf("hesap #%d (silinmiş)", accountID)
	}
	if acc.Username != "" {
		return "@" + acc.Username
	}
	return acc.Label
}

// runExportComments writes a post's comments to a CSV file under ExportDir.
func (m *Manager) runExportComments(ctx context.Context, job models.Job) error {
	var params ExportCommentsParams
	_ = json.Unmarshal([]byte(job.ParamsJSON), &params)

	comments, err := m.ig.ListComments(ctx, job.AccountID, job.MediaID, params.MaxComments)
	if err != nil {
		return err
	}
	m.setTotal(job.ID, len(comments))

	if err := os.MkdirAll(ExportDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(ExportDir, fmt.Sprintf("job_%d_comments.csv", job.ID))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"comment_id", "username", "text", "timestamp", "like_count"})
	for _, c := range comments {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = w.Write([]string{
			c.ID, csvSafe(c.Username), csvSafe(c.Text), c.Timestamp, strconv.Itoa(c.LikeCount),
		})
		m.incrProgress(job.ID, true)
	}

	return m.db.Model(&models.Job{}).Where("id = ?", job.ID).Update("result_path", path).Error
}

// csvSafe neutralizes user-written text that spreadsheet apps would
// otherwise evaluate as a formula.
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}

// ListJobsByType returns the latest jobs of one type across all accounts.
func (m *Manager) ListJobsByType(ctx context.Context, jobType models.JobType) ([]models.Job, error) {
	var out []models.Job
	err := m.db.WithContext(ctx).Where("type = ?", jobType).Order("created_at desc").Limit(100).Find(&out).Error
	return out, err
}

// ListJobs returns jobs for an account, most recent first.
func (m *Manager) ListJobs(ctx context.Context, accountID uint) ([]models.Job, error) {
	var out []models.Job
	err := m.db.WithContext(ctx).Where("account_id = ?", accountID).Order("created_at desc").Find(&out).Error
	return out, err
}

func (m *Manager) GetJob(ctx context.Context, jobID uint) (models.Job, error) {
	var job models.Job
	err := m.db.WithContext(ctx).First(&job, jobID).Error
	return job, err
}

func (m *Manager) ListJobLogs(ctx context.Context, jobID uint, limit int) ([]models.JobLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var out []models.JobLog
	err := m.db.WithContext(ctx).Where("job_id = ?", jobID).Order("created_at desc").Limit(limit).Find(&out).Error
	return out, err
}
