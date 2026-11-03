// Package api exposes the REST + WebSocket surface of the panel backend.
package api

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"gorm.io/gorm"

	"instagrampanel/internal/auth"
	"instagrampanel/internal/crypto"
	"instagrampanel/internal/instagram"
	"instagrampanel/internal/queue"
	"instagrampanel/internal/realtime"
)

type Server struct {
	db         *gorm.DB
	box        *crypto.Box
	authSvc    *auth.Service
	ig         *instagram.Manager
	q          *queue.Manager
	hub        *realtime.Hub
	corsOrigin string
}

func NewServer(db *gorm.DB, box *crypto.Box, authSvc *auth.Service, ig *instagram.Manager, q *queue.Manager, hub *realtime.Hub, corsOrigin string) *Server {
	return &Server{db: db, box: box, authSvc: authSvc, ig: ig, q: q, hub: hub, corsOrigin: corsOrigin}
}

// Router builds the API router. webDir, if non-empty, is served for any
// path that doesn't match an /api/* route (with an index.html fallback for
// client-side routing), so the server binary also serves the web panel.
func (s *Server) Router(webDir string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	if webDir != "" {
		r.NotFound(spaHandler(webDir))
	}

	r.Route("/api", func(r chi.Router) {
		r.Post("/setup/init", s.handleSetupInit)
		r.Get("/setup/status", s.handleSetupStatus)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/ws", s.handleWS) // token passed as ?token=, validated inside

		r.Group(func(r chi.Router) {
			r.Use(s.authSvc.Middleware)

			r.Route("/accounts", func(r chi.Router) {
				r.Get("/", s.handleListAccounts)
				r.Post("/login", s.handleAccountLogin)
				r.Post("/login/code", s.handleAccountLoginCode)
				r.Delete("/{accountID}", s.handleDeleteAccount)
				r.Post("/{accountID}/relogin", s.handleRelogin)

				r.Post("/{accountID}/sync", s.handleSyncAccount)
				r.Put("/{accountID}/proxy", s.handleSetProxy)
				r.Post("/{accountID}/proxy/test", s.handleTestProxy)

				r.Get("/{accountID}/media", s.handleListMedia)
				r.Get("/{accountID}/media/{mediaID}/comments", s.handleListComments)
				r.Post("/{accountID}/media/{mediaID}/comments/{commentID}/reply", s.handleReplyComment)

				r.Post("/{accountID}/jobs/moderate-comments", s.handleModerateCommentsJob)
				r.Post("/{accountID}/jobs/publish", s.handlePublishJob)
				r.Post("/{accountID}/jobs/export-comments", s.handleExportCommentsJob)
				r.Get("/{accountID}/jobs", s.handleListJobs)
			})

			r.Post("/uploads", s.handleUpload)
			r.Post("/bulk-publish", s.handleBulkPublishJob)
			r.Get("/bulk-publish/jobs", s.handleListBulkPublishJobs)

			r.Route("/jobs/{jobID}", func(r chi.Router) {
				r.Get("/", s.handleGetJob)
				r.Get("/logs", s.handleJobLogs)
				r.Get("/download", s.handleJobDownload)
				r.Post("/pause", s.handleJobPause)
				r.Post("/resume", s.handleJobResume)
				r.Post("/cancel", s.handleJobCancel)
			})
		})
	})

	return r
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// spaHandler serves static files from dir, falling back to index.html for
// any path without a matching file (client-side routing support).
func spaHandler(dir string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(dir))
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(dir + r.URL.Path); os.IsNotExist(err) {
			http.ServeFile(w, r, dir+"/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	}
}
