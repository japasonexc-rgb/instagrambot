package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"

	"instagrampanel/internal/queue"
)

func jobID(r *http.Request) (uint, error) {
	v, err := strconv.ParseUint(chi.URLParam(r, "jobID"), 10, 64)
	if err != nil {
		return 0, errors.New("invalid job id")
	}
	return uint(v), nil
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	jobs, err := s.q.ListJobs(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs, err := s.q.ListJobLogs(r.Context(), id, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// handleJobDownload serves a job's CSV result file.
func (s *Server) handleJobDownload(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if job.ResultPath == "" || filepath.Dir(job.ResultPath) != filepath.Clean(queue.ExportDir) {
		writeErr(w, http.StatusNotFound, errors.New("job has no downloadable result"))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(job.ResultPath)+`"`)
	http.ServeFile(w, r, job.ResultPath)
}

func (s *Server) handleJobPause(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.q.Pause(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleJobResume(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.q.Resume(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id, err := jobID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.q.Cancel(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}
