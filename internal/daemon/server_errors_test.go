package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/executor"
)

// captureLog redirects the standard logger into a buffer for the duration of
// the test. The logger is process-wide, so tests using this helper must not
// run in parallel.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

// ---------- Start ----------

func TestStart_RefusesNonLoopbackTCP(t *testing.T) {
	access := &auth.Token{Value: "a", Expires: time.Now().Add(time.Hour), TTL: auth.AccessTokenTTL}
	refresh := &auth.Token{Value: "r", Expires: time.Now().Add(time.Hour), TTL: auth.RefreshTokenTTL}
	srv := New(access, refresh, endpoint.Endpoint{Network: "tcp", Address: "0.0.0.0:0"}, "0.3.0")

	err := srv.Start()
	if err == nil || !strings.Contains(err.Error(), "refusing to bind to non-loopback address") {
		t.Fatalf("Start() error = %v, want the non-loopback refusal", err)
	}
}

// Start owns its listener and offers no way to close it, so the serving
// goroutine outlives the test, exactly as in TestServeOverUnixEndpoint. The
// socket lives in a private temp directory that is removed on teardown.
func TestStart_ServesOverUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix endpoints are rejected on Windows")
	}
	dir, err := os.MkdirTemp("", "opf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ep := endpoint.Endpoint{Network: "unix", Address: filepath.Join(dir, "s.sock")}

	access := &auth.Token{Value: "a", Expires: time.Now().Add(time.Hour), TTL: auth.AccessTokenTTL}
	refresh := &auth.Token{Value: "r", Expires: time.Now().Add(time.Hour), TTL: auth.RefreshTokenTTL}
	srv := New(access, refresh, ep, "0.3.0")
	if srv.accessToken != access || srv.refreshToken != refresh || srv.endpoint != ep || srv.version != "0.3.0" {
		t.Fatalf("New() did not retain its arguments: %+v", srv)
	}

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start() }()

	deadline := time.Now().Add(3 * time.Second)
	for ep.Probe(100*time.Millisecond) != nil {
		select {
		case err := <-startErr:
			t.Fatalf("Start() returned before serving: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not begin accepting connections on the unix socket")
		}
		time.Sleep(20 * time.Millisecond)
	}

	client := ep.HTTPClient(2 * time.Second)

	resp, err := client.Get(ep.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health over unix socket: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
	var health map[string]string
	if err := json.Unmarshal(body, &health); err != nil || health["status"] != "ok" {
		t.Fatalf("health body = %q (err %v), want status ok", body, err)
	}

	// The connection carries this process's own uid as peer credentials, so
	// the peer check must pass the request through to the bearer token check
	// (401 for a missing token) rather than refusing it outright (403).
	resp, err = client.Post(ep.BaseURL()+"/token/refresh", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /token/refresh over unix socket: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated refresh status = %d, want 401 (own uid must not be refused as foreign)", resp.StatusCode)
	}
}

// Over TCP no peer credentials are available, so ConnContext leaves the
// context untouched and the request proceeds to the handlers.
func TestServe_TCPConnectionsCarryNoPeerCredentials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, _, _ := newTestServer()
	// Serve returns once the listener is closed during teardown; that error
	// is the expected shutdown signal rather than a test failure.
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	ep, err := endpoint.Parse("tcp://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ep.HTTPClient(2 * time.Second).Get(ep.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health over TCP: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

// ---------- handleExecute executor failure ----------

func TestExecute_ExecutorFailureReturns500(t *testing.T) {
	logs := captureLog(t)
	t.Setenv("PATH", t.TempDir()) // No op binary is resolvable.

	srv, accessToken, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the executor cannot run op", w.Code)
	}
	if !strings.Contains(w.Body.String(), "execution error") {
		t.Errorf("body = %q, want it to contain %q", w.Body.String(), "execution error")
	}
	if !strings.Contains(w.Body.String(), "op CLI not found in PATH") {
		t.Errorf("body = %q, want the executor's cause to be included", w.Body.String())
	}
	if !strings.Contains(logs.String(), "[op-forward] error:") {
		t.Errorf("log = %q, want the executor failure to be logged", logs.String())
	}
}

// ---------- handleTokenRefresh persistence warnings ----------

// Today a failed persistence step is logged as a warning and the refresh still
// succeeds: the rotated pair is returned and swapped into memory even though
// nothing reached disk. Whether that ordering is correct is questioned in
// issue #18; this test records the current behavior.
func TestTokenRefresh_PersistenceFailureWarnsButRotates(t *testing.T) {
	logs := captureLog(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every token path resolves beneath a regular file, so each save fails
	// while creating the token directory.
	t.Setenv("OP_FORWARD_TOKEN_DIR", filepath.Join(blocker, "tokens"))
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")

	srv, _, oldRefresh := newTestServer()
	oldAccess := srv.accessToken.Value

	req := httptest.NewRequest("POST", "/token/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+oldRefresh)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite persistence failure; body %q", w.Code, w.Body.String())
	}
	var resp TokenRefreshResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.AccessToken == "" || resp.AccessToken == oldAccess {
		t.Errorf("access token was not rotated: %q", resp.AccessToken)
	}
	if resp.RefreshToken == "" || resp.RefreshToken == oldRefresh {
		t.Errorf("refresh token was not rotated: %q", resp.RefreshToken)
	}

	logged := logs.String()
	for _, warning := range []string{
		"warning: failed to save access token",
		"warning: failed to save refresh token",
		"warning: failed to save legacy session token",
	} {
		if !strings.Contains(logged, warning) {
			t.Errorf("log is missing %q; log = %q", warning, logged)
		}
	}
	if strings.Contains(logged, resp.AccessToken) || strings.Contains(logged, resp.RefreshToken) {
		t.Error("log output must not contain token values")
	}

	// The in-memory state carries the rotated pair: the old refresh token is
	// spent and the new access token authenticates.
	if srv.refreshToken.Value != resp.RefreshToken || srv.accessToken.Value != resp.AccessToken {
		t.Error("in-memory tokens were not swapped to the rotated pair")
	}
	replay := httptest.NewRequest("POST", "/token/refresh", nil)
	replay.Header.Set("Authorization", "Bearer "+oldRefresh)
	replayW := httptest.NewRecorder()
	srv.handler().ServeHTTP(replayW, replay)
	if replayW.Code != http.StatusUnauthorized {
		t.Errorf("replayed old refresh token status = %d, want 401", replayW.Code)
	}

	data, err := os.ReadFile(blocker)
	if err != nil || string(data) != "not-a-directory" {
		t.Errorf("blocking file was altered: err=%v content=%q", err, data)
	}
}
