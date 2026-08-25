package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
// Unix endpoints get a 0700 parent directory and a 0600 socket so that other
// users on the machine cannot connect at all, independent of the bearer token
// check. A socket file left behind by a daemon that exited uncleanly is
// replaced, but a socket that still has a live listener is left alone so two
// daemons cannot silently fight over the same path.
func (e Endpoint) Listen() (net.Listener, error) {
	switch e.Network {
	case "tcp":
		if !e.IsLoopback() {
			return nil, fmt.Errorf("refusing to bind to non-loopback address: %s", e.Address)
		}
		return net.Listen("tcp", e.Address)
	case "unix":
		return e.listenUnix()
	default:
		return nil, fmt.Errorf("unsupported network %q", e.Network)
	}
}

func (e Endpoint) listenUnix() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(e.Address), 0o700); err != nil {
		return nil, fmt.Errorf("creating socket directory: %w", err)
	}
	if _, err := os.Stat(e.Address); err == nil {
		if e.Probe(200*time.Millisecond) == nil {
			return nil, fmt.Errorf("socket %s is already in use by another daemon", e.Address)
		}
		if err := os.Remove(e.Address); err != nil {
			return nil, fmt.Errorf("removing stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", e.Address)
	if err != nil {
		if errors.Is(err, os.ErrInvalid) || len(e.Address) > 100 {
			return nil, fmt.Errorf("listen on unix socket %s: %w (socket paths are limited to roughly 100 bytes on macOS)", e.Address, err)
		}
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(e.Address, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("restricting socket permissions: %w", err)
	}
	return ln, nil
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
