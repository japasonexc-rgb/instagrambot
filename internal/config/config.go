// Package config loads server bootstrap configuration.
//
// Only infrastructure-level secrets live here (HTTP port, DB connection,
// JWT signing secret, master encryption key). Instagram usernames,
// passwords and per-account proxies are entered through the web panel and
// stored encrypted in the database, never in environment files.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr      string
	DBDriver      string // "sqlite" or "postgres"
	DBDSN         string
	JWTSecret     []byte
	EncryptionKey []byte // 32 bytes, used for AES-256-GCM
	CORSOrigin    string
	// BridgeURL points at an already running Instagram bridge (e.g. a
	// separate Docker service). When empty the panel starts bridge/bridge.py
	// itself with BridgePython.
	BridgeURL    string
	BridgeToken  string
	BridgePython string
	// ProxyCheckURL is fetched through an account's proxy to report the
	// outbound IP the account's requests appear from. It must return either
	// {"ip":"..."} JSON or the bare IP as plain text.
	ProxyCheckURL string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	httpAddr := getEnv("HTTP_ADDR", ":8081")
	if port := os.Getenv("PORT"); port != "" {
		httpAddr = ":" + port
	}

	cfg := &Config{
		HTTPAddr:      httpAddr,
		DBDriver:      getEnv("DB_DRIVER", "sqlite"),
		DBDSN:         getEnv("DB_DSN", "data/instagram.db"),
		CORSOrigin:    getEnv("CORS_ORIGIN", "*"),
		ProxyCheckURL: getEnv("PROXY_CHECK_URL", "https://api.ipify.org?format=json"),
		BridgeURL:     strings.TrimRight(getEnv("BRIDGE_URL", ""), "/"),
		BridgeToken:   getEnv("BRIDGE_TOKEN", ""),
		BridgePython:  getEnv("BRIDGE_PYTHON", defaultBridgePython()),
	}

	jwtSecret := getEnv("JWT_SECRET", "")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required (set it in .env)")
	}
	cfg.JWTSecret = []byte(jwtSecret)

	encKeyHex := getEnv("ENCRYPTION_KEY", "")
	if encKeyHex == "" {
		return nil, fmt.Errorf("ENCRYPTION_KEY is required: 64 hex chars (32 bytes) for AES-256-GCM, generate with: openssl rand -hex 32")
	}
	key, err := hex.DecodeString(encKeyHex)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be valid hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must decode to exactly 32 bytes, got %d", len(key))
	}
	cfg.EncryptionKey = key

	return cfg, nil
}

// defaultBridgePython prefers the virtualenv created by the README setup step.
func defaultBridgePython() string {
	if _, err := os.Stat("bridge/.venv/bin/python"); err == nil {
		return "bridge/.venv/bin/python"
	}
	return "python3"
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
