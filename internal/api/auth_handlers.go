package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"gorm.io/gorm"

	"instagrampanel/internal/auth"
	"instagrampanel/internal/models"
)

type setupInitRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetupStatus reports whether an initial admin account already
// exists, so the frontend knows whether to show the setup wizard or the
// login screen.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	var count int64
	s.db.Model(&models.AdminUser{}).Count(&count)
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": count > 0})
}

func (s *Server) handleSetupInit(w http.ResponseWriter, r *http.Request) {
	var count int64
	s.db.Model(&models.AdminUser{}).Count(&count)
	if count > 0 {
		writeErr(w, http.StatusConflict, errors.New("panel already initialized"))
		return
	}

	var req setupInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Username) < 3 || len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, errors.New("username must be >=3 chars and password >=8 chars"))
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	user := models.AdminUser{Username: req.Username, PasswordHash: hash}
	if err := s.db.Create(&user).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	token, err := s.authSvc.IssueToken(user.ID, user.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var user models.AdminUser
	err := s.db.Where("username = ?", req.Username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !auth.CheckPassword(user.PasswordHash, req.Password)) {
		writeErr(w, http.StatusUnauthorized, errors.New("invalid username or password"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	token, err := s.authSvc.IssueToken(user.ID, user.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}
