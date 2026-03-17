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
// daemon and reproduces its stdout/stderr/exit code locally. This replaces
// the previous bash+python3 approach in the shim, keeping all logic in Go.
//
// Exit code contract with the shim:
//   - 127: proxy infrastructure failure (tunnel down, no token, daemon error)
//     → shim falls back to real op binary
//   - Any other code: relayed from the op command execution on the host
//     → shim exits with that code directly
func runProxy() error {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	port := fs.Int("port", getProxyPort(), "Daemon port")
	timeoutMs := fs.Int("timeout", getProxyTimeout(), "Request timeout in milliseconds")
	fs.Parse(os.Args[2:])

	args := fs.Args()

	// Read token
	tokenFile := os.Getenv("OP_FORWARD_TOKEN_FILE")
	if tokenFile == "" {
		home, _ := os.UserHomeDir()
		tokenFile = home + "/.cache/op-forward/session.token"
	}
	tokenData, err := os.ReadFile(tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
		os.Exit(proxyExitInfraFailure)
	}
	token := strings.SplitN(strings.TrimSpace(string(tokenData)), "\n", 2)[0]

	// Probe tunnel availability (fast TCP check)
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: tunnel not available on port %d\n", *port)
		os.Exit(proxyExitInfraFailure)
	}
	conn.Close()

	// Build and send request
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

	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/op/execute", addr), bytes.NewReader(bodyBytes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: creating request: %v\n", err)
		os.Exit(proxyExitInfraFailure)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "op-forward/"+Version)
	req.Header.Set("X-Client-Version", Version)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: %v\n", err)
		os.Exit(proxyExitInfraFailure)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "op-forward: reading response: %v\n", err)
		os.Exit(proxyExitInfraFailure)
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
