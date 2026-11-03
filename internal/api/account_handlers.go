package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"instagrampanel/internal/instagram"
	"instagrampanel/internal/models"
)

func accountID(r *http.Request) (uint, error) {
	v, err := strconv.ParseUint(chi.URLParam(r, "accountID"), 10, 64)
	if err != nil {
		return 0, errors.New("invalid account id")
	}
	return uint(v), nil
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	var accounts []models.InstagramAccount
	if err := s.db.Order("id").Find(&accounts).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

// Logging in can take a while: Instagram is called through the account's
// proxy with human-like pauses between requests.
const loginTimeout = 3 * time.Minute

type accountLoginRequest struct {
	Label    string `json:"label"`
	Username string `json:"username"`
	Password string `json:"password"`
	ProxyURL string `json:"proxy_url"`
}

// handleAccountLogin adds an account: it logs in with username + password
// through the optional proxy and saves the account to the panel. If
// Instagram asks for a code, the response says so and the code goes to
// handleAccountLoginCode.
func (s *Server) handleAccountLogin(w http.ResponseWriter, r *http.Request) {
	var req accountLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), loginTimeout)
	defer cancel()
	out, err := s.ig.StartLogin(ctx, instagram.LoginInput{
		Label: req.Label, Username: req.Username, Password: req.Password, ProxyURL: req.ProxyURL,
	})
	writeLoginOutcome(w, out, err)
}

type loginCodeRequest struct {
	LoginID string `json:"login_id"`
	Code    string `json:"code"`
}

func (s *Server) handleAccountLoginCode(w http.ResponseWriter, r *http.Request) {
	var req loginCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), loginTimeout)
	defer cancel()
	out, err := s.ig.SubmitLoginCode(ctx, req.LoginID, req.Code)
	writeLoginOutcome(w, out, err)
}

type reloginRequest struct {
	Password string `json:"password"` // empty uses the stored password
}

// handleRelogin logs an existing account in again (after a password change
// or when Instagram asked for a security check), keeping its IP.
func (s *Server) handleRelogin(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req reloginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), loginTimeout)
	defer cancel()
	out, err := s.ig.StartLogin(ctx, instagram.LoginInput{AccountID: id, Password: req.Password})
	writeLoginOutcome(w, out, err)
}

func writeLoginOutcome(w http.ResponseWriter, out *instagram.LoginOutcome, err error) {
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.q.CancelAccountJobs(id)
	s.ig.Invalidate(id)

	jobIDs := s.db.Model(&models.Job{}).Select("id").Where("account_id = ?", id)
	s.db.Where("job_id IN (?)", jobIDs).Delete(&models.JobLog{})
	s.db.Where("account_id = ?", id).Delete(&models.Job{})
	if err := s.db.Delete(&models.InstagramAccount{}, id).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSyncAccount(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	acc, err := s.ig.SyncProfile(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

type setProxyRequest struct {
	ProxyURL string `json:"proxy_url"` // empty removes the proxy
}

func (s *Server) handleSetProxy(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req setProxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	proxyURL, err := instagram.ParseProxy(req.ProxyURL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	updates := map[string]interface{}{
		"proxy_url_enc": "", "proxy_display": "", "proxy_last_ip": "", "proxy_checked_at": nil,
	}
	if proxyURL != nil {
		enc, err := s.box.EncryptString(proxyURL.String())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		updates["proxy_url_enc"] = enc
		updates["proxy_display"] = instagram.MaskProxy(proxyURL)
	}
	if err := s.db.Model(&models.InstagramAccount{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.ig.Invalidate(id)

	var acc models.InstagramAccount
	if err := s.db.First(&acc, id).Error; err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleTestProxy(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ip, err := s.ig.TestProxy(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ip": ip})
}
