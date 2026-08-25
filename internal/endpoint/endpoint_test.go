package endpoint

import (
	"testing"
)

func TestParseTCP(t *testing.T) {
	ep, err := Parse("tcp://127.0.0.1:18340")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if ep.Network != "tcp" || ep.Address != "127.0.0.1:18340" {
		t.Fatalf("Parse() = %+v, want tcp 127.0.0.1:18340", ep)
	}
}

func TestParseTCPIPv6(t *testing.T) {
	ep, err := Parse("tcp://[::1]:18340")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if ep.Address != "[::1]:18340" {
		t.Fatalf("Address = %q, want [::1]:18340", ep.Address)
	}
}

func TestParseUnix(t *testing.T) {
	ep, err := Parse("unix:///tmp/op-forward.sock")
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if ep.Network != "unix" || ep.Address != "/tmp/op-forward.sock" {
		t.Fatalf("Parse() = %+v, want unix /tmp/op-forward.sock", ep)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"no scheme":         "127.0.0.1:18340",
		"unknown scheme":    "http://127.0.0.1:18340",
		"tcp without port":  "tcp://127.0.0.1",
		"tcp non-numeric":   "tcp://127.0.0.1:abc",
		"unix relative":     "unix://relative/path.sock",
		"unix empty path":   "unix://",
		"empty string":      "",
		"tcp trailing path": "tcp://127.0.0.1:18340/op",
	}
	for name, raw := range cases {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s: Parse(%q) succeeded, want error", name, raw)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, raw := range []string{"tcp://127.0.0.1:18340", "tcp://[::1]:1", "unix:///run/user/501/op-forward.sock"} {
		ep, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", raw, err)
		}
		if got := ep.String(); got != raw {
			t.Errorf("String() = %q, want %q", got, raw)
		}
	}
}

func TestTCPConstructor(t *testing.T) {
	if got := TCP("host.docker.internal", 18340).String(); got != "tcp://host.docker.internal:18340" {
		t.Fatalf("TCP() = %q", got)
	}
	if got := TCP("::1", 1).Address; got != "[::1]:1" {
		t.Fatalf("TCP() IPv6 Address = %q, want [::1]:1", got)
	}
}

func TestBaseURL(t *testing.T) {
	tcp, _ := Parse("tcp://host.docker.internal:18340")
	if got := tcp.BaseURL(); got != "http://host.docker.internal:18340" {
		t.Errorf("tcp BaseURL() = %q", got)
	}
	unix, _ := Parse("unix:///tmp/x.sock")
	if got := unix.BaseURL(); got != "http://unix" {
		t.Errorf("unix BaseURL() = %q", got)
	}
}

func TestIsLoopback(t *testing.T) {
	loop := []string{"tcp://127.0.0.1:1", "tcp://[::1]:1", "tcp://127.1.2.3:1", "unix:///tmp/x.sock"}
	for _, raw := range loop {
		ep, _ := Parse(raw)
		if !ep.IsLoopback() {
			t.Errorf("IsLoopback(%q) = false, want true", raw)
		}
	}
	// "localhost" is a name, not an address; a resolver can map it anywhere,
	// so only IP literals count as loopback for binding purposes.
	notLoop := []string{"tcp://0.0.0.0:1", "tcp://[::]:1", "tcp://host.docker.internal:1", "tcp://10.0.0.1:1", "tcp://localhost:1"}
	for _, raw := range notLoop {
		ep, _ := Parse(raw)
		if ep.IsLoopback() {
			t.Errorf("IsLoopback(%q) = true, want false", raw)
		}
	}
}
