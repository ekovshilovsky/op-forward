package endpoint

import (
	"errors"
	"strings"
	"testing"
)

// TestParseUnixRejectedOnWindows exercises the Windows parsing rules from any
// host by passing the target platform explicitly.
func TestParseUnixRejectedOnWindows(t *testing.T) {
	for _, raw := range []string{
		"unix:///tmp/op-forward.sock",
		"unix://C:/Users/me/op-forward.sock",
		"unix://relative.sock",
		"unix://",
	} {
		_, err := parseFor(raw, "windows")
		if err == nil {
			t.Errorf("parseFor(%q, windows) succeeded, want rejection", raw)
			continue
		}
		if !errors.Is(err, errUnixUnsupported) {
			t.Errorf("parseFor(%q, windows) error = %v, want errUnixUnsupported", raw, err)
		}
		for _, fragment := range []string{"not supported on Windows", "tcp://127.0.0.1:PORT", raw} {
			if !strings.Contains(err.Error(), fragment) {
				t.Errorf("parseFor(%q, windows) error = %q, want it to contain %q", raw, err, fragment)
			}
		}
	}
}

func TestParseTCPAcceptedOnWindows(t *testing.T) {
	ep, err := parseFor("tcp://127.0.0.1:18340", "windows")
	if err != nil {
		t.Fatalf("parseFor(tcp, windows) error: %v", err)
	}
	if ep.Network != "tcp" || ep.Address != "127.0.0.1:18340" {
		t.Fatalf("parseFor(tcp, windows) = %+v", ep)
	}
}

// TestParseUnixStillAcceptedOffWindows pins that the Windows rule is the only
// platform gate: other targets keep the original unix behavior.
func TestParseUnixStillAcceptedOffWindows(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		ep, err := parseFor("unix:///tmp/op-forward.sock", goos)
		if err != nil || ep.Network != "unix" {
			t.Errorf("parseFor(unix, %s) = %+v, %v", goos, ep, err)
		}
	}
}
