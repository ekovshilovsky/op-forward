package cmd

import (
	"path/filepath"
	"testing"

	"github.com/ekovshilovsky/op-forward/internal/auth"
)

func TestGetProbeTimeoutMs(t *testing.T) {
	t.Setenv("OP_FORWARD_PROBE_TIMEOUT_MS", "")
	if got := getProbeTimeoutMs(); got != 500 {
		t.Fatalf("default probe timeout = %d, want 500", got)
	}
	t.Setenv("OP_FORWARD_PROBE_TIMEOUT_MS", "1200")
	if got := getProbeTimeoutMs(); got != 1200 {
		t.Fatalf("probe timeout = %d, want 1200", got)
	}
	for _, raw := range []string{"soon", "0", "-5"} {
		t.Setenv("OP_FORWARD_PROBE_TIMEOUT_MS", raw)
		if got := getProbeTimeoutMs(); got != 500 {
			t.Fatalf("probe timeout with %q = %d, want 500 (a zero timeout would disable the shim fallback)", raw, got)
		}
	}
}

// Explicitly typed --host/--port must beat an OP_FORWARD_ADDR inherited from
// the environment, while an explicitly typed --addr beats everything.
func TestEndpointArgPrecedence(t *testing.T) {
	envOnly := map[string]bool{}
	if got := endpointArg("unix:///env.sock", envOnly, "host", "port"); got != "unix:///env.sock" {
		t.Fatalf("env addr with no explicit flags = %q, want env addr", got)
	}
	hostTyped := map[string]bool{"host": true}
	if got := endpointArg("unix:///env.sock", hostTyped, "host", "port"); got != "" {
		t.Fatalf("explicit --host should discard env addr, got %q", got)
	}
	bothTyped := map[string]bool{"addr": true, "host": true}
	if got := endpointArg("unix:///typed.sock", bothTyped, "host", "port"); got != "unix:///typed.sock" {
		t.Fatalf("explicit --addr must win, got %q", got)
	}
}

func TestProxyTokenPathMatchesAuthPackage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv("OP_FORWARD_TOKEN_DIR", dir)

	want := map[string]func() (string, error){
		auth.AccessTokenFile:  auth.AccessTokenPath,
		auth.RefreshTokenFile: auth.RefreshTokenPath,
		auth.LegacyTokenFile:  auth.LegacyTokenPath,
	}
	for name, fn := range want {
		expected, err := fn()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := proxyTokenPath(name); got != expected {
			t.Errorf("proxyTokenPath(%q) = %q, want %q", name, got, expected)
		}
	}
}

func TestProxyTokenPathFileOverrideOnlyAppliesToAccess(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "custom-access.token")
	t.Setenv("OP_FORWARD_TOKEN_FILE", override)
	t.Setenv("OP_FORWARD_TOKEN_DIR", dir)

	if got := proxyTokenPath(auth.AccessTokenFile); got != override {
		t.Fatalf("access path = %q, want %q", got, override)
	}
	if got := proxyTokenPath(auth.RefreshTokenFile); got != filepath.Join(dir, auth.RefreshTokenFile) {
		t.Fatalf("refresh path = %q, must not follow OP_FORWARD_TOKEN_FILE", got)
	}
}
