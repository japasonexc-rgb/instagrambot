package instagram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Bridge talks to bridge/bridge.py, the local helper that logs in to
// Instagram with username + password (via the instagrapi library) and makes
// every call as that account, through that account's proxy.
type Bridge struct {
	baseURL string
	token   string
	http    *http.Client
}

// BridgeError is an error reported by the bridge; Kind tells the panel how
// to react (re-login, wait, change IP...).
type BridgeError struct {
	Kind    string `json:"kind"` // bad_password | challenge | login_required | rate_limited | feedback | ip_blocked | network | other
	Message string `json:"message"`
}

func (e *BridgeError) Error() string { return e.Message }

func bridgeKind(err error) string {
	var be *BridgeError
	if errors.As(err, &be) {
		return be.Kind
	}
	return ""
}

// NewBridge connects to an already running bridge (e.g. a separate Docker
// service).
func NewBridge(baseURL, token string) *Bridge {
	return &Bridge{baseURL: baseURL, token: token, http: &http.Client{Timeout: 10 * time.Minute}}
}

// StartBridge runs bridge.py as a child process on a free local port with a
// fresh random token, restarting it if it ever exits.
func StartBridge(python, script string) (*Bridge, error) {
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("köprü betiği bulunamadı (%s): %w", script, err)
	}
	if _, err := exec.LookPath(python); err != nil {
		return nil, fmt.Errorf("python bulunamadı (%s); README'deki kurulum adımını uygulayın: %w", python, err)
	}
	// The bridge runs inside its own folder, so a relative interpreter path
	// (bridge/.venv/bin/python) must be resolved first.
	if strings.ContainsRune(python, filepath.Separator) {
		if abs, err := filepath.Abs(python); err == nil {
			python = abs
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	b := NewBridge("http://127.0.0.1:"+strconv.Itoa(port), hex.EncodeToString(tok))

	go func() {
		for {
			cmd := exec.Command(python, filepath.Base(script), "--port", strconv.Itoa(port))
			cmd.Dir = filepath.Dir(script)
			cmd.Env = append(os.Environ(), "BRIDGE_TOKEN="+b.token, "PYTHONUNBUFFERED=1")
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			err := cmd.Run()
			log.Printf("[bridge] exited (%v), restarting in 3s", err)
			time.Sleep(3 * time.Second)
		}
	}()

	if err := b.waitHealthy(45 * time.Second); err != nil {
		return b, err
	}
	return b, nil
}

func (b *Bridge) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, b.baseURL+"/health", nil)
		req.Header.Set("X-Bridge-Token", b.token)
		if resp, err := b.http.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("instagram köprüsü başlamadı; README'deki 'pip install -r bridge/requirements.txt' adımını kontrol edin")
}

// call POSTs req to the bridge and returns the raw JSON of a successful
// reply, or a *BridgeError.
func (b *Bridge) call(ctx context.Context, path string, req interface{}) ([]byte, error) {
	if b == nil {
		return nil, &BridgeError{Kind: "other", Message: "instagram köprüsü çalışmıyor; sunucu loglarına ve README'deki kuruluma bakın"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Bridge-Token", b.token)

	resp, err := b.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &BridgeError{Kind: "other", Message: "instagram köprüsüne ulaşılamadı: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		OK    bool         `json:"ok"`
		Error *BridgeError `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("köprü yanıtı okunamadı (HTTP %d)", resp.StatusCode)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			envelope.Error = &BridgeError{Kind: "other", Message: fmt.Sprintf("köprü hatası (HTTP %d)", resp.StatusCode)}
		}
		return nil, envelope.Error
	}
	return raw, nil
}

// BridgeUser is the profile the bridge reports for a logged-in account.
type BridgeUser struct {
	PK             string `json:"pk"`
	Username       string `json:"username"`
	FullName       string `json:"full_name"`
	ProfilePicURL  string `json:"profile_pic_url"`
	FollowerCount  int    `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	MediaCount     int    `json:"media_count"`
	AccountType    string `json:"account_type"`
}

// LoginResult is the outcome of a login step: either logged in ("ok", with
// the session to store) or Instagram wants a code ("code_required").
type LoginResult struct {
	Status   string          `json:"status"` // ok | code_required
	LoginID  string          `json:"login_id"`
	Kind     string          `json:"kind"` // two_factor | challenge
	Message  string          `json:"message"`
	Settings json.RawMessage `json:"settings"`
	User     *BridgeUser     `json:"user"`
}

func (b *Bridge) login(ctx context.Context, username, password, proxy string, oldSettings json.RawMessage) (*LoginResult, error) {
	req := map[string]interface{}{"username": username, "password": password, "proxy": proxy}
	if len(oldSettings) > 0 {
		req["settings"] = oldSettings
	}
	raw, err := b.call(ctx, "/login", req)
	if err != nil {
		return nil, err
	}
	var res LoginResult
	return &res, json.Unmarshal(raw, &res)
}

func (b *Bridge) submitCode(ctx context.Context, loginID, code string) (*LoginResult, error) {
	raw, err := b.call(ctx, "/login/code", map[string]string{"login_id": loginID, "code": code})
	if err != nil {
		return nil, err
	}
	var res LoginResult
	return &res, json.Unmarshal(raw, &res)
}
