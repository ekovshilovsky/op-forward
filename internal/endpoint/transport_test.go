package endpoint

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func serveOK(t *testing.T, ln net.Listener) {
	t.Helper()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	// Serve returns once the server is closed during cleanup; that error is
	// the expected shutdown signal rather than a test failure.
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
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
	defer func() { _ = ln.Close() }()
	serveOK(t, ln)

	ep, _ := Parse("tcp://" + ln.Addr().String())
	if err := ep.Probe(time.Second); err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	resp, err := ep.HTTPClient(time.Second).Get(ep.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
