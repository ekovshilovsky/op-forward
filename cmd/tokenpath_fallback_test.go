//go:build !windows

package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekovshilovsky/op-forward/internal/auth"
)

// When the user cache directory cannot be determined (no HOME and no
// XDG_CACHE_HOME), proxyTokenPath must still return a usable location under
// ~/.cache so the proxy has somewhere to persist refreshed tokens, rather
// than failing the whole invocation over a path lookup.
func TestProxyTokenPathFallsBackWithoutCacheDir(t *testing.T) {
	t.Setenv("OP_FORWARD_TOKEN_DIR", "")
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	got := proxyTokenPath(auth.AccessTokenFile)
	want := filepath.Join(".cache", auth.CacheDirName, auth.AccessTokenFile)
	if !strings.HasSuffix(got, want) {
		t.Errorf("proxyTokenPath(%q) = %q, want a path ending in %q", auth.AccessTokenFile, got, want)
	}
}
