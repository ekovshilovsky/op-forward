package cmd

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/executor"
)

// proxyExitInfraFailure is the exit code the proxy uses to signal that
// the forwarding infrastructure itself failed (no token, no tunnel, daemon
// unreachable). The shim checks for this code to decide whether to fall
// back to the real op binary. This is distinct from op-originated exit
// codes which are relayed as-is.
const proxyExitInfraFailure = 127

// runProxy is the client-side command that forwards op arguments to the host
// daemon and reproduces its stdout/stderr/exit code locally.
//
// Token refresh flow:
//  1. Try the request with the cached access token.
//  2. On HTTP 401, attempt a token refresh using the refresh token.
//  3. On successful refresh, save both new tokens and retry the request.
//  4. On failed refresh, print a clear error and exit.
//
// Exit contract, which the op shim depends on: every infrastructure failure
// prints its diagnostic to stderr and returns ExitStatus(127), so the shim
// falls back to the real op binary. A completed round trip returns the exit
// code of the forwarded op command as-is (nil for zero). Flag errors return
// ExitStatus(2), and a help request returns ExitStatus(0).
func runProxy() error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	addr := fs.String("addr", os.Getenv(endpoint.EnvAddr), "Daemon endpoint: tcp://host:port or unix:///path.sock (overrides --host/--port)")
	port := fs.Int("port", endpoint.PortFromEnv(), "Daemon port")
	host := fs.String("host", endpoint.HostFromEnv(), "Daemon host (set to host.docker.internal for Docker Desktop containers)")
	timeoutMs := fs.Int("timeout", getProxyTimeout(), "Request timeout in milliseconds")
	if err := parseFlags(fs, os.Args[2:]); err != nil {
		return err
	}

	args := fs.Args()

	ep, err := endpoint.ForDial(endpointArg(*addr, explicitFlags(fs), "host", "port"), *host, *port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
		return ExitStatus(proxyExitInfraFailure)
	}

	// Resolve token file paths.
	accessPath := proxyTokenPath(auth.AccessTokenFile)
	refreshPath := proxyTokenPath(auth.RefreshTokenFile)
	legacyPath := proxyTokenPath(auth.LegacyTokenFile)

	// Read access token, with fallback to legacy session.token.
	// Also check expiry so we can skip straight to refresh when stale.
	accessToken, accessValid := readToken(accessPath)
	if accessToken == "" {
		accessToken, accessValid = readToken(legacyPath)
	}
	if accessToken == "" {
		// No access token at all — check for a refresh token.
		if readTokenValue(refreshPath) == "" {
			printNoTokenError()
			return ExitStatus(proxyExitInfraFailure)
		}
		// Fall through — accessValid is false, will trigger refresh below.
	}

	// Probe daemon availability before building the request so a missing
	// tunnel fails fast and the shim can fall back to the local op binary.
	if err := ep.Probe(time.Duration(getProbeTimeoutMs()) * time.Millisecond); err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: daemon not reachable at %s\n", ep)
		return ExitStatus(proxyExitInfraFailure)
	}

	// Build the request body.
	reqBody := executor.Request{
		Args:      args,
		TimeoutMs: *timeoutMs,
	}
	// Marshaling a struct of a string slice and an int cannot fail, so the
	// error is discarded rather than guarded with unreachable handling.
	bodyBytes, _ := json.Marshal(reqBody)

	httpTimeout := time.Duration(*timeoutMs)*time.Millisecond + 5*time.Second
	client := ep.HTTPClient(httpTimeout)

	// If the access token is known-expired, skip directly to refresh
	// instead of burning a round-trip to get a 401.
	var resp *http.Response
	var respBody []byte

	if accessValid && accessToken != "" {
		resp, respBody, err = executeWithAuth(client, ep, bodyBytes, accessToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
			return ExitStatus(proxyExitInfraFailure)
		}
	}

	// On 401 or known-expired access token, attempt a token refresh and retry.
	if resp == nil || resp.StatusCode == http.StatusUnauthorized {
		refreshToken := readTokenValue(refreshPath)
		if refreshToken == "" {
			// Try legacy path as refresh token (migration scenario).
			// This works because the daemon's MigrateLegacyToken() promotes
			// the same session.token value to its in-memory refresh token on
			// startup, so the values will match as long as the daemon has
			// been restarted with the new two-tier code.
			refreshToken = readTokenValue(legacyPath)
		}
		if refreshToken == "" {
			printNoTokenError()
			return ExitStatus(proxyExitInfraFailure)
		}

		newTokens, refreshErr := attemptRefresh(client, ep, refreshToken)
		if refreshErr != nil {
			// Refresh failed — print actionable error and exit.
			printRefreshFailedError(refreshErr)
			return ExitStatus(proxyExitInfraFailure)
		}

		// Save both new tokens to disk.
		saveTokenFile(accessPath, newTokens.AccessToken, newTokens.AccessExpires)
		saveTokenFile(refreshPath, newTokens.RefreshToken, newTokens.RefreshExpires)

		// Retry the original request with the new access token.
		resp, respBody, err = executeWithAuth(client, ep, bodyBytes, newTokens.AccessToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
			return ExitStatus(proxyExitInfraFailure)
		}
	}

	// Surface update availability to the user via stderr so it
	// does not interfere with stdout (which carries op output).
	if avail := resp.Header.Get("X-Update-Available"); avail != "" {
		fmt.Fprintf(os.Stderr, "op-forward: update available (v%s → v%s) — run: op-forward update\n", Version, avail)
	}

	// 426 Upgrade Required means the client is below the daemon's
	// minimum version and must update before further commands.
	if resp.StatusCode == http.StatusUpgradeRequired {
		fmt.Fprintf(os.Stderr, "op-forward: %s\n", string(respBody))
		return ExitStatus(proxyExitInfraFailure)
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "op-forward: daemon returned HTTP %d\n", resp.StatusCode)
		fmt.Fprint(os.Stderr, string(respBody))
		return ExitStatus(proxyExitInfraFailure)
	}

	// Parse JSON response
	var result executor.Result
	if err := json.Unmarshal(respBody, &result); err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: parsing response: %v\n", err)
		return ExitStatus(proxyExitInfraFailure)
	}

	// Relay op command output and exit code as-is
	if result.Stdout != "" {
		// A write failure here means the consumer closed stdout (for example a
		// terminated pipeline). There is no channel left to report it on, and
		// the op exit code below is still relayed to the caller.
		_, _ = fmt.Fprint(os.Stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	if result.ExitCode == 0 {
		return nil
	}
	return ExitStatus(result.ExitCode)
}

// executeWithAuth sends a POST to /op/execute with the given bearer token.
func executeWithAuth(client *http.Client, ep endpoint.Endpoint, body []byte, token string) (*http.Response, []byte, error) {
	req, err := http.NewRequest("POST", ep.BaseURL()+"/op/execute", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "op-forward/"+Version)
	req.Header.Set("X-Client-Version", Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	// The body is read in full below; a close failure on a fully consumed
	// response carries no actionable information.
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response: %w", err)
	}
	return resp, respBody, nil
}

// tokenRefreshResponse mirrors the daemon's TokenRefreshResponse.
type tokenRefreshResponse struct {
	AccessToken    string `json:"access_token"`
	AccessExpires  string `json:"access_expires"`
	RefreshToken   string `json:"refresh_token"`
	RefreshExpires string `json:"refresh_expires"`
	Error          string `json:"error,omitempty"`
	Message        string `json:"message,omitempty"`
}

// attemptRefresh calls the daemon's /token/refresh endpoint with the given
// refresh token and returns the new token pair on success.
func attemptRefresh(client *http.Client, ep endpoint.Endpoint, refreshToken string) (*tokenRefreshResponse, error) {
	req, err := http.NewRequest("POST", ep.BaseURL()+"/token/refresh", nil)
	if err != nil {
		return nil, fmt.Errorf("creating refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("X-Client-Version", Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	// The body is read in full below; a close failure on a fully consumed
	// response carries no actionable information.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp tokenRefreshResponse
		// A non-JSON error body is expected from intermediaries; in that case
		// errResp.Message stays empty and the raw HTTP status and body are
		// reported below instead.
		_ = json.Unmarshal(body, &errResp)
		if errResp.Message != "" {
			return nil, fmt.Errorf("%s", errResp.Message)
		}
		return nil, fmt.Errorf("refresh returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokens tokenRefreshResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parsing refresh response: %w", err)
	}
	return &tokens, nil
}

// ---------- Token file helpers ----------

// proxyTokenPath returns the path to a token file on the VM (proxy) side.
// It delegates to the auth package so the proxy and the daemon resolve
// OP_FORWARD_TOKEN_FILE / OP_FORWARD_TOKEN_DIR with identical precedence.
// If the user cache directory cannot be determined, ~/.cache is used so the
// proxy still has somewhere to persist refreshed tokens.
func proxyTokenPath(filename string) string {
	var path string
	var err error
	switch filename {
	case auth.AccessTokenFile:
		path, err = auth.AccessTokenPath()
	case auth.RefreshTokenFile:
		path, err = auth.RefreshTokenPath()
	default:
		path, err = auth.LegacyTokenPath()
	}
	if err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", auth.CacheDirName, filename)
}

// readTokenValue reads the first line (token value) from a token file.
// Returns empty string on any error.
func readTokenValue(path string) string {
	val, _ := readToken(path)
	return val
}

// readToken reads a token file and returns the value and whether it is
// still valid (not expired). Returns ("", false) on any error.
func readToken(path string) (value string, valid bool) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from the invoking user's own OP_FORWARD_TOKEN_FILE/OP_FORWARD_TOKEN_DIR environment or their cache directory, the same user who owns the token files being read
	if err != nil {
		return "", false
	}
	// SplitN with a positive count always yields at least one element, so
	// the first line can be taken unconditionally.
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	val := lines[0]
	if len(lines) < 2 {
		return val, false
	}
	expires, err := time.Parse(time.RFC3339, lines[1])
	if err != nil {
		return val, false
	}
	return val, time.Now().Before(expires)
}

// writeTokenFile writes a token value and expiry to disk atomically: the
// token directory is created with mode 0700 and the file with mode 0600, via
// a temporary file renamed into place so a reader never sees a partial token.
// It is the single persistence primitive for tokens the client side receives,
// shared by the proxy (which treats a failure as a warning) and by doctor
// (which must surface it, because a refresh rotates the refresh token).
func writeTokenFile(path, value, expires string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	content := fmt.Sprintf("%s\n%s\n", value, expires)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort removal of the staged temp file; the rename failure
		// returned below is the actionable error.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// saveTokenFile persists a token and downgrades a failure to a warning, which
// is the proxy's behavior: the freshly issued access token is still held in
// memory and usable for the request in flight even if caching it failed.
func saveTokenFile(path, value, expires string) {
	if err := writeTokenFile(path, value, expires); err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: warning: could not cache token at %s: %v\n", path, err)
	}
}

// ---------- Error messages ----------

func printNoTokenError() {
	fmt.Fprintln(os.Stderr,
		"op-forward: no authentication token found. The op-forward token files "+
			"are missing from this VM. Run your deployment script to push tokens, "+
			"or copy the refresh.token file from the host:\n"+
			"  scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token\n\n"+
			"This is NOT a 1Password issue — it is the op-forward inter-VM session token.")
}

func printRefreshFailedError(err error) {
	fmt.Fprintf(os.Stderr,
		"op-forward: token refresh failed — %v\n\n"+
			"The op-forward access token expired and the refresh token could not "+
			"obtain a new one. This typically means the refresh token has not been "+
			"used in over 30 days and the daemon was restarted with a new token.\n\n"+
			"To fix this, re-run your deployment script to push a fresh token, or:\n"+
			"  scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token\n\n"+
			"This is NOT a 1Password authentication issue — it is the op-forward "+
			"inter-VM session token that needs to be redeployed.\n", err)
}
