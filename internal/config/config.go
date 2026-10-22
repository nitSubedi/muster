// Package config persists the node's identity and cluster token.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is stored as JSON with mode 0600 because it holds the token.
type Config struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token"`
}

// DefaultPath is <user config dir>/muster/config.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "muster", "config.json"), nil
}

// LoadOrCreate reads path, creating it with a fresh node ID and token if it
// does not exist. created reports whether a new file was written.
func LoadOrCreate(path string) (cfg Config, created bool, err error) {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = Config{NodeID: randomString(8), Token: randomString(32)}
		return cfg, true, Save(path, cfg)
	case err != nil:
		return Config{}, false, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.NodeID == "" || cfg.Token == "" {
		return Config{}, false, fmt.Errorf("%s is missing node_id or token", path)
	}
	return cfg, false, nil
}

// Save writes cfg atomically with owner-only permissions.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClusterID is a public, non-reversible identifier for the token, safe to
// broadcast so agents can ignore beacons from other clusters.
func ClusterID(token string) string {
	sum := sha256.Sum256([]byte("muster-cluster:" + token))
	return hex.EncodeToString(sum[:8])
}

// TokenEqual compares tokens in constant time.
func TokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
