// Package instagram manages the brand's Instagram accounts on behalf of the
// panel:
//   - adding an account is a plain username + password login (with an
//     in-panel step for the SMS / e-mail / authenticator code Instagram may
//     ask for),
//   - every account talks to Instagram only through its own proxy (its own
//     IP), or the server's IP if none is set,
//   - passwords and sessions (device ids + cookies) are stored encrypted, so
//     an account keeps looking like the same phone on the same IP and is
//     logged in again automatically if Instagram ends the session.
//
// Instagram itself is reached through the local bridge (bridge/bridge.py,
// built on instagrapi). Scope is deliberately limited to managing accounts
// the brand owns: profile, publishing, comments. There is no follow/like/
// DM automation or scraping of other users.
package instagram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"instagrampanel/internal/crypto"
	"instagrampanel/internal/models"
)

const pendingLoginTTL = 15 * time.Minute

type Manager struct {
	db            *gorm.DB
	box           *crypto.Box
	bridge        *Bridge
	proxyCheckURL string

	mu      sync.Mutex
	clients map[uint]*http.Client // proxied clients for IP tests
	pending map[string]*pendingLogin

	reloginMu sync.Mutex
}

func NewManager(db *gorm.DB, box *crypto.Box, bridge *Bridge, proxyCheckURL string) *Manager {
	return &Manager{
		db: db, box: box, bridge: bridge, proxyCheckURL: proxyCheckURL,
		clients: map[uint]*http.Client{}, pending: map[string]*pendingLogin{},
	}
}

var idPattern = regexp.MustCompile(`^[0-9]{1,32}(_[0-9]{1,32})?$`)

func validID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("geçersiz instagram id %q", id)
	}
	return nil
}

// ---------- login ----------

// LoginInput starts a login: a new account (AccountID 0) or logging an
// existing account in again.
type LoginInput struct {
	Label     string
	Username  string
	Password  string // for a re-login, empty means "use the stored password"
	ProxyURL  string // new accounts only
	AccountID uint
}

type pendingLogin struct {
	LoginInput
	proxy    string // normalized proxy URL the login runs through
	setProxy bool   // the login form gave an IP, store it on the account
	expires  time.Time
}

// LoginOutcome is what the panel shows after a login step.
type LoginOutcome struct {
	Status  string                   `json:"status"` // ok | code_required
	LoginID string                   `json:"login_id,omitempty"`
	Kind    string                   `json:"kind,omitempty"` // two_factor | challenge
	Message string                   `json:"message,omitempty"`
	Account *models.InstagramAccount `json:"account,omitempty"`
}

// StartLogin logs in with username + password through the account's proxy.
// If Instagram asks for a code, the returned outcome carries a LoginID to
// pass to SubmitLoginCode.
func (m *Manager) StartLogin(ctx context.Context, in LoginInput) (*LoginOutcome, error) {
	in.Username = strings.TrimPrefix(strings.TrimSpace(in.Username), "@")
	in.Label = strings.TrimSpace(in.Label)
	p := &pendingLogin{LoginInput: in}
	var oldSettings json.RawMessage

	if in.AccountID == 0 {
		u, err := ParseProxy(in.ProxyURL)
		if err != nil {
			return nil, err
		}
		if u != nil {
			p.proxy, p.setProxy = u.String(), true
		}
		// Adding a username that is already in the panel logs that account
		// in again, so it keeps its device identity and, unless a new IP was
		// given, its own IP.
		var existing models.InstagramAccount
		if in.Username != "" && m.db.WithContext(ctx).Where("LOWER(username) = LOWER(?)", in.Username).First(&existing).Error == nil {
			p.AccountID = existing.ID
		}
	}

	if p.AccountID != 0 {
		var acc models.InstagramAccount
		if err := m.db.WithContext(ctx).First(&acc, p.AccountID).Error; err != nil {
			return nil, fmt.Errorf("hesap bulunamadı: %w", err)
		}
		p.Username = acc.Username
		if p.Password == "" {
			pw, err := m.box.DecryptString(acc.PasswordEnc)
			if err != nil {
				return nil, err
			}
			p.Password = pw
		}
		var err error
		if !p.setProxy {
			if p.proxy, err = m.box.DecryptString(acc.ProxyURLEnc); err != nil {
				return nil, err
			}
		}
		if oldSettings, err = m.sessionOf(&acc); err != nil {
			return nil, err
		}
	}
	if p.Username == "" || p.Password == "" {
		return nil, errors.New("kullanıcı adı ve şifre gerekli")
	}

	res, err := m.bridge.login(ctx, p.Username, p.Password, p.proxy, oldSettings)
	if err != nil {
		return nil, err
	}
	return m.finishStep(ctx, p, res)
}

// SubmitLoginCode hands the code Instagram sent to a login in progress.
func (m *Manager) SubmitLoginCode(ctx context.Context, loginID, code string) (*LoginOutcome, error) {
	m.mu.Lock()
	p, ok := m.pending[loginID]
	m.mu.Unlock()
	if !ok || time.Now().After(p.expires) {
		return nil, errors.New("giriş oturumu bulunamadı veya süresi doldu; baştan deneyin")
	}
	res, err := m.bridge.submitCode(ctx, loginID, code)
	if err != nil {
		return nil, err
	}
	return m.finishStep(ctx, p, res)
}

func (m *Manager) finishStep(ctx context.Context, p *pendingLogin, res *LoginResult) (*LoginOutcome, error) {
	m.mu.Lock()
	for id, pl := range m.pending {
		if time.Now().After(pl.expires) {
			delete(m.pending, id)
		}
	}
	if res.Status == "code_required" {
		p.expires = time.Now().Add(pendingLoginTTL)
		m.pending[res.LoginID] = p
		m.mu.Unlock()
		return &LoginOutcome{Status: "code_required", LoginID: res.LoginID, Kind: res.Kind, Message: res.Message}, nil
	}
	delete(m.pending, res.LoginID)
	m.mu.Unlock()

	acc, err := m.saveLogin(ctx, p, res)
	if err != nil {
		return nil, err
	}
	return &LoginOutcome{Status: "ok", Account: acc}, nil
}

// saveLogin stores a successful login: a new account, the matching existing
// account (same Instagram account added again), or the re-logged account.
func (m *Manager) saveLogin(ctx context.Context, p *pendingLogin, res *LoginResult) (*models.InstagramAccount, error) {
	user := res.User
	if user == nil || user.PK == "" {
		return nil, errors.New("instagram hesap bilgisi alınamadı")
	}

	var acc models.InstagramAccount
	found := false
	if p.AccountID != 0 {
		if err := m.db.WithContext(ctx).First(&acc, p.AccountID).Error; err != nil {
			return nil, errors.New("hesap panelde artık yok")
		}
		if acc.IGUserID != "" && acc.IGUserID != user.PK {
			return nil, fmt.Errorf("farklı bir hesapla (@%s) giriş yapıldı", user.Username)
		}
		found = true
	} else if err := m.db.WithContext(ctx).Where("ig_user_id = ?", user.PK).First(&acc).Error; err == nil {
		found = true
	}

	encPass, err := m.box.EncryptString(p.Password)
	if err != nil {
		return nil, err
	}
	encSession, err := m.box.Encrypt(res.Settings)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	updates := map[string]interface{}{
		"password_enc": encPass, "session_enc": encSession, "logged_in_at": &now,
	}
	for k, v := range profileUpdates(user) {
		updates[k] = v
	}
	// An IP given in the login form (new account, or a new IP for an
	// account added again) is stored on the account.
	if p.setProxy || !found {
		encProxy, display := "", ""
		if p.proxy != "" {
			u, _ := ParseProxy(p.proxy)
			if encProxy, err = m.box.EncryptString(p.proxy); err != nil {
				return nil, err
			}
			display = MaskProxy(u)
		}
		updates["proxy_url_enc"], updates["proxy_display"] = encProxy, display
		updates["proxy_last_ip"], updates["proxy_checked_at"] = "", nil
	}

	if !found {
		label := p.Label
		if label == "" {
			label = "@" + user.Username
		}
		acc = models.InstagramAccount{Label: label, Status: models.AccountActive}
		if err := m.db.WithContext(ctx).Create(&acc).Error; err != nil {
			return nil, err
		}
	} else if p.Label != "" {
		updates["label"] = p.Label
	}
	if err := m.db.WithContext(ctx).Model(&models.InstagramAccount{}).Where("id = ?", acc.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	m.Invalidate(acc.ID)

	err = m.db.WithContext(ctx).First(&acc, acc.ID).Error
	return &acc, err
}

func profileUpdates(u *BridgeUser) map[string]interface{} {
	now := time.Now()
	return map[string]interface{}{
		"ig_user_id": u.PK, "username": u.Username, "name": u.FullName,
		"account_type": u.AccountType, "profile_picture_url": u.ProfilePicURL,
		"followers_count": u.FollowerCount, "follows_count": u.FollowingCount, "media_count": u.MediaCount,
		"status": models.AccountActive, "last_error": "", "last_synced_at": &now,
	}
}

// relogin logs an account in again with its stored password after
// Instagram ended its session, keeping the same device identity.
func (m *Manager) relogin(ctx context.Context, accountID uint) error {
	started := time.Now()
	m.reloginMu.Lock()
	defer m.reloginMu.Unlock()
	// Several workers of one job can hit the ended session at once; only the
	// first one logs in, the rest reuse its fresh session.
	var acc models.InstagramAccount
	if err := m.db.WithContext(ctx).First(&acc, accountID).Error; err == nil && acc.LoggedInAt != nil && acc.LoggedInAt.After(started) {
		return nil
	}
	out, err := m.StartLogin(ctx, LoginInput{AccountID: accountID})
	if err != nil {
		return err
	}
	if out.Status != "ok" {
		return errors.New("Instagram doğrulama kodu istiyor; hesabın sayfasından \"Yeniden Giriş Yap\" ile girin")
	}
	log.Printf("[instagram] account %d logged in again automatically", accountID)
	return nil
}

// ---------- account calls ----------

func (m *Manager) sessionOf(acc *models.InstagramAccount) (json.RawMessage, error) {
	if acc.SessionEnc == "" {
		return nil, nil
	}
	b, err := m.box.Decrypt(acc.SessionEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt session: %w", err)
	}
	return json.RawMessage(b), nil
}

// call runs one bridge call as an account, stores the session cookies the
// call may have rotated, and logs in again once with the stored password if
// Instagram ended the session.
func (m *Manager) call(ctx context.Context, accountID uint, path string, params map[string]interface{}, out interface{}) error {
	err := m.callOnce(ctx, accountID, path, params, out)
	if bridgeKind(err) == "login_required" {
		if rerr := m.relogin(ctx, accountID); rerr != nil {
			m.markError(accountID, "Oturum sona erdi ve otomatik giriş yapılamadı: "+rerr.Error())
			return fmt.Errorf("oturum sona erdi ve otomatik giriş yapılamadı: %w", rerr)
		}
		err = m.callOnce(ctx, accountID, path, params, out)
	}
	if k := bridgeKind(err); k == "challenge" || k == "ip_blocked" || k == "login_required" {
		m.markError(accountID, err.Error())
	}
	return err
}

func (m *Manager) callOnce(ctx context.Context, accountID uint, path string, params map[string]interface{}, out interface{}) error {
	var acc models.InstagramAccount
	if err := m.db.WithContext(ctx).First(&acc, accountID).Error; err != nil {
		return fmt.Errorf("hesap bulunamadı: %w", err)
	}
	session, err := m.sessionOf(&acc)
	if err != nil {
		return err
	}
	proxy, err := m.box.DecryptString(acc.ProxyURLEnc)
	if err != nil {
		return err
	}

	req := map[string]interface{}{"account_id": acc.ID, "proxy": proxy, "settings": session}
	for k, v := range params {
		req[k] = v
	}
	var raw []byte
	err = WithRetry(ctx, func() error {
		var callErr error
		raw, callErr = m.bridge.call(ctx, path, req)
		return callErr
	})
	if err != nil {
		return err
	}

	var envelope struct {
		Settings json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Settings) > 0 && !bytes.Equal(envelope.Settings, session) {
		if enc, err := m.box.Encrypt(envelope.Settings); err == nil {
			m.db.Model(&models.InstagramAccount{}).Where("id = ?", acc.ID).Update("session_enc", enc)
		}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (m *Manager) markError(accountID uint, msg string) {
	m.db.Model(&models.InstagramAccount{}).Where("id = ?", accountID).
		Updates(map[string]interface{}{"status": models.AccountError, "last_error": msg})
}

// SyncProfile refreshes the account's profile (followers, posts...). A
// failure is recorded on the account so the panel can show it.
func (m *Manager) SyncProfile(ctx context.Context, accountID uint) (models.InstagramAccount, error) {
	var resp struct {
		User *BridgeUser `json:"user"`
	}
	callErr := m.call(ctx, accountID, "/account/info", nil, &resp)
	if callErr != nil {
		m.markError(accountID, callErr.Error())
	} else if resp.User != nil {
		m.db.WithContext(ctx).Model(&models.InstagramAccount{}).Where("id = ?", accountID).Updates(profileUpdates(resp.User))
	}
	var acc models.InstagramAccount
	err := m.db.WithContext(ctx).First(&acc, accountID).Error
	return acc, err
}

// ---------- IP / proxy ----------

// httpClient returns the account's cached proxied HTTP client (used to test
// which IP the account appears from), building it on first use.
func (m *Manager) httpClient(acc *models.InstagramAccount) (*http.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[acc.ID]; ok {
		return c, nil
	}
	raw, err := m.box.DecryptString(acc.ProxyURLEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt proxy url: %w", err)
	}
	u, err := ParseProxy(raw)
	if err != nil {
		return nil, err
	}
	c, err := newHTTPClient(u)
	if err != nil {
		return nil, err
	}
	m.clients[acc.ID] = c
	return c, nil
}

// Invalidate drops cached connections for an account so a changed proxy or
// session is picked up on the next call.
func (m *Manager) Invalidate(accountID uint) {
	m.mu.Lock()
	c, ok := m.clients[accountID]
	delete(m.clients, accountID)
	m.mu.Unlock()
	if ok {
		c.CloseIdleConnections()
	}
	if m.bridge != nil {
		_, _ = m.bridge.call(context.Background(), "/account/forget", map[string]interface{}{"account_id": accountID})
	}
}

// TestProxy fetches the IP-check URL through the account's proxy and
// records the IP address the account appears from.
func (m *Manager) TestProxy(ctx context.Context, accountID uint) (string, error) {
	var acc models.InstagramAccount
	if err := m.db.WithContext(ctx).First(&acc, accountID).Error; err != nil {
		return "", fmt.Errorf("hesap bulunamadı: %w", err)
	}
	hc, err := m.httpClient(&acc)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proxyCheckURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", fmt.Errorf("IP'ye bağlanılamadı: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ip kontrol servisi HTTP %d döndü", resp.StatusCode)
	}

	ip := strings.TrimSpace(string(body))
	var parsed struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.IP != "" {
		ip = parsed.IP
	}
	if len(ip) > 64 {
		ip = ip[:64]
	}

	now := time.Now()
	m.db.WithContext(ctx).Model(&models.InstagramAccount{}).Where("id = ?", accountID).
		Updates(map[string]interface{}{"proxy_last_ip": ip, "proxy_checked_at": &now})
	return ip, nil
}
