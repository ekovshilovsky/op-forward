package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	TokenLength   = 32 // 32 bytes = 64 hex chars
	DefaultTTL    = 30 * 24 * time.Hour
	RenewalFactor = 0.5 // Renew when less than half TTL remains
	TokenFileName = "session.token"
	CacheDirName  = "op-forward"
)

// Token represents an authentication token with expiry.
type Token struct {
	Value   string
	Expires time.Time
}

// Generate creates a new random token with the default TTL.
func Generate() (*Token, error) {
	bytes := make([]byte, TokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	return &Token{
		Value:   hex.EncodeToString(bytes),
		Expires: time.Now().Add(DefaultTTL),
	}, nil
}

// IsValid returns true if the token hasn't expired.
func (t *Token) IsValid() bool {
	return time.Now().Before(t.Expires)
}

// ShouldRenew returns true if the token should be renewed (past half TTL).
func (t *Token) ShouldRenew() bool {
	remaining := time.Until(t.Expires)
	return remaining < time.Duration(float64(DefaultTTL)*RenewalFactor)
}

// Renew extends the token expiry by the default TTL.
func (t *Token) Renew() {
	t.Expires = time.Now().Add(DefaultTTL)
}

// TokenDir returns the directory for storing tokens.
func TokenDir() (string, error) {
	if dir := os.Getenv("OP_FORWARD_TOKEN_DIR"); dir != "" {
		return dir, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determining cache directory: %w", err)
	}
	return filepath.Join(cacheDir, CacheDirName), nil
}

// TokenPath returns the full path to the token file.
func TokenPath() (string, error) {
	if path := os.Getenv("OP_FORWARD_TOKEN_FILE"); path != "" {
		return path, nil
	}
	dir, err := TokenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, TokenFileName), nil
}

// Save writes the token to disk.
func (t *Token) Save() error {
	path, err := TokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	content := fmt.Sprintf("%s\n%s\n", t.Value, t.Expires.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("writing token: %w", err)
	}
	return nil
}

// Load reads an existing token from disk.
func Load() (*Token, error) {
	path, err := TokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading token: %w", err)
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) != 2 {
		return nil, fmt.Errorf("invalid token file format")
	}
	expires, err := time.Parse(time.RFC3339, lines[1])
	if err != nil {
		return nil, fmt.Errorf("parsing token expiry: %w", err)
	}
	return &Token{Value: lines[0], Expires: expires}, nil
}

// LoadOrGenerate loads an existing valid token or generates a new one.
func LoadOrGenerate() (*Token, bool, error) {
	token, err := Load()
	if err == nil && token.IsValid() {
		return token, false, nil
	}
	token, err = Generate()
	if err != nil {
		return nil, false, err
	}
	if err := token.Save(); err != nil {
		return nil, false, err
	}
	return token, true, nil
}
