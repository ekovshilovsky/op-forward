package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/executor"
)

func newTestServer() (*Server, string) {
	token := &auth.Token{
		Value:   "test-token-abc123",
		Expires: time.Now().Add(24 * time.Hour),
	}
	return &Server{token: token, port: 18340}, token.Value
}

// newTestServerWithTempToken creates a test server that writes tokens to a temp
// directory instead of the real token path. Prevents tests from corrupting the
// running daemon's token file.
func newTestServerWithTempToken(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(t.TempDir(), "test.token"))
	return newTestServer()
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := newTestServer()
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("health status = %d, want 200", w.Code)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("health body status = %q, want 'ok'", body["status"])
	}
}

func TestExecute_NoAuth(t *testing.T) {
	srv, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestExecute_WrongToken(t *testing.T) {
	srv, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestExecute_ExpiredToken(t *testing.T) {
	token := &auth.Token{
		Value:   "expired-token",
		Expires: time.Now().Add(-time.Hour),
	}
	srv := &Server{token: token, port: 18340}

	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer expired-token")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for expired token", w.Code)
	}
}

func TestExecute_WrongMethod(t *testing.T) {
	srv, token := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("GET", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	srv, token := newTestServerWithTempToken(t)
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestExecute_BlockedSubcommand(t *testing.T) {
	srv, token := newTestServerWithTempToken(t)
	body, _ := json.Marshal(executor.Request{Args: []string{"signin"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	// Blocked subcommand returns 200 with exit_code 1 (validation error in executor)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (validation error returned in response body)", w.Code)
	}

	var result executor.Result
	json.NewDecoder(w.Body).Decode(&result)
	if result.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", result.ExitCode)
	}
	if result.Stderr == "" {
		t.Error("stderr should contain error message for blocked subcommand")
	}
}

func TestAuthenticate_MissingHeader(t *testing.T) {
	srv, _ := newTestServer()
	req := httptest.NewRequest("POST", "/op/execute", nil)

	if srv.authenticate(req) {
		t.Error("should reject request with no Authorization header")
	}
}

func TestAuthenticate_InvalidScheme(t *testing.T) {
	srv, token := newTestServer()
	req := httptest.NewRequest("POST", "/op/execute", nil)
	req.Header.Set("Authorization", "Basic "+token)

	if srv.authenticate(req) {
		t.Error("should reject non-Bearer auth scheme")
	}
}

func TestAuthenticate_ValidToken(t *testing.T) {
	srv, token := newTestServer()
	req := httptest.NewRequest("POST", "/op/execute", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	if !srv.authenticate(req) {
		t.Error("should accept valid Bearer token")
	}
}

func TestSanitizeArgsForLog(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			"simple command",
			[]string{"account", "list"},
			"account list",
		},
		{
			"password flag redacted",
			[]string{"item", "get", "--password", "secret123"},
			"item get --password [REDACTED]",
		},
		{
			"short password flag redacted",
			[]string{"item", "get", "-p", "secret123"},
			"item get -p [REDACTED]",
		},
		{
			"reveal not redacted (flag only, no value)",
			[]string{"item", "get", "abc", "--fields", "password", "--reveal"},
			"item get abc --fields password --reveal",
		},
		{
			"no args",
			[]string{},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeArgsForLog(tt.args)
			if result != tt.expected {
				t.Errorf("sanitizeArgsForLog(%v) = %q, want %q", tt.args, result, tt.expected)
			}
		})
	}
}
