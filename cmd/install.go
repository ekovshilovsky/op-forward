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
// It intercepts all op invocations and delegates to op-forward proxy,
// which handles JSON encoding, HTTP transport, and response parsing in Go.
// Falls back to the real op binary if op-forward proxy fails to connect.
const shimTemplate = `#!/bin/bash
# op-forward shim — forwards op commands to host daemon via SSH tunnel.
# Installed by op-forward. Do not edit directly.

REAL_OP="{{REAL_OP}}"
OP_FORWARD_BIN="{{OP_FORWARD_BIN}}"

# Delegate to op-forward proxy (handles tunnel probe, auth, JSON, HTTP)
if [ -x "$OP_FORWARD_BIN" ]; then
  "$OP_FORWARD_BIN" proxy -- "$@"
  EXIT=$?
  # Exit code 0 or op-level non-zero: use as-is
  # If proxy itself failed (e.g. tunnel down), fall back to real op
  if [ $EXIT -ne 127 ]; then
    exit $EXIT
  fi
fi

# Fallback to real op binary if proxy is unavailable
if [ -n "$REAL_OP" ] && [ -x "$REAL_OP" ]; then
  exec "$REAL_OP" "$@"
fi

echo "op-forward: proxy unavailable and no fallback op binary" >&2
exit 1
`

func runInstall() error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	_ = fs.Int("port", getInstallPort(), "Daemon port (stored in shim for legacy compat)")
	fs.Parse(os.Args[2:])

	// Find the real op binary (before our shim shadows it)
	realOp := findRealOp()

	// Determine shim install location
	shimDir := filepath.Join(os.Getenv("HOME"), ".local", "bin")
	shimPath := filepath.Join(shimDir, "op")

	// Locate the op-forward binary (should be in the same directory)
	opForwardBin := filepath.Join(shimDir, "op-forward")
	if _, err := os.Stat(opForwardBin); err != nil {
		// Try the current executable path
		if exe, err := os.Executable(); err == nil {
			opForwardBin = exe
		}
	}

	if err := os.MkdirAll(shimDir, 0755); err != nil {
		return fmt.Errorf("creating shim directory: %w", err)
	}

	// Generate the shim script with paths to op-forward binary and real op
	shim := strings.ReplaceAll(shimTemplate, "{{OP_FORWARD_BIN}}", opForwardBin)
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
