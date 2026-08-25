package endpoint

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortSocketPath returns a socket path inside a fresh temp directory whose
// name stays well under the ~104 byte sun_path limit; t.TempDir() embeds the
// test name and can exceed it on macOS.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "opf")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func serveOK(t *testing.T, ln net.Listener) {
	t.Helper()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func TestListenRefusesNonLoopbackTCP(t *testing.T) {
	_, err := Endpoint{Network: "tcp", Address: "0.0.0.0:0"}.Listen()
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Listen() error = %v, want loopback refusal", err)
	}
}

func TestListenTCPLoopback(t *testing.T) {
	ln, err := Endpoint{Network: "tcp", Address: "127.0.0.1:0"}.Listen()
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer ln.Close()
	serveOK(t, ln)

	ep, _ := Parse("tcp://" + ln.Addr().String())
	if err := ep.Probe(time.Second); err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	resp, err := ep.HTTPClient(time.Second).Get(ep.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestListenUnixCreatesPrivateSocket(t *testing.T) {
	sock := filepath.Join(filepath.Dir(shortSocketPath(t)), "nested", "s.sock")
	ep := Endpoint{Network: "unix", Address: sock}

	ln, err := ep.Listen()
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	defer ln.Close()
	serveOK(t, ln)

	dirInfo, err := os.Stat(filepath.Dir(sock))
	if err != nil {
		t.Fatalf("socket dir missing: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("socket dir mode = %o, want 0700", mode)
	}
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket missing: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode = %o, want 0600", mode)
	}

	if err := ep.Probe(time.Second); err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	resp, err := ep.HTTPClient(time.Second).Get(ep.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("Get() over unix socket error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func TestListenUnixReplacesStaleSocket(t *testing.T) {
	sock := shortSocketPath(t)
	ep := Endpoint{Network: "unix", Address: sock}

	// Simulate a daemon that died without unlinking its socket.
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("creating stale socket: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("stale socket should remain on disk: %v", err)
	}

	ln, err := ep.Listen()
	if err != nil {
		t.Fatalf("Listen() over stale socket error: %v", err)
	}
	ln.Close()
}

func TestListenUnixRefusesLiveSocket(t *testing.T) {
	sock := shortSocketPath(t)
	ep := Endpoint{Network: "unix", Address: sock}

	live, err := ep.Listen()
	if err != nil {
		t.Fatalf("first Listen() error: %v", err)
	}
	defer live.Close()

	if _, err := ep.Listen(); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second Listen() error = %v, want 'in use'", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("live socket must not be removed: %v", err)
	}
}

func TestProbeFailsWhenNothingListens(t *testing.T) {
	sock := shortSocketPath(t)
	if err := (Endpoint{Network: "unix", Address: sock}).Probe(100 * time.Millisecond); err == nil {
		t.Fatal("Probe() succeeded on absent socket")
	}
}
