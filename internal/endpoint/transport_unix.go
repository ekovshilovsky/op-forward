//go:build !windows

package endpoint

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (e Endpoint) listenUnix() (net.Listener, error) {
	dir := filepath.Dir(e.Address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating socket directory: %w", err)
	}
	if err := checkPrivateDir(dir); err != nil {
		// The directory check runs first because it is a security refusal
		// that must hold before anything is bound, but an over-long path is
		// just as fatal and is the next error the operator would meet once
		// the directory is fixed. Both are reported together so one edit to
		// the configuration fixes both (a shared directory such as /tmp
		// combined with a long path is the common real-world case).
		if hint := socketPathLengthHint(e.Address); hint != "" {
			return nil, fmt.Errorf("%w; also, %s", err, hint)
		}
		return nil, err
	}
	if info, err := os.Lstat(e.Address); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; refusing to replace it", e.Address)
		}
		if e.Probe(200*time.Millisecond) == nil {
			return nil, fmt.Errorf("socket %s is already in use by another daemon", e.Address)
		}
		if err := os.Remove(e.Address); err != nil {
			return nil, fmt.Errorf("removing stale socket: %w", err)
		}
	}

	// Create the socket with owner-only permissions from the first instant
	// rather than relying on a chmod after bind(), which would leave a window
	// in which the umask-derived mode applies. The umask is process-wide, but
	// the daemon is single-purpose and restores it immediately.
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", e.Address)
	syscall.Umask(old)
	if err != nil {
		if hint := socketPathLengthHint(e.Address); hint != "" {
			return nil, fmt.Errorf("listen on unix socket %s: %w (%s)", e.Address, err, hint)
		}
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(e.Address, 0o600); err != nil {
		// Best-effort release of the listener; the chmod failure returned
		// below is the actionable error.
		_ = ln.Close()
		return nil, fmt.Errorf("restricting socket permissions: %w", err)
	}
	return ln, nil
}

// checkPrivateDir verifies that dir belongs to the current user and grants
// no group or other permissions. MkdirAll only applies the requested mode to
// directories it creates, so a pre-existing shared directory such as /tmp
// would otherwise silently weaken the socket's protection.
func checkPrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("checking socket directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("socket directory %s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("socket directory %s is mode %04o; it must be 0700 so other users cannot reach the socket", dir, perm)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("socket directory %s is owned by uid %d, not the current user", dir, st.Uid)
	}
	return nil
}

// socketPathHintLength is the path length above which an error carries the
// sun_path hint. It sits a few bytes under the macOS limit of about 104 so a
// path that is borderline once the terminating byte is counted is still caught.
const socketPathHintLength = 100

// socketPathLengthHint returns advice about the operating-system limit on
// Unix socket path length when path is long enough to be the likely cause of a
// failure, and the empty string otherwise.
func socketPathLengthHint(path string) string {
	if len(path) <= socketPathHintLength {
		return ""
	}
	return "socket paths are limited to about 104 bytes on macOS; choose a shorter path"
}
