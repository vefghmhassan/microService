package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port          string
	DataDir       string
	DBPath        string
	AdminUsername string
	AdminPassword string
	JWTSecret     []byte
	MasterKey     []byte
	CookieSecure  bool
}

// Load reads .env (when present) and the process environment.
//
// JWT_SECRET and MASTER_KEY are generated on first boot and kept in DATA_DIR
// when they are not set, so a fresh install works without editing anything but
// never falls back to a shared, well-known secret.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Port:          env("APP_PORT", "8090"),
		DataDir:       env("DATA_DIR", "./data"),
		AdminUsername: env("ADMIN_USERNAME", "admin"),
		AdminPassword: env("ADMIN_PASSWORD", ""),
		CookieSecure:  strings.EqualFold(env("COOKIE_SECURE", "false"), "true"),
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	cfg.DBPath = filepath.Join(cfg.DataDir, "microserver.db")

	var err error
	if cfg.JWTSecret, err = secretFromEnvOrFile("JWT_SECRET", filepath.Join(cfg.DataDir, "jwt.secret"), 32); err != nil {
		return nil, err
	}
	if cfg.MasterKey, err = secretFromEnvOrFile("MASTER_KEY", filepath.Join(cfg.DataDir, "master.key"), 32); err != nil {
		return nil, err
	}
	if len(cfg.MasterKey) != 32 {
		return nil, fmt.Errorf("MASTER_KEY must be 32 bytes (64 hex chars), got %d bytes", len(cfg.MasterKey))
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// secretFromEnvOrFile returns the hex-decoded env value, else the hex contents
// of path, else a new random secret that is written to path.
func secretFromEnvOrFile(key, path string, size int) ([]byte, error) {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		b, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("%s must be hex: %w", key, err)
		}
		return b, nil
	}
	if raw, err := os.ReadFile(path); err == nil {
		b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return b, nil
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(b)), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return b, nil
}
