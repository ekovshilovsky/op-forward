package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearTokenLocationEnv removes every input that TokenDir and the path helpers
// consult, leaving no way to resolve a per-user cache directory. An empty value
// is treated as unset by both TokenDir and os.UserCacheDir. Tests using this
// helper mutate process environment and therefore must not run in parallel.
func clearTokenLocationEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "OP_FORWARD_TOKEN_DIR", "OP_FORWARD_TOKEN_FILE"} {
		t.Setenv(name, "")
	}
}

// blockStagingPath turns the temporary staging path used by SaveToPath into a
// directory, which makes the staged write fail deterministically without
// depending on file permissions (and therefore without needing to skip when
// running as root).
func blockStagingPath(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatalf("creating blocking staging directory: %v", err)
	}
}

// requireNoFile fails the test if anything exists at path.
func requireNoFile(t *testing.T, path string) {
	t.Helper()
	// The path must not be reachable as a file. Depending on the failure
	// injected, Lstat reports ENOENT (nothing there) or ENOTDIR (a parent
	// component is a regular file); both mean no token was written there.
	if _, err := os.Lstat(path); err == nil {
		t.Fatalf("expected %s to be absent, but it exists", path)
	}
}

// requireFileBytes fails the test unless the file at path holds exactly want.
func requireFileBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func requireErrorContaining(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", fragments)
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), fragment)
		}
	}
}

// ---------- Cache directory unavailable ----------

func TestTokenDir_CacheDirectoryUnavailable(t *testing.T) {
	clearTokenLocationEnv(t)

	dir, err := TokenDir()
	requireErrorContaining(t, err, "determining cache directory")
	if dir != "" {
		t.Errorf("TokenDir() dir = %q on error, want empty", dir)
	}
}

func TestCacheDirectoryErrorPropagates(t *testing.T) {
	const want = "determining cache directory"

	cases := []struct {
		name string
		call func() error
	}{
		{"tokenFilePath", func() error { _, err := tokenFilePath("any.token"); return err }},
		{"AccessTokenPath", func() error { _, err := AccessTokenPath(); return err }},
		{"RefreshTokenPath", func() error { _, err := RefreshTokenPath(); return err }},
		{"LegacyTokenPath", func() error { _, err := LegacyTokenPath(); return err }},
		{"TokenPath", func() error { _, err := TokenPath(); return err }},
		{"Save", func() error {
			tok := &Token{Value: "v", Expires: time.Now().Add(time.Hour)}
			return tok.Save()
		}},
		{"Load", func() error {
			tok, err := Load()
			if tok != nil {
				t.Errorf("Load() returned a token alongside the error: %+v", tok)
			}
			return err
		}},
		{"LoadOrGenerate", func() error {
			tok, generated, err := LoadOrGenerate()
			if tok != nil || generated {
				t.Errorf("LoadOrGenerate() = (%v, %v), want (nil, false) on error", tok, generated)
			}
			return err
		}},
		{"LoadOrGenerateRefresh", func() error {
			tok, generated, err := LoadOrGenerateRefresh()
			if tok != nil || generated {
				t.Errorf("LoadOrGenerateRefresh() = (%v, %v), want (nil, false) on error", tok, generated)
			}
			return err
		}},
		{"MigrateLegacyToken", MigrateLegacyToken},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearTokenLocationEnv(t)
			requireErrorContaining(t, tc.call(), want)
		})
	}
}

// ---------- SaveToPath failure modes ----------

func TestSaveToPath_ParentIsRegularFile(t *testing.T) {
	tmpDir := t.TempDir()
	parent := filepath.Join(tmpDir, "parent")
	if err := os.WriteFile(parent, []byte("unrelated-file-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "nested", "access.token")

	tok := &Token{Value: "secret-value", Expires: time.Now().Add(time.Hour)}
	requireErrorContaining(t, SaveToPath(tok, target), "creating token directory")

	requireFileBytes(t, parent, "unrelated-file-content")
	requireNoFile(t, target)
	requireNoFile(t, target+".tmp")
}

func TestSaveToPath_StagingPathIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "access.token")
	if err := os.WriteFile(target, []byte("previous-token\n2030-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockStagingPath(t, target)

	tok := &Token{Value: "secret-value", Expires: time.Now().Add(time.Hour)}
	requireErrorContaining(t, SaveToPath(tok, target), "writing token")

	// The pre-existing destination must be untouched.
	requireFileBytes(t, target, "previous-token\n2030-01-01T00:00:00Z\n")

	// The staging path is still the directory the fixture created, and the
	// new secret was not written anywhere beneath it.
	info, err := os.Stat(target + ".tmp")
	if err != nil || !info.IsDir() {
		t.Fatalf("staging path should remain the fixture directory, got info=%v err=%v", info, err)
	}
	entries, err := os.ReadDir(target + ".tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging directory should be empty, found %d entries", len(entries))
	}
}

func TestSaveToPath_RenameTargetIsNonEmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "access.token")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	occupant := filepath.Join(target, "occupant")
	if err := os.WriteFile(occupant, []byte("pre-existing-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	tok := &Token{Value: "secret-value", Expires: time.Now().Add(time.Hour)}
	requireErrorContaining(t, SaveToPath(tok, target), "renaming token file")

	// The staged file holds the secret, so it must not survive the failure.
	requireNoFile(t, target+".tmp")

	// The destination directory and its contents are unchanged.
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		t.Fatalf("destination should still be a directory, got info=%v err=%v", info, err)
	}
	requireFileBytes(t, occupant, "pre-existing-content")
}

// ---------- MigrateLegacyToken ----------

func TestMigrateLegacyToken_ExpiredLegacyIsIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	legacyPath := filepath.Join(tmpDir, LegacyTokenFile)
	expired := &Token{Value: "expired-legacy-value", Expires: time.Now().Add(-time.Hour)}
	if err := SaveToPath(expired, legacyPath); err != nil {
		t.Fatalf("saving fixture legacy token: %v", err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyToken(); err != nil {
		t.Fatalf("MigrateLegacyToken() error: %v", err)
	}

	requireNoFile(t, filepath.Join(tmpDir, RefreshTokenFile))
	requireNoFile(t, filepath.Join(tmpDir, RefreshTokenFile+".tmp"))
	requireFileBytes(t, legacyPath, string(before))
}

func TestMigrateLegacyToken_MissingLegacyIsNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	if err := MigrateLegacyToken(); err != nil {
		t.Fatalf("MigrateLegacyToken() error: %v", err)
	}

	requireNoFile(t, filepath.Join(tmpDir, RefreshTokenFile))
	requireNoFile(t, filepath.Join(tmpDir, LegacyTokenFile))
}

func TestMigrateLegacyToken_UnwritableRefreshPathReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)

	legacyPath := filepath.Join(tmpDir, LegacyTokenFile)
	legacy, err := GenerateRefresh()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveToPath(legacy, legacyPath); err != nil {
		t.Fatalf("saving fixture legacy token: %v", err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	refreshPath := filepath.Join(tmpDir, RefreshTokenFile)
	blockStagingPath(t, refreshPath)

	requireErrorContaining(t, MigrateLegacyToken(), "writing token")

	requireNoFile(t, refreshPath)
	requireFileBytes(t, legacyPath, string(before))
}

// ---------- LoadOrGenerate save failures ----------

func TestLoadOrGenerateRefresh_SaveFailureReturnsError(t *testing.T) {
	t.Run("no existing token", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)
		refreshPath := filepath.Join(tmpDir, RefreshTokenFile)
		blockStagingPath(t, refreshPath)

		tok, generated, err := LoadOrGenerateRefresh()
		requireErrorContaining(t, err, "writing token")
		if tok != nil || generated {
			t.Errorf("LoadOrGenerateRefresh() = (%v, %v), want (nil, false) on save failure", tok, generated)
		}
		requireNoFile(t, refreshPath)
	})

	t.Run("expired token is not replaced", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)
		refreshPath := filepath.Join(tmpDir, RefreshTokenFile)
		expired := &Token{Value: "expired-refresh-value", Expires: time.Now().Add(-time.Hour)}
		if err := SaveToPath(expired, refreshPath); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(refreshPath)
		if err != nil {
			t.Fatal(err)
		}
		blockStagingPath(t, refreshPath)

		tok, generated, err := LoadOrGenerateRefresh()
		requireErrorContaining(t, err, "writing token")
		if tok != nil || generated {
			t.Errorf("LoadOrGenerateRefresh() = (%v, %v), want (nil, false) on save failure", tok, generated)
		}
		requireFileBytes(t, refreshPath, string(before))
	})

	t.Run("token directory beneath a regular file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("not-a-directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OP_FORWARD_TOKEN_DIR", filepath.Join(blocker, "tokens"))

		tok, generated, err := LoadOrGenerateRefresh()
		requireErrorContaining(t, err, "creating token directory")
		if tok != nil || generated {
			t.Errorf("LoadOrGenerateRefresh() = (%v, %v), want (nil, false) on save failure", tok, generated)
		}
		requireFileBytes(t, blocker, "not-a-directory")
	})
}

func TestLoadOrGenerate_SaveFailureReturnsError(t *testing.T) {
	t.Run("no existing token", func(t *testing.T) {
		tmpDir := t.TempDir()
		accessPath := filepath.Join(tmpDir, "access.token")
		t.Setenv("OP_FORWARD_TOKEN_FILE", accessPath)
		blockStagingPath(t, accessPath)

		tok, generated, err := LoadOrGenerate()
		requireErrorContaining(t, err, "writing token")
		if tok != nil || generated {
			t.Errorf("LoadOrGenerate() = (%v, %v), want (nil, false) on save failure", tok, generated)
		}
		requireNoFile(t, accessPath)
	})

	t.Run("expired token is not replaced", func(t *testing.T) {
		tmpDir := t.TempDir()
		accessPath := filepath.Join(tmpDir, "access.token")
		t.Setenv("OP_FORWARD_TOKEN_FILE", accessPath)
		expired := &Token{Value: "expired-access-value", Expires: time.Now().Add(-time.Hour)}
		if err := SaveToPath(expired, accessPath); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(accessPath)
		if err != nil {
			t.Fatal(err)
		}
		blockStagingPath(t, accessPath)

		tok, generated, err := LoadOrGenerate()
		requireErrorContaining(t, err, "writing token")
		if tok != nil || generated {
			t.Errorf("LoadOrGenerate() = (%v, %v), want (nil, false) on save failure", tok, generated)
		}
		requireFileBytes(t, accessPath, string(before))
	})

	t.Run("token file beneath a regular file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("not-a-directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(blocker, "access.token"))

		tok, generated, err := LoadOrGenerate()
		requireErrorContaining(t, err, "creating token directory")
		if tok != nil || generated {
			t.Errorf("LoadOrGenerate() = (%v, %v), want (nil, false) on save failure", tok, generated)
		}
		requireFileBytes(t, blocker, "not-a-directory")
	})
}
