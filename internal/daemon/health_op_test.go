//go:build !windows

package daemon

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/opcheck"
)

// healthOpFakeScript is a desktop-app-integration op: whoami fails with "not
// signed in" while the authenticated read succeeds (the incident the
// endpoint exists to get right). FAKE_OP_VAULT_EXIT makes the read fail, and
// its stderr deliberately echoes FAKE_OP_SECRET so tests can prove that raw
// op output never reaches the response.
const healthOpFakeScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_OP_LOG"
case "$1" in
  --version) echo "2.41.0"; exit 0 ;;
  whoami) echo "[ERROR] account is not signed in" >&2; exit 1 ;;
  account) echo '[{"url":"my.1password.com","email":"someone@example.com"}]'; exit 0 ;;
  vault)
    if [ "${FAKE_OP_VAULT_SLEEP:-0}" != "0" ]; then
      exec sleep "$FAKE_OP_VAULT_SLEEP"
    fi
    if [ "${FAKE_OP_VAULT_EXIT:-0}" != "0" ]; then
      echo "[ERROR] account is not signed in ($FAKE_OP_SECRET)" >&2
      exit 1
    fi
    echo '[{"id":"abc","name":"SECRET-VAULT-NAME"}]'
    exit 0
    ;;
esac
exit 0
`

// installHealthOpFake puts the fake op on PATH and returns its invocation log.
func installHealthOpFake(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	// #nosec G306 -- the fake op is a test fixture that must be executable
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte(healthOpFakeScript), 0o755); err != nil {
		t.Fatalf("writing fake op: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_OP_LOG", logPath)
	return logPath
}

func getHealthOp(srv *Server, target, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	return w
}

func decodeObject(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, w.Body.String())
	}
	return body
}

// /health/op describes host state and can start a process, so it requires the
// same bearer access token as /op/execute. The refresh token and an expired
// access token must not open it either.
func TestHealthOp_RequiresAccessToken(t *testing.T) {
	installHealthOpFake(t)
	srv, _, refreshToken := newTestServer()
	expired := &Server{
		accessToken:  &auth.Token{Value: "expired-token", Expires: time.Now().Add(-time.Hour), TTL: auth.AccessTokenTTL},
		refreshToken: srv.refreshToken,
		endpoint:     endpoint.TCP("127.0.0.1", 18340),
	}

	tests := []struct {
		name   string
		server *Server
		bearer string
	}{
		{"no token", srv, ""},
		{"wrong token", srv, "wrong-token"},
		{"refresh token", srv, refreshToken},
		{"expired access token", expired, "expired-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := getHealthOp(tt.server, "/health/op", tt.bearer)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", w.Code)
			}
		})
	}
}

// The unauthenticated liveness route keeps working exactly as before.
func TestHealth_RemainsUnauthenticatedAndUnchanged(t *testing.T) {
	srv, _, _ := newTestServer()

	w := getHealthOp(srv, "/health", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"status":"ok"}` {
		t.Errorf("body = %s, want {\"status\":\"ok\"}", got)
	}
}

func TestHealthOp_RejectsNonGet(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/health/op", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHealthOp_RejectsForeignPeerUID(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/health/op", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req = req.WithContext(withPeerUID(req.Context(), os.Getuid()+1))
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a foreign peer uid", w.Code)
	}
}

// By default the endpoint answers from metadata alone: it must say the session
// was not probed (never "healthy") and must not run any authenticated command,
// since a remote caller must not be able to raise a biometric prompt by
// polling health.
func TestHealthOp_DefaultIsMetadataOnly(t *testing.T) {
	logPath := installHealthOpFake(t)
	srv, accessToken, _ := newTestServer()

	w := getHealthOp(srv, "/health/op", accessToken)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := decodeObject(t, w)
	want := map[string]any{
		"op_present":     true,
		"op_version":     "2.41.0",
		"integration":    "reachable",
		"account_count":  float64(1),
		"session":        "not_probed",
		"session_reason": "",
	}
	if len(body) != len(want) {
		t.Errorf("response has %d fields, want exactly %d: %v", len(body), len(want), body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading invocation log: %v", err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(call, "vault") || strings.HasPrefix(call, "whoami") {
			t.Errorf("default /health/op ran %q", call)
		}
	}
}

// THE INCIDENT, at the endpoint: whoami fails with "not signed in" while the
// authenticated read works. With probe=1 the session must be reported healthy
// and whoami must never be run.
func TestHealthOp_ProbeReportsHealthyWhenWhoamiWouldFail(t *testing.T) {
	logPath := installHealthOpFake(t)
	srv, accessToken, _ := newTestServer()

	w := getHealthOp(srv, "/health/op?probe=1", accessToken)

	body := decodeObject(t, w)
	if body["session"] != "healthy" {
		t.Fatalf("session = %v, want healthy", body["session"])
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "whoami") {
		t.Errorf("endpoint invoked whoami:\n%s", data)
	}
}

func TestHealthOp_ProbeReportsFailureWithClassification(t *testing.T) {
	installHealthOpFake(t)
	t.Setenv("FAKE_OP_VAULT_EXIT", "1")
	srv, accessToken, _ := newTestServer()

	body := decodeObject(t, getHealthOp(srv, "/health/op?probe=1", accessToken))

	if body["session"] != "failed" || body["session_reason"] != "not_signed_in" {
		t.Errorf("session = %v/%v, want failed/not_signed_in", body["session"], body["session_reason"])
	}
}

// The response is a diagnostic: it must never carry token material, vault or
// account data, or raw op output, even when op echoes a secret on stderr.
func TestHealthOp_NeverLeaksTokensOrOpOutput(t *testing.T) {
	installHealthOpFake(t)
	t.Setenv("FAKE_OP_VAULT_EXIT", "1")
	srv, accessToken, refreshToken := newTestServer()
	t.Setenv("FAKE_OP_SECRET", accessToken)

	for _, target := range []string{"/health/op", "/health/op?probe=1"} {
		w := getHealthOp(srv, target, accessToken)
		raw := w.Body.String()
		for name, leaked := range map[string]string{
			"access token":  accessToken,
			"refresh token": refreshToken,
			"vault name":    "SECRET-VAULT-NAME",
			"account url":   "my.1password.com",
			"account email": "someone@example.com",
		} {
			if strings.Contains(raw, leaked) {
				t.Errorf("%s response leaks the %s: %s", target, name, raw)
			}
		}
	}
}

// Each probe can raise a prompt on the host, so only one may run at a time.
// A second probe request is refused, while the metadata-only form stays
// available.
func TestHealthOp_ConcurrentProbeIsRefused(t *testing.T) {
	installHealthOpFake(t)
	srv, accessToken, _ := newTestServer()
	srv.probeMu.Lock()
	defer srv.probeMu.Unlock()

	if w := getHealthOp(srv, "/health/op?probe=1", accessToken); w.Code != http.StatusConflict {
		t.Errorf("concurrent probe status = %d, want 409", w.Code)
	}
	if w := getHealthOp(srv, "/health/op", accessToken); w.Code != http.StatusOK {
		t.Errorf("metadata-only status = %d, want 200 while a probe is running", w.Code)
	}
}

func TestHealthOp_ReportsAbsentOp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	srv, accessToken, _ := newTestServer()

	body := decodeObject(t, getHealthOp(srv, "/health/op", accessToken))

	if body["op_present"] != false || body["integration"] != "unavailable" || body["session"] != "not_probed" {
		t.Errorf("absent-op report = %v", body)
	}
}

// A failed response write is logged and must not panic or alter the status.
func TestHealthOp_WriteFailureIsLogged(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	srv, accessToken, _ := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/health/op", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	srv.handleHealthOp(failingResponseWriter{httptest.NewRecorder()}, req)
}

type failingResponseWriter struct{ *httptest.ResponseRecorder }

func (failingResponseWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// A locked 1Password app answers metadata but never answers the authenticated
// read. The endpoint must report that as app_locked, and because the HTTP
// reply cannot show progress, the daemon log (where the person at the host
// is looking) must say it is waiting for an unlock or approval.
func TestHealthOp_ProbeAgainstALockedAppReportsAppLockedAndAnnouncesTheWait(t *testing.T) {
	installHealthOpFake(t)
	t.Setenv("FAKE_OP_VAULT_SLEEP", "30")
	srv, accessToken, _ := newTestServer()
	srv.probeTimeout = 300 * time.Millisecond
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	body := decodeObject(t, getHealthOp(srv, "/health/op?probe=1", accessToken))

	if body["integration"] != "reachable" || body["session"] != "failed" || body["session_reason"] != "app_locked" {
		t.Errorf("report = %v, want reachable/failed/app_locked", body)
	}
	if got := logs.String(); !strings.Contains(got, "waiting up to 300ms") || !strings.Contains(got, "unlock or approve") {
		t.Errorf("daemon log does not announce the wait:\n%s", got)
	}
}

// Without probe=1 nothing waits, so nothing is announced.
func TestHealthOp_MetadataOnlyRequestDoesNotAnnounceAWait(t *testing.T) {
	installHealthOpFake(t)
	srv, accessToken, _ := newTestServer()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	getHealthOp(srv, "/health/op", accessToken)

	if strings.Contains(logs.String(), "waiting") {
		t.Errorf("metadata-only request logged a wait: %s", logs.String())
	}
}

// The default probe timeout applies when none is injected.
func TestHealthOp_ProbeUsesTheDefaultTimeoutWhenNoneIsSet(t *testing.T) {
	installHealthOpFake(t)
	srv, accessToken, _ := newTestServer()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	getHealthOp(srv, "/health/op?probe=1", accessToken)

	if want := "waiting up to " + opcheck.DefaultProbeTimeout.String(); !strings.Contains(logs.String(), want) {
		t.Errorf("log %q does not contain %q", logs.String(), want)
	}
}
