package endpoint

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Listen opens a listener for the endpoint, applying the safety rules each
// transport needs.
//
// TCP endpoints must be loopback. The daemon executes 1Password commands
// with the host user's session, so it is never allowed to be reachable from
// the network; the only way to reach it remotely is through an SSH tunnel or
// Docker Desktop's host.docker.internal route, both of which terminate on
// loopback.
//
// Unix endpoints require a parent directory that is owned by the current
// user and closed to everyone else (mode 0700), and the socket itself is
// created 0600, so other users on the machine cannot connect at all,
// independent of the bearer token check. A socket file left behind by a
// daemon that exited uncleanly is replaced; a socket with a live listener is
// left alone so two daemons cannot silently fight over the same path; and
// anything that is not a socket is never touched, so a mistyped path cannot
// destroy an unrelated file.
func (e Endpoint) Listen() (net.Listener, error) {
	switch e.Network {
	case "tcp":
		if !e.IsLoopback() {
			return nil, fmt.Errorf("refusing to bind to non-loopback address: %s (use a loopback IP literal such as 127.0.0.1)", e.Address)
		}
		return net.Listen("tcp", e.Address)
	case "unix":
		return e.listenUnix()
	default:
		return nil, fmt.Errorf("unsupported network %q", e.Network)
	}
}

func (e Endpoint) listenUnix() (net.Listener, error) {
	dir := filepath.Dir(e.Address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating socket directory: %w", err)
	}
	if err := checkPrivateDir(dir); err != nil {
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
		if len(e.Address) > 100 {
			return nil, fmt.Errorf("listen on unix socket %s: %w (socket paths are limited to about 104 bytes on macOS; choose a shorter path)", e.Address, err)
		}
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(e.Address, 0o600); err != nil {
		ln.Close()
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

// Probe checks that something is accepting connections at the endpoint. The
// proxy uses this before building a request so a missing tunnel is reported
// quickly and the shim can fall back to the local op binary.
func (e Endpoint) Probe(timeout time.Duration) error {
	conn, err := net.DialTimeout(e.Network, e.Address, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

// HTTPClient returns a client whose connections go to this endpoint
// regardless of the host named in the request URL. For TCP the default
// transport is used; for Unix sockets a custom dialer ignores the request
// host (see BaseURL) and connects to the socket path.
func (e Endpoint) HTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if e.Network == "unix" {
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", e.Address)
			},
		}
	}
	return client
}
