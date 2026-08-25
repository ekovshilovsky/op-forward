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
	t.Setenv("OP_FORWARD_PROBE_TIMEOUT_MS", "soon")
	if got := getProbeTimeoutMs(); got != 500 {
		t.Fatalf("probe timeout with garbage = %d, want 500", got)
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

func TestResolveDialEndpoint(t *testing.T) {
	ep, err := resolveDialEndpoint("", "host.docker.internal", 18340)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "tcp://host.docker.internal:18340" {
		t.Fatalf("host/port form = %q", got)
	}

	ep, err = resolveDialEndpoint("unix:///run/op-forward.sock", "host.docker.internal", 18340)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "unix:///run/op-forward.sock" {
		t.Fatalf("--addr must win over host/port, got %q", got)
	}

	if _, err := resolveDialEndpoint("nonsense", "h", 1); err == nil {
		t.Fatal("malformed --addr accepted")
	}
}

func TestResolveListenEndpoint(t *testing.T) {
	ep, err := resolveListenEndpoint("", 20000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "tcp://127.0.0.1:20000" {
		t.Fatalf("port form = %q, want loopback tcp", got)
	}
	ep, err = resolveListenEndpoint("unix:///run/op-forward.sock", 20000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != "unix:///run/op-forward.sock" {
		t.Fatalf("--listen must win over --port, got %q", got)
	}
}
