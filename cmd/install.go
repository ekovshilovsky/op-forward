package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
)

// shimTemplate is the op wrapper script installed on the remote side.
// It intercepts all op invocations and delegates to op-forward proxy,
// which handles JSON encoding, HTTP transport, and response parsing in Go.
// Falls back to the real op binary if op-forward proxy fails to connect.
// The shim is POSIX sh: NixOS has no /bin/bash at all, and minimal
// environments such as stock Alpine ship only /bin/sh, so a bash shebang
// would make the installed shim unrunnable exactly where op-forward is
// most useful. Every construct below is POSIX.
//
// The header's second line must keep containing opcheck.ShimSignature, which
// is how op-forward recognizes its own shim, and the version marker line must
// keep the shimVersionPrefix wording, which "op-forward doctor" reads to detect
// a shim installed by an older binary. The exit-code contract below (127 means
// the forwarding infrastructure failed and the real op is run; every other
// status is relayed unchanged) is relied on by callers and must not change.
const shimTemplate = `#!/bin/sh
# op-forward shim — forwards op commands to host daemon via SSH tunnel.
# Installed by op-forward. Do not edit directly.
# op-forward-shim-version: {{VERSION}}

REAL_OP="{{REAL_OP}}"
OP_FORWARD_BIN="{{OP_FORWARD_BIN}}"

# The recorded path can go stale when a package upgrade relocates the
# binary (0.7.1 and earlier installed to /usr/local/bin; packages now use
# /usr/bin). Fall back to whatever op-forward is on PATH before giving up.
if [ ! -x "$OP_FORWARD_BIN" ]; then
  OP_FORWARD_BIN="$(command -v op-forward 2>/dev/null || true)"
fi

# Delegate to op-forward proxy (handles tunnel probe, auth, JSON, HTTP)
if [ -n "$OP_FORWARD_BIN" ] && [ -x "$OP_FORWARD_BIN" ]; then
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
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	// The shim reads OP_FORWARD_ADDR / OP_FORWARD_HOST / OP_FORWARD_PORT at
	// run time, so nothing is baked in here. --port is accepted for scripts
	// that still pass it.
	_ = fs.Int("port", endpoint.PortFromEnv(), "Accepted for backward compatibility; the shim reads the environment at run time")
	if err := parseFlags(fs, os.Args[2:]); err != nil {
		return err
	}

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

	// #nosec G301 -- ~/.local/bin is a PATH directory that must stay world-traversable like the standard user bin directory; it holds only the executable shim, never secrets
	if err := os.MkdirAll(shimDir, 0755); err != nil {
		return fmt.Errorf("creating shim directory: %w", err)
	}

	shim := renderShim(opForwardBin, realOp)

	// #nosec G306 -- the shim is an executable script that must carry the execute bit; it contains only binary paths and no secrets
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

// shimVersionPrefix introduces the version marker comment in the shim.
const shimVersionPrefix = "# op-forward-shim-version: "

// renderShim fills the shim template with the op-forward binary path
// recorded at install time, the real op binary to fall back to, and the
// version of the installing binary (cmd.Version) as a staleness marker.
func renderShim(opForwardBin, realOp string) string {
	shim := strings.ReplaceAll(shimTemplate, "{{OP_FORWARD_BIN}}", opForwardBin)
	shim = strings.ReplaceAll(shim, "{{VERSION}}", shimVersionStamp(Version))
	return strings.ReplaceAll(shim, "{{REAL_OP}}", realOp)
}

// shimVersionStamp reduces a version to characters that are safe inside a
// shell comment. Released versions (1.2.3, 1.2.3-rc1, 1.2.3+dirty) pass
// through unchanged; anything else, such as a newline smuggled in through a
// build flag, is replaced so the stamp can never end the comment line and
// inject shell code into the shim. Doctor applies the same reduction to the
// running version before comparing.
func shimVersionStamp(v string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '+', r == '-', r == '_', r == '~':
			return r
		}
		return '_'
	}, v)
}

// parseShimVersion extracts the version recorded in a shim's marker line. The
// boolean is false for a shim that carries no marker, which is every shim
// installed before stamping existed. Only the leading comment block is
// consulted, so a marker-shaped line later in the script is ignored.
func parseShimVersion(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(line, shimVersionPrefix); ok {
			return strings.TrimSpace(rest), true
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			return "", false
		}
	}
	return "", false
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
