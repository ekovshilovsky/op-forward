package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/executor"
)

func TestServeOverUnixEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix endpoints are rejected on Windows")
	}
	dir, err := os.MkdirTemp("", "opf")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ep := endpoint.Endpoint{Network: "unix", Address: filepath.Join(dir, "s.sock")}

	srv, _, _ := newTestServer()
	srv.endpoint = ep
	ln, err := ep.Listen()
	if err != nil {
		t.Fatalf("Listen() error: %v", err)
	}
	// Serve returns once the listener is closed during teardown; that error
	// is the expected shutdown signal rather than a test failure.
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = ln.Close() }()

	resp, err := ep.HTTPClient(2 * time.Second).Get(ep.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health over unix socket: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestExecute_RejectsForeignPeerUID(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req = req.WithContext(withPeerUID(req.Context(), os.Getuid()+1))
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for foreign peer uid", w.Code)
	}
}

func TestTokenRefresh_RejectsForeignPeerUID(t *testing.T) {
	srv, _, refreshToken := newTestServer()
	req := httptest.NewRequest("POST", "/token/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req = req.WithContext(withPeerUID(req.Context(), os.Getuid()+1))
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for foreign peer uid", w.Code)
	}
}

func TestExecute_AllowsOwnPeerUID(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req = req.WithContext(withPeerUID(req.Context(), os.Getuid()))
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("own uid was rejected by the peer credential check")
	}
}

func TestHealth_RejectsForeignPeerUID(t *testing.T) {
	srv, _, _ := newTestServer()
	req := httptest.NewRequest("GET", "/health", nil)
	req = req.WithContext(withPeerUID(req.Context(), os.Getuid()+1))
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: the peer check must cover every route, not selected handlers", w.Code)
	}
}
