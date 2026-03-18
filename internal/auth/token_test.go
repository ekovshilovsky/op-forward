package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateAccess(t *testing.T) {
	token, err := GenerateAccess()
	if err != nil {
		t.Fatalf("GenerateAccess() error: %v", err)
	}
	if len(token.Value) != TokenLength*2 {
		t.Errorf("token length = %d, want %d hex chars", len(token.Value), TokenLength*2)
	}
	if !token.IsValid() {
		t.Error("newly generated token should be valid")
	}
	if token.TTL != AccessTokenTTL {
		t.Errorf("TTL = %v, want %v", token.TTL, AccessTokenTTL)
	}
}

func TestGenerateRefresh(t *testing.T) {
	token, err := GenerateRefresh()
	if err != nil {
		t.Fatalf("GenerateRefresh() error: %v", err)
	}
	if len(token.Value) != TokenLength*2 {
		t.Errorf("token length = %d, want %d hex chars", len(token.Value), TokenLength*2)
	}
	if token.TTL != RefreshTokenTTL {
		t.Errorf("TTL = %v, want %v", token.TTL, RefreshTokenTTL)
	}
}

func TestGenerateUniqueness(t *testing.T) {
	t1, _ := GenerateAccess()
	t2, _ := GenerateAccess()
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
	// Access token with most of TTL remaining — should NOT renew
	fresh := &Token{Value: "test", Expires: time.Now().Add(AccessTokenTTL), TTL: AccessTokenTTL}
	if fresh.ShouldRenew() {
		t.Error("fresh token should not need renewal")
	}

	// Access token with less than half TTL remaining — SHOULD renew
	stale := &Token{Value: "test", Expires: time.Now().Add(AccessTokenTTL / 4), TTL: AccessTokenTTL}
	if !stale.ShouldRenew() {
		t.Error("token past half TTL should need renewal")
	}

	// Refresh token with most of TTL remaining — should NOT renew
	freshRefresh := &Token{Value: "test", Expires: time.Now().Add(RefreshTokenTTL), TTL: RefreshTokenTTL}
	if freshRefresh.ShouldRenew() {
		t.Error("fresh refresh token should not need renewal")
	}

	// Refresh token with less than half TTL remaining — SHOULD renew
	staleRefresh := &Token{Value: "test", Expires: time.Now().Add(RefreshTokenTTL / 4), TTL: RefreshTokenTTL}
	if !staleRefresh.ShouldRenew() {
		t.Error("refresh token past half TTL should need renewal")
	}
}

func TestRenew(t *testing.T) {
	token := &Token{Value: "test", Expires: time.Now().Add(time.Minute), TTL: AccessTokenTTL}
	token.Renew()

	expected := time.Now().Add(AccessTokenTTL)
	diff := token.Expires.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("renewed expiry off by %v", diff)
	}
}

func TestRenewRefreshToken(t *testing.T) {
	token := &Token{Value: "test", Expires: time.Now().Add(time.Hour), TTL: RefreshTokenTTL}
	token.Renew()

	expected := time.Now().Add(RefreshTokenTTL)
	diff := token.Expires.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("renewed refresh expiry off by %v", diff)
	}
}

func TestSaveToPathAndLoadFromPath(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.token")

	original, _ := GenerateAccess()
	if err := SaveToPath(original, path); err != nil {
		t.Fatalf("SaveToPath() error: %v", err)
	}

	loaded, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath() error: %v", err)
	}

	if loaded.Value != original.Value {
		t.Errorf("loaded value = %q, want %q", loaded.Value, original.Value)
	}
	if loaded.Expires.Sub(original.Expires).Abs() > time.Second {
		t.Errorf("loaded expiry = %v, want %v", loaded.Expires, original.Expires)
	}
}

func TestSaveToPath_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "atomic.token")

	token, _ := GenerateAccess()
	if err := SaveToPath(token, path); err != nil {
		t.Fatalf("SaveToPath() error: %v", err)
	}

	// Verify no .tmp file left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file should not exist after successful save")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestSaveAndLoad_BackwardCompat(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(tmpDir, "test.token"))

	original, _ := GenerateAccess()
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

	first, _ := GenerateAccess()
	first.Save()

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

	expired := &Token{Value: "expired", Expires: time.Now().Add(-time.Hour), TTL: AccessTokenTTL}
	expired.Save()

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

func TestLoadOrGenerateRefresh_NewToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	token, isNew, err := LoadOrGenerateRefresh()
	if err != nil {
		t.Fatalf("LoadOrGenerateRefresh() error: %v", err)
	}
	if !isNew {
		t.Error("should report as new when no file exists")
	}
	if token.TTL != RefreshTokenTTL {
		t.Errorf("TTL = %v, want %v", token.TTL, RefreshTokenTTL)
	}
}

func TestLoadOrGenerateRefresh_ExistingValid(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	first, _ := GenerateRefresh()
	path := filepath.Join(tmpDir, RefreshTokenFile)
	SaveToPath(first, path)

	loaded, isNew, err := LoadOrGenerateRefresh()
	if err != nil {
		t.Fatalf("LoadOrGenerateRefresh() error: %v", err)
	}
	if isNew {
		t.Error("should reuse existing valid refresh token")
	}
	if loaded.Value != first.Value {
		t.Error("should return the existing token value")
	}
}

func TestMigrateLegacyToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	// Write a legacy session.token.
	legacy, _ := GenerateRefresh()
	legacyPath := filepath.Join(tmpDir, LegacyTokenFile)
	SaveToPath(legacy, legacyPath)

	if err := MigrateLegacyToken(); err != nil {
		t.Fatalf("MigrateLegacyToken() error: %v", err)
	}

	// Verify refresh.token was created with the same value.
	refreshPath := filepath.Join(tmpDir, RefreshTokenFile)
	migrated, err := LoadFromPath(refreshPath)
	if err != nil {
		t.Fatalf("LoadFromPath(refresh) error: %v", err)
	}
	if migrated.Value != legacy.Value {
		t.Errorf("migrated value = %q, want %q", migrated.Value, legacy.Value)
	}
}

func TestMigrateLegacyToken_NoOpIfRefreshExists(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	// Write both files.
	refresh, _ := GenerateRefresh()
	refreshPath := filepath.Join(tmpDir, RefreshTokenFile)
	SaveToPath(refresh, refreshPath)

	legacy, _ := GenerateRefresh()
	legacyPath := filepath.Join(tmpDir, LegacyTokenFile)
	SaveToPath(legacy, legacyPath)

	if err := MigrateLegacyToken(); err != nil {
		t.Fatalf("MigrateLegacyToken() error: %v", err)
	}

	// Refresh token should be unchanged (not overwritten by legacy).
	loaded, _ := LoadFromPath(refreshPath)
	if loaded.Value != refresh.Value {
		t.Error("migration should not overwrite existing refresh token")
	}
}

func TestAccessTokenPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)
	t.Setenv("OP_FORWARD_TOKEN_FILE", "") // Clear override

	path, err := AccessTokenPath()
	if err != nil {
		t.Fatalf("AccessTokenPath() error: %v", err)
	}
	expected := filepath.Join(tmpDir, AccessTokenFile)
	if path != expected {
		t.Errorf("AccessTokenPath() = %q, want %q", path, expected)
	}
}

func TestRefreshTokenPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	path, err := RefreshTokenPath()
	if err != nil {
		t.Fatalf("RefreshTokenPath() error: %v", err)
	}
	expected := filepath.Join(tmpDir, RefreshTokenFile)
	if path != expected {
		t.Errorf("RefreshTokenPath() = %q, want %q", path, expected)
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
	path := filepath.Join(tmpDir, "test.token")

	token, _ := GenerateAccess()
	SaveToPath(token, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("token file permissions = %o, want 0600", perm)
	}
}
