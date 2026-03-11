package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerate(t *testing.T) {
	token, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(token.Value) != TokenLength*2 { // hex encoding doubles length
		t.Errorf("token length = %d, want %d hex chars", len(token.Value), TokenLength*2)
	}
	if !token.IsValid() {
		t.Error("newly generated token should be valid")
	}
	if token.ShouldRenew() {
		t.Error("newly generated token should not need renewal")
	}
}

func TestGenerateUniqueness(t *testing.T) {
	t1, _ := Generate()
	t2, _ := Generate()
	if t1.Value == t2.Value {
		t.Error("two generated tokens should not be identical")
	}
}

func TestIsValid(t *testing.T) {
	token := &Token{Value: "test", Expires: time.Now().Add(time.Hour)}
	if !token.IsValid() {
		t.Error("token with future expiry should be valid")
	}

	expired := &Token{Value: "test", Expires: time.Now().Add(-time.Hour)}
	if expired.IsValid() {
		t.Error("token with past expiry should be invalid")
	}
}

func TestShouldRenew(t *testing.T) {
	// Token with most of TTL remaining — should NOT renew
	fresh := &Token{Value: "test", Expires: time.Now().Add(DefaultTTL)}
	if fresh.ShouldRenew() {
		t.Error("fresh token should not need renewal")
	}

	// Token with less than half TTL remaining — SHOULD renew
	stale := &Token{Value: "test", Expires: time.Now().Add(DefaultTTL / 4)}
	if !stale.ShouldRenew() {
		t.Error("token past half TTL should need renewal")
	}
}

func TestRenew(t *testing.T) {
	token := &Token{Value: "test", Expires: time.Now().Add(time.Hour)}
	token.Renew()

	// After renewal, expiry should be approximately DefaultTTL from now
	expected := time.Now().Add(DefaultTTL)
	diff := token.Expires.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("renewed expiry off by %v", diff)
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(tmpDir, "test.token"))

	original, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if err := original.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if loaded.Value != original.Value {
		t.Errorf("loaded value = %q, want %q", loaded.Value, original.Value)
	}
	// Compare within 1-second tolerance (RFC3339 only has second precision)
	if loaded.Expires.Sub(original.Expires).Abs() > time.Second {
		t.Errorf("loaded expiry = %v, want %v", loaded.Expires, original.Expires)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("OP_FORWARD_TOKEN_FILE", "/nonexistent/path/token")
	_, err := Load()
	if err == nil {
		t.Error("Load() should fail for missing file")
	}
}

func TestLoadInvalidFormat(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "bad.token")
	t.Setenv("OP_FORWARD_TOKEN_FILE", tokenPath)

	// Single line (missing expiry)
	os.WriteFile(tokenPath, []byte("tokenvalue\n"), 0600)
	_, err := Load()
	if err == nil {
		t.Error("Load() should fail for single-line token file")
	}
}

func TestLoadInvalidExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "bad.token")
	t.Setenv("OP_FORWARD_TOKEN_FILE", tokenPath)

	os.WriteFile(tokenPath, []byte("tokenvalue\nnot-a-date\n"), 0600)
	_, err := Load()
	if err == nil {
		t.Error("Load() should fail for invalid expiry format")
	}
}

func TestLoadOrGenerate_NewToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(tmpDir, "test.token"))

	token, isNew, err := LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate() error: %v", err)
	}
	if !isNew {
		t.Error("should report token as new when no file exists")
	}
	if !token.IsValid() {
		t.Error("generated token should be valid")
	}
}

func TestLoadOrGenerate_ExistingValid(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(tmpDir, "test.token"))

	// Create and save a valid token
	first, _ := Generate()
	first.Save()

	// LoadOrGenerate should return the existing token
	loaded, isNew, err := LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate() error: %v", err)
	}
	if isNew {
		t.Error("should not report as new when valid token exists")
	}
	if loaded.Value != first.Value {
		t.Error("should return the existing token")
	}
}

func TestLoadOrGenerate_ExpiredToken(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "test.token")
	t.Setenv("OP_FORWARD_TOKEN_FILE", tokenPath)

	// Write an expired token
	expired := &Token{Value: "expired", Expires: time.Now().Add(-time.Hour)}
	expired.Save()

	// LoadOrGenerate should create a new one
	token, isNew, err := LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate() error: %v", err)
	}
	if !isNew {
		t.Error("should generate new token when existing is expired")
	}
	if token.Value == "expired" {
		t.Error("should not return the expired token value")
	}
}

func TestTokenDir_EnvOverride(t *testing.T) {
	t.Setenv("OP_FORWARD_TOKEN_DIR", "/custom/path")
	dir, err := TokenDir()
	if err != nil {
		t.Fatalf("TokenDir() error: %v", err)
	}
	if dir != "/custom/path" {
		t.Errorf("TokenDir() = %q, want /custom/path", dir)
	}
}

func TestTokenPath_EnvOverride(t *testing.T) {
	t.Setenv("OP_FORWARD_TOKEN_FILE", "/custom/token")
	path, err := TokenPath()
	if err != nil {
		t.Fatalf("TokenPath() error: %v", err)
	}
	if path != "/custom/token" {
		t.Errorf("TokenPath() = %q, want /custom/token", path)
	}
}

func TestTokenFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "test.token")
	t.Setenv("OP_FORWARD_TOKEN_FILE", tokenPath)

	token, _ := Generate()
	token.Save()

	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("token file permissions = %o, want 0600", perm)
	}
}
