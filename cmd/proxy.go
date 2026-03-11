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

// runProxy is the client-side command that forwards op arguments to the host
// daemon and reproduces its stdout/stderr/exit code locally. This replaces
// the previous bash+python3 approach in the shim, keeping all logic in Go.
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
		return fmt.Errorf("reading token: %w", err)
	}
	token := strings.SplitN(strings.TrimSpace(string(tokenData)), "\n", 2)[0]

	// Probe tunnel availability (fast TCP check)
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("tunnel not available on port %d", *port)
	}
	conn.Close()

	// Build and send request
	reqBody := executor.Request{
		Args:      args,
		TimeoutMs: *timeoutMs,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	httpTimeout := time.Duration(*timeoutMs)*time.Millisecond + 5*time.Second
	client := &http.Client{Timeout: httpTimeout}

	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/op/execute", addr), bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "op-forward/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "op-forward: daemon returned HTTP %d\n", resp.StatusCode)
		fmt.Fprint(os.Stderr, string(respBody))
		os.Exit(1)
	}

	// Parse JSON response
	var result executor.Result
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

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
