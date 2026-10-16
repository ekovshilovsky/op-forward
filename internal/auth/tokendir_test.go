package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// Without the OP_FORWARD_TOKEN_DIR override, TokenDir must place tokens in
// the op-forward subdirectory of the user cache directory the platform
// reports.
func TestTokenDirDefaultsToUserCacheDir(t *testing.T) {
	t.Setenv("OP_FORWARD_TOKEN_DIR", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir() error: %v", err)
	}

	dir, err := TokenDir()
	if err != nil {
		t.Fatalf("TokenDir() error: %v", err)
	}
	if want := filepath.Join(cacheDir, CacheDirName); dir != want {
		t.Errorf("TokenDir() = %q, want %q", dir, want)
	}
}
