package api

import (
	"net/http"
)

// handleWS upgrades to a WebSocket for real-time job progress/log events.
// Since browsers cannot set an Authorization header on the WS handshake,
// the JWT is passed as a query parameter instead and validated here.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	if _, err := s.authSvc.Parse(token); err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	s.hub.ServeWS(w, r)
}
