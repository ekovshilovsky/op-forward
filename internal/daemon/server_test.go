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

func newTestServer() (*Server, string, string) {
	accessToken := &auth.Token{
		Value:   "test-access-token-abc123",
		Expires: time.Now().Add(1 * time.Hour),
		TTL:     auth.AccessTokenTTL,
	}
	refreshToken := &auth.Token{
		Value:   "test-refresh-token-xyz789",
		Expires: time.Now().Add(30 * 24 * time.Hour),
		TTL:     auth.RefreshTokenTTL,
	}
	srv := &Server{
		accessToken:  accessToken,
		refreshToken: refreshToken,
		port:         18340,
		version:      "0.3.0",
	}
	return srv, accessToken.Value, refreshToken.Value
}

// newTestServerWithTempToken creates a test server that writes tokens to a temp
// directory instead of the real token path. Prevents tests from corrupting the
// running daemon's token file.
func newTestServerWithTempToken(t *testing.T) (*Server, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(tmpDir, "access.token"))
	t.Setenv("OP_FORWARD_TOKEN_DIR", tmpDir)
	return newTestServer()
}

func TestHealthEndpoint(t *testing.T) {
	srv, _, _ := newTestServer()
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
	srv, _, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestExecute_WrongToken(t *testing.T) {
	srv, _, _ := newTestServer()
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
	accessToken := &auth.Token{
		Value:   "expired-token",
		Expires: time.Now().Add(-time.Hour),
		TTL:     auth.AccessTokenTTL,
	}
	refreshToken := &auth.Token{
		Value:   "refresh-token",
		Expires: time.Now().Add(30 * 24 * time.Hour),
		TTL:     auth.RefreshTokenTTL,
	}
	srv := &Server{accessToken: accessToken, refreshToken: refreshToken, port: 18340, version: "0.3.0"}

	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer expired-token")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for expired token", w.Code)
	}
}

func TestExecute_RefreshTokenRejectedForExecute(t *testing.T) {
	srv, _, refreshToken := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — refresh token must not work for /op/execute", w.Code)
	}
}

func TestExecute_WrongMethod(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("GET", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	srv, accessToken, _ := newTestServerWithTempToken(t)
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestExecute_BlockedSubcommand(t *testing.T) {
	srv, accessToken, _ := newTestServerWithTempToken(t)
	body, _ := json.Marshal(executor.Request{Args: []string{"signin"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
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
	srv, _, _ := newTestServer()
	req := httptest.NewRequest("POST", "/op/execute", nil)

	if srv.authenticate(req) {
		t.Error("should reject request with no Authorization header")
	}
}

func TestAuthenticate_InvalidScheme(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	req := httptest.NewRequest("POST", "/op/execute", nil)
	req.Header.Set("Authorization", "Basic "+accessToken)

	if srv.authenticate(req) {
		t.Error("should reject non-Bearer auth scheme")
	}
}

func TestAuthenticate_ValidToken(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	req := httptest.NewRequest("POST", "/op/execute", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	if !srv.authenticate(req) {
		t.Error("should accept valid Bearer token")
	}
}

// ---------- Token refresh endpoint tests ----------

func TestTokenRefresh_ValidRefreshToken(t *testing.T) {
	srv, _, refreshToken := newTestServerWithTempToken(t)
	req := httptest.NewRequest("POST", "/token/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	w := httptest.NewRecorder()

	srv.handleTokenRefresh(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp TokenRefreshResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("response missing access_token")
	}
	if resp.RefreshToken == "" {
		t.Error("response missing refresh_token")
	}
	if resp.RefreshToken == refreshToken {
		t.Error("refresh token should be rotated to a new value")
	}

	// Verify the old refresh token no longer works.
	req2 := httptest.NewRequest("POST", "/token/refresh", nil)
	req2.Header.Set("Authorization", "Bearer "+refreshToken)
	w2 := httptest.NewRecorder()
	srv.handleTokenRefresh(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("old refresh token should be rejected, got status %d", w2.Code)
	}

	// Verify the new access token works for /op/execute.
	execBody, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req3 := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(execBody))
	req3.Header.Set("Authorization", "Bearer "+resp.AccessToken)
	w3 := httptest.NewRecorder()
	srv.handleExecute(w3, req3)
	if w3.Code == http.StatusUnauthorized {
		t.Error("new access token should be accepted for /op/execute")
	}
}

func TestTokenRefresh_ExpiredRefreshToken(t *testing.T) {
	srv, _, _ := newTestServerWithTempToken(t)
	srv.refreshToken = &auth.Token{
		Value:   "expired-refresh",
		Expires: time.Now().Add(-time.Hour),
		TTL:     auth.RefreshTokenTTL,
	}

	req := httptest.NewRequest("POST", "/token/refresh", nil)
	req.Header.Set("Authorization", "Bearer expired-refresh")
	w := httptest.NewRecorder()

	srv.handleTokenRefresh(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for expired refresh token", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "refresh_token_expired" {
		t.Errorf("error = %q, want 'refresh_token_expired'", resp["error"])
	}
	if resp["message"] == "" {
		t.Error("should include a descriptive message")
	}
}

func TestTokenRefresh_InvalidRefreshToken(t *testing.T) {
	srv, _, _ := newTestServer()
	req := httptest.NewRequest("POST", "/token/refresh", nil)
	req.Header.Set("Authorization", "Bearer totally-wrong-token")
	w := httptest.NewRecorder()

	srv.handleTokenRefresh(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestTokenRefresh_WrongMethod(t *testing.T) {
	srv, _, _ := newTestServer()
	req := httptest.NewRequest("GET", "/token/refresh", nil)
	w := httptest.NewRecorder()

	srv.handleTokenRefresh(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestTokenRefresh_NoAuth(t *testing.T) {
	srv, _, _ := newTestServer()
	req := httptest.NewRequest("POST", "/token/refresh", nil)
	w := httptest.NewRecorder()

	srv.handleTokenRefresh(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestTokenRefresh_SlidingExpiry(t *testing.T) {
	srv, _, refreshToken := newTestServerWithTempToken(t)

	req := httptest.NewRequest("POST", "/token/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	w := httptest.NewRecorder()

	srv.handleTokenRefresh(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp TokenRefreshResponse
	json.NewDecoder(w.Body).Decode(&resp)

	expires, err := time.Parse(time.RFC3339, resp.RefreshExpires)
	if err != nil {
		t.Fatalf("parsing refresh_expires: %v", err)
	}

	// New refresh token should expire ~30 days from now (within 1 minute tolerance).
	expected := time.Now().Add(auth.RefreshTokenTTL)
	diff := expires.Sub(expected)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("refresh token expires at %v, expected ~%v (diff: %v)", expires, expected, diff)
	}
}

// ---------- Existing tests ----------

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

func TestExecute_VersionNegotiation_UpdateAvailable(t *testing.T) {
	srv, accessToken, _ := newTestServerWithTempToken(t)
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Version", "0.2.0")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	// Request should succeed (0.2.0 >= MinClientVersion 0.1.0)
	if w.Code == http.StatusUpgradeRequired {
		t.Errorf("status = 426, client 0.2.0 should not be rejected")
	}

	// Should advertise that an update is available
	if avail := w.Header().Get("X-Update-Available"); avail != "0.3.0" {
		t.Errorf("X-Update-Available = %q, want %q", avail, "0.3.0")
	}
}

func TestExecute_VersionNegotiation_UpgradeRequired(t *testing.T) {
	srv, accessToken, _ := newTestServerWithTempToken(t)

	// Temporarily raise the minimum version to force a rejection
	origMin := MinClientVersion
	MinClientVersion = "0.3.0"
	defer func() { MinClientVersion = origMin }()

	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Version", "0.2.0")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	if w.Code != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want 426 for client below minimum version", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("response should contain an error message")
	}
}

func TestExecute_VersionNegotiation_CurrentClient(t *testing.T) {
	srv, accessToken, _ := newTestServerWithTempToken(t)
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Version", "0.3.0")
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	// No update header when client matches server version
	if avail := w.Header().Get("X-Update-Available"); avail != "" {
		t.Errorf("X-Update-Available = %q, want empty for current client", avail)
	}
}

func TestExecute_VersionNegotiation_NoHeader(t *testing.T) {
	srv, accessToken, _ := newTestServerWithTempToken(t)
	body, _ := json.Marshal(executor.Request{Args: []string{"account", "list"}})
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	// No X-Client-Version header — older clients that don't send it
	w := httptest.NewRecorder()

	srv.handleExecute(w, req)

	// Should not reject clients that don't send the header (backward compatible)
	if w.Code == http.StatusUpgradeRequired {
		t.Error("should not reject clients without X-Client-Version header")
	}
}
