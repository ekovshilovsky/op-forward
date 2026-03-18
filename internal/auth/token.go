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
	TokenLength    = 32 // 32 bytes = 64 hex chars
	AccessTokenTTL = 1 * time.Hour
	// RefreshTokenTTL is the sliding window for the refresh token. The token
	// remains valid as long as it is used at least once within this period.
	RefreshTokenTTL = 30 * 24 * time.Hour
	RenewalFactor   = 0.5 // Renew when less than half TTL remains

	AccessTokenFile  = "access.token"
	RefreshTokenFile = "refresh.token"
	LegacyTokenFile  = "session.token"
	CacheDirName     = "op-forward"
)

// Token represents an authentication token with expiry and an associated TTL
// that governs renewal behaviour. The TTL field is not persisted to disk.
type Token struct {
	Value   string
	Expires time.Time
	TTL     time.Duration
}

// GenerateAccess creates a new random access token with a 1-hour TTL.
func GenerateAccess() (*Token, error) {
	return generate(AccessTokenTTL)
}

// GenerateRefresh creates a new random refresh token with a 30-day TTL.
func GenerateRefresh() (*Token, error) {
	return generate(RefreshTokenTTL)
}

func generate(ttl time.Duration) (*Token, error) {
	b := make([]byte, TokenLength)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating token: %w", err)
	}
	return &Token{
		Value:   hex.EncodeToString(b),
		Expires: time.Now().Add(ttl),
		TTL:     ttl,
	}, nil
}

// IsValid returns true if the token hasn't expired.
func (t *Token) IsValid() bool {
	return time.Now().Before(t.Expires)
}

// ShouldRenew returns true if the token should be renewed (past half its TTL).
func (t *Token) ShouldRenew() bool {
	remaining := time.Until(t.Expires)
	return remaining < time.Duration(float64(t.TTL)*RenewalFactor)
}

// Renew extends the token expiry by its TTL from now (sliding window).
func (t *Token) Renew() {
	t.Expires = time.Now().Add(t.TTL)
}

// ---------- Path helpers ----------

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

func tokenFilePath(name string) (string, error) {
	dir, err := TokenDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// AccessTokenPath returns the full path to the access token file.
// Respects OP_FORWARD_TOKEN_FILE for backward compatibility.
func AccessTokenPath() (string, error) {
	if path := os.Getenv("OP_FORWARD_TOKEN_FILE"); path != "" {
		return path, nil
	}
	return tokenFilePath(AccessTokenFile)
}

// RefreshTokenPath returns the full path to the refresh token file.
func RefreshTokenPath() (string, error) {
	return tokenFilePath(RefreshTokenFile)
}

// LegacyTokenPath returns the full path to the legacy session.token file.
func LegacyTokenPath() (string, error) {
	return tokenFilePath(LegacyTokenFile)
}

// TokenPath returns the access token path. Kept for backward compatibility
// with code that references the old single-token API.
func TokenPath() (string, error) {
	return AccessTokenPath()
}

// ---------- Persistence ----------

// SaveToPath writes a token to the specified path atomically.
func SaveToPath(t *Token, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	content := fmt.Sprintf("%s\n%s\n", t.Value, t.Expires.Format(time.RFC3339))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return fmt.Errorf("writing token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming token file: %w", err)
	}
	return nil
}

// LoadFromPath reads a token from the specified path.
// The TTL field is not stored on disk, so the caller must set it.
func LoadFromPath(path string) (*Token, error) {
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

// Save writes the token to the default access token path.
// Retained for backward compatibility.
func (t *Token) Save() error {
	path, err := AccessTokenPath()
	if err != nil {
		return err
	}
	return SaveToPath(t, path)
}

// Load reads the token from the default access token path.
// Retained for backward compatibility.
func Load() (*Token, error) {
	path, err := AccessTokenPath()
	if err != nil {
		return nil, err
	}
	return LoadFromPath(path)
}

// ---------- Lifecycle orchestration ----------

// LoadOrGenerateRefresh loads the refresh token from disk. If missing or
// expired it generates a new one. Returns the token and whether it was
// newly generated.
func LoadOrGenerateRefresh() (*Token, bool, error) {
	path, err := RefreshTokenPath()
	if err != nil {
		return nil, false, err
	}
	t, err := LoadFromPath(path)
	if err == nil && t.IsValid() {
		t.TTL = RefreshTokenTTL
		return t, false, nil
	}
	t, err = GenerateRefresh()
	if err != nil {
		return nil, false, err
	}
	if err := SaveToPath(t, path); err != nil {
		return nil, false, err
	}
	return t, true, nil
}

// MigrateLegacyToken checks for a legacy session.token and, if the new
// refresh.token does not yet exist, migrates it. This allows users who
// upgrade from the single-token system to keep working without a redeploy.
func MigrateLegacyToken() error {
	refreshPath, err := RefreshTokenPath()
	if err != nil {
		return err
	}
	// Nothing to migrate if refresh token already exists.
	if _, err := os.Stat(refreshPath); err == nil {
		return nil
	}

	legacyPath, err := LegacyTokenPath()
	if err != nil {
		return err
	}
	t, err := LoadFromPath(legacyPath)
	if err != nil {
		// No legacy token either; nothing to do.
		return nil
	}
	t.TTL = RefreshTokenTTL
	if !t.IsValid() {
		// Expired legacy token — migration cannot help.
		return nil
	}
	return SaveToPath(t, refreshPath)
}

// LoadOrGenerate loads a valid token or generates a new one.
// Retained for backward compatibility with tests and older code paths.
func LoadOrGenerate() (*Token, bool, error) {
	token, err := Load()
	if err == nil && token.IsValid() {
		token.TTL = AccessTokenTTL
		return token, false, nil
	}
	token, err = GenerateAccess()
	if err != nil {
		return nil, false, err
	}
	if err := token.Save(); err != nil {
		return nil, false, err
	}
	return token, true, nil
}
