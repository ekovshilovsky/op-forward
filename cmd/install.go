package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// shimTemplate is the op wrapper script installed on the remote side.
// It intercepts all op invocations and forwards them to the host daemon.
const shimTemplate = `#!/bin/bash
# op-forward shim — forwards op commands to host daemon via SSH tunnel.
# Installed by op-forward. Do not edit directly.
#
# Falls back to the real op binary if the tunnel is unavailable.

set -euo pipefail

OP_FORWARD_PORT="${OP_FORWARD_PORT:-{{PORT}}}"
OP_FORWARD_PROBE_TIMEOUT="${OP_FORWARD_PROBE_TIMEOUT_MS:-500}"
OP_FORWARD_FETCH_TIMEOUT="${OP_FORWARD_FETCH_TIMEOUT_MS:-60000}"
REAL_OP="{{REAL_OP}}"

# Read token from file (first line = token, second line = expiry)
TOKEN_FILE="${OP_FORWARD_TOKEN_FILE:-${HOME}/.cache/op-forward/session.token}"
if [ ! -f "$TOKEN_FILE" ]; then
  if [ -n "$REAL_OP" ] && [ -x "$REAL_OP" ]; then
    exec "$REAL_OP" "$@"
  fi
  echo "op-forward: no token file and no fallback op binary" >&2
  exit 1
fi
TOKEN=$(head -1 "$TOKEN_FILE")

# Probe tunnel availability (fast TCP check)
if ! bash -c "echo >/dev/tcp/127.0.0.1/$OP_FORWARD_PORT" 2>/dev/null; then
  if [ -n "$REAL_OP" ] && [ -x "$REAL_OP" ]; then
    exec "$REAL_OP" "$@"
  fi
  echo "op-forward: tunnel not available on port $OP_FORWARD_PORT" >&2
  exit 1
fi

# Build JSON args array
ARGS_JSON="["
FIRST=true
for arg in "$@"; do
  if [ "$FIRST" = true ]; then
    FIRST=false
  else
    ARGS_JSON+=","
  fi
  # Escape special JSON characters in the argument
  ESCAPED=$(printf '%s' "$arg" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()), end="")')
  ARGS_JSON+="$ESCAPED"
done
ARGS_JSON+="]"

BODY="{\"args\":${ARGS_JSON},\"timeout_ms\":${OP_FORWARD_FETCH_TIMEOUT}}"

# Execute via daemon
RESPONSE=$(curl -s -w "\n%{http_code}" \
  --max-time "$((OP_FORWARD_FETCH_TIMEOUT / 1000 + 5))" \
  -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "User-Agent: op-forward/0.1" \
  -d "$BODY" \
  "http://127.0.0.1:${OP_FORWARD_PORT}/op/execute" 2>/dev/null)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
RESPONSE_BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" != "200" ]; then
  echo "op-forward: daemon returned HTTP $HTTP_CODE" >&2
  echo "$RESPONSE_BODY" >&2
  exit 1
fi

# Parse JSON response and output stdout/stderr appropriately
STDOUT=$(echo "$RESPONSE_BODY" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r.get("stdout",""), end="")')
STDERR=$(echo "$RESPONSE_BODY" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r.get("stderr",""), end="")')
EXIT_CODE=$(echo "$RESPONSE_BODY" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(r.get("exit_code",1))')

[ -n "$STDOUT" ] && printf '%s' "$STDOUT"
[ -n "$STDERR" ] && printf '%s' "$STDERR" >&2
exit "$EXIT_CODE"
`

func runInstall() error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	port := fs.Int("port", getInstallPort(), "Daemon port")
	fs.Parse(os.Args[2:])

	// Find the real op binary (before our shim shadows it)
	realOp := findRealOp()

	// Determine shim install location
	shimDir := filepath.Join(os.Getenv("HOME"), ".local", "bin")
	shimPath := filepath.Join(shimDir, "op")

	if err := os.MkdirAll(shimDir, 0755); err != nil {
		return fmt.Errorf("creating shim directory: %w", err)
	}

	// Generate the shim script with the correct port and real op path
	shim := strings.ReplaceAll(shimTemplate, "{{PORT}}", strconv.Itoa(*port))
	shim = strings.ReplaceAll(shim, "{{REAL_OP}}", realOp)

	if err := os.WriteFile(shimPath, []byte(shim), 0755); err != nil {
		return fmt.Errorf("writing shim: %w", err)
	}

	fmt.Printf("Shim installed:\n")
	fmt.Printf("  target:    op\n")
	fmt.Printf("  shim:      %s\n", shimPath)
	fmt.Printf("  real bin:  %s\n", realOp)

	// Verify PATH priority
	which, err := exec.LookPath("op")
	if err == nil {
		fmt.Printf("  PATH:      'which op' resolves to %s", which)
		if which == shimPath {
			fmt.Printf(" (shim)\n")
		} else {
			fmt.Printf(" (NOT shim — ensure %s is first in PATH)\n", shimDir)
		}
	}

	return nil
}

// findRealOp locates the real op binary, skipping any existing shim.
func findRealOp() string {
	shimDir := filepath.Join(os.Getenv("HOME"), ".local", "bin")

	pathDirs := strings.Split(os.Getenv("PATH"), ":")
	for _, dir := range pathDirs {
		if dir == shimDir {
			continue // Skip our shim directory
		}
		candidate := filepath.Join(dir, "op")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "/usr/bin/op" // Default fallback
}

func getInstallPort() int {
	if p := os.Getenv("OP_FORWARD_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	return DefaultPort
}
