package endpoint

import (
	"context"
	"fmt"
	"net"
	"net/http"
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
