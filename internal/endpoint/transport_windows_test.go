//go:build windows

package endpoint

import (
	"errors"
	"testing"
)

// These tests run only on a Windows host, where the platform guards in
// Parse and listenUnix are the real code path rather than a simulated one.

func TestForListenRejectsUnixOnWindows(t *testing.T) {
	if _, err := ForListen("unix:///tmp/op-forward.sock", 18340); !errors.Is(err, errUnixUnsupported) {
		t.Fatalf("ForListen(unix) error = %v, want errUnixUnsupported", err)
	}
}

func TestForDialRejectsUnixOnWindows(t *testing.T) {
	if _, err := ForDial("unix:///tmp/op-forward.sock", "127.0.0.1", 18340); !errors.Is(err, errUnixUnsupported) {
		t.Fatalf("ForDial(unix) error = %v, want errUnixUnsupported", err)
	}
}

func TestListenUnixRefusedOnWindows(t *testing.T) {
	ln, err := Endpoint{Network: "unix", Address: `C:\Temp\op-forward.sock`}.Listen()
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen() succeeded for a unix endpoint on Windows")
	}
	if !errors.Is(err, errUnixUnsupported) {
		t.Fatalf("Listen() error = %v, want errUnixUnsupported", err)
	}
}
