package cmd

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
func runProxy() error {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	port := fs.Int("port", getProxyPort(), "Daemon port")
	timeoutMs := fs.Int("timeout", getProxyTimeout(), "Request timeout in milliseconds")
	fs.Parse(os.Args[2:])

	args := fs.Args()

	// Resolve token file paths.
	accessPath := proxyTokenPath("access.token")
	refreshPath := proxyTokenPath("refresh.token")
	legacyPath := proxyTokenPath("session.token")

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
			os.Exit(proxyExitInfraFailure)
		}
		// Fall through — accessValid is false, will trigger refresh below.
	}

	// Probe tunnel availability (fast TCP check).
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: tunnel not available on port %d\n", *port)
		os.Exit(proxyExitInfraFailure)
	}
	conn.Close()

	// Build the request body.
	reqBody := executor.Request{
		Args:      args,
		TimeoutMs: *timeoutMs,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: encoding request: %v\n", err)
		os.Exit(proxyExitInfraFailure)
	}

	httpTimeout := time.Duration(*timeoutMs)*time.Millisecond + 5*time.Second
	client := &http.Client{Timeout: httpTimeout}

	// If the access token is known-expired, skip directly to refresh
	// instead of burning a round-trip to get a 401.
	var resp *http.Response
	var respBody []byte

	if accessValid && accessToken != "" {
		resp, respBody, err = executeWithAuth(client, addr, bodyBytes, accessToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
			os.Exit(proxyExitInfraFailure)
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
			os.Exit(proxyExitInfraFailure)
		}

		newTokens, refreshErr := attemptRefresh(client, addr, refreshToken)
		if refreshErr != nil {
			// Refresh failed — print actionable error and exit.
			printRefreshFailedError(refreshErr)
			os.Exit(proxyExitInfraFailure)
		}

		// Save both new tokens to disk.
		saveTokenFile(accessPath, newTokens.AccessToken, newTokens.AccessExpires)
		saveTokenFile(refreshPath, newTokens.RefreshToken, newTokens.RefreshExpires)

		// Retry the original request with the new access token.
		resp, respBody, err = executeWithAuth(client, addr, bodyBytes, newTokens.AccessToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
			os.Exit(proxyExitInfraFailure)
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
		os.Exit(proxyExitInfraFailure)
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "op-forward: daemon returned HTTP %d\n", resp.StatusCode)
		fmt.Fprint(os.Stderr, string(respBody))
		os.Exit(proxyExitInfraFailure)
	}

	// Parse JSON response
	var result executor.Result
	if err := json.Unmarshal(respBody, &result); err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: parsing response: %v\n", err)
		os.Exit(proxyExitInfraFailure)
	}

	// Relay op command output and exit code as-is
	if result.Stdout != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	os.Exit(result.ExitCode)
	return nil // unreachable
}

// executeWithAuth sends a POST to /op/execute with the given bearer token.
func executeWithAuth(client *http.Client, addr string, body []byte, token string) (*http.Response, []byte, error) {
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/op/execute", addr), bytes.NewReader(body))
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
	defer resp.Body.Close()

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
func attemptRefresh(client *http.Client, addr, refreshToken string) (*tokenRefreshResponse, error) {
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/token/refresh", addr), nil)
	if err != nil {
		return nil, fmt.Errorf("creating refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("X-Client-Version", Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp tokenRefreshResponse
		json.Unmarshal(body, &errResp)
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
// Uses os.UserCacheDir so the path is correct on both macOS
// (~/Library/Caches) and Linux (~/.cache).
func proxyTokenPath(filename string) string {
	if path := os.Getenv("OP_FORWARD_TOKEN_FILE"); path != "" && filename == "access.token" {
		return path
	}
	if dir := os.Getenv("OP_FORWARD_TOKEN_DIR"); dir != "" {
		return filepath.Join(dir, filename)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheDir, "op-forward", filename)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) == 0 {
		return "", false
	}
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

// saveTokenFile writes a token value and expiry to disk atomically.
func saveTokenFile(path, value, expires string) {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)
	content := fmt.Sprintf("%s\n%s\n", value, expires)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: warning: could not cache token at %s: %v\n", path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
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

// ---------- Config helpers ----------

func getProxyPort() int {
	if p := os.Getenv("OP_FORWARD_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	return DefaultPort
}

func getProxyTimeout() int {
	if t := os.Getenv("OP_FORWARD_FETCH_TIMEOUT_MS"); t != "" {
		if ms, err := strconv.Atoi(t); err == nil {
			return ms
		}
	}
	return 60000
}
