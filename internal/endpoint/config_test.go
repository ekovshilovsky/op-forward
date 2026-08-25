package endpoint

import "testing"

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OP_FORWARD_ADDR", "OP_FORWARD_LISTEN", "OP_FORWARD_HOST", "OP_FORWARD_PORT"} {
		t.Setenv(k, "")
	}
}

func TestDialFromEnvDefaultsToLoopbackTCP(t *testing.T) {
	clearEnv(t)
	ep, err := DialFromEnv()
	if err != nil {
		t.Fatalf("DialFromEnv() error: %v", err)
	}
	if got := ep.String(); got != "tcp://127.0.0.1:18340" {
		t.Fatalf("DialFromEnv() = %q", got)
	}
}

func TestDialFromEnvHonorsHostAndPortAliases(t *testing.T) {
	clearEnv(t)
	t.Setenv("OP_FORWARD_HOST", "host.docker.internal")
	t.Setenv("OP_FORWARD_PORT", "20000")
	ep, err := DialFromEnv()
	if err != nil {
		t.Fatalf("DialFromEnv() error: %v", err)
	}
	if got := ep.String(); got != "tcp://host.docker.internal:20000" {
		t.Fatalf("DialFromEnv() = %q", got)
	}
}

func TestDialFromEnvAddrOverridesAliases(t *testing.T) {
	clearEnv(t)
	t.Setenv("OP_FORWARD_HOST", "host.docker.internal")
	t.Setenv("OP_FORWARD_ADDR", "unix:///run/op-forward.sock")
	ep, err := DialFromEnv()
	if err != nil {
		t.Fatalf("DialFromEnv() error: %v", err)
	}
	if got := ep.String(); got != "unix:///run/op-forward.sock" {
		t.Fatalf("DialFromEnv() = %q", got)
	}
}

func TestDialFromEnvRejectsMalformedAddr(t *testing.T) {
	clearEnv(t)
	t.Setenv("OP_FORWARD_ADDR", "127.0.0.1:18340")
	if _, err := DialFromEnv(); err == nil {
		t.Fatal("DialFromEnv() accepted an address without a scheme")
	}
}

func TestListenFromEnvDefaultsToLoopbackTCP(t *testing.T) {
	clearEnv(t)
	ep, err := ListenFromEnv()
	if err != nil {
		t.Fatalf("ListenFromEnv() error: %v", err)
	}
	if got := ep.String(); got != "tcp://127.0.0.1:18340" {
		t.Fatalf("ListenFromEnv() = %q", got)
	}
}

func TestListenFromEnvIgnoresHostAlias(t *testing.T) {
	// OP_FORWARD_HOST is a client-side dial target. The daemon must keep
	// binding loopback even when the same environment is shared by both.
	clearEnv(t)
	t.Setenv("OP_FORWARD_HOST", "host.docker.internal")
	t.Setenv("OP_FORWARD_PORT", "20000")
	ep, err := ListenFromEnv()
	if err != nil {
		t.Fatalf("ListenFromEnv() error: %v", err)
	}
	if got := ep.String(); got != "tcp://127.0.0.1:20000" {
		t.Fatalf("ListenFromEnv() = %q", got)
	}
}

func TestListenFromEnvHonorsListen(t *testing.T) {
	clearEnv(t)
	t.Setenv("OP_FORWARD_LISTEN", "unix:///run/op-forward.sock")
	ep, err := ListenFromEnv()
	if err != nil {
		t.Fatalf("ListenFromEnv() error: %v", err)
	}
	if got := ep.String(); got != "unix:///run/op-forward.sock" {
		t.Fatalf("ListenFromEnv() = %q", got)
	}
}

func TestPortFromEnvFallsBackOnGarbage(t *testing.T) {
	clearEnv(t)
	t.Setenv("OP_FORWARD_PORT", "not-a-port")
	if got := PortFromEnv(); got != DefaultPort {
		t.Fatalf("PortFromEnv() = %d, want %d", got, DefaultPort)
	}
}
