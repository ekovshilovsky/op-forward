// Package endpoint describes where the op-forward daemon listens and where
// the proxy dials, independent of the underlying transport. A single value
// of the form tcp://host:port or unix:///path.sock drives both the listen
// and dial sides, so the HTTP, auth, and JSON layers never need to know
// which transport carries them.
//
// Two transports are supported because different deployment topologies need
// different things. TCP over loopback is what SSH port forwarding and Docker
// Desktop's host.docker.internal route both understand; Unix domain sockets
// give a per-user, kernel-permissioned endpoint on the VM side and avoid port
// collisions when several VMs share a host.
package endpoint

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// Endpoint is a parsed transport address. Network is a value accepted by
// net.Dial and net.Listen ("tcp" or "unix"); Address is the corresponding
// host:port or absolute socket path.
type Endpoint struct {
	Network string
	Address string
}

// Parse converts a tcp://host:port or unix:///absolute/path string into an
// Endpoint. Anything else is rejected so that a typo in an environment
// variable fails loudly at startup instead of silently falling back.
func Parse(raw string) (Endpoint, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return Endpoint{}, fmt.Errorf("endpoint %q must be of the form tcp://host:port or unix:///path", raw)
	}
	switch scheme {
	case "tcp":
		host, port, err := net.SplitHostPort(rest)
		if err != nil {
			return Endpoint{}, fmt.Errorf("endpoint %q: expected tcp://host:port: %w", raw, err)
		}
		if strings.ContainsAny(port, "/?#") || host == "" {
			return Endpoint{}, fmt.Errorf("endpoint %q: expected tcp://host:port", raw)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return Endpoint{}, fmt.Errorf("endpoint %q: port must be numeric", raw)
		}
		return Endpoint{Network: "tcp", Address: net.JoinHostPort(host, port)}, nil
	case "unix":
		if rest == "" {
			return Endpoint{}, fmt.Errorf("endpoint %q: unix socket path is empty", raw)
		}
		if !filepath.IsAbs(rest) {
			return Endpoint{}, fmt.Errorf("endpoint %q: unix socket path must be absolute (unix:///path)", raw)
		}
		return Endpoint{Network: "unix", Address: filepath.Clean(rest)}, nil
	default:
		return Endpoint{}, fmt.Errorf("endpoint %q: unsupported scheme %q (use tcp or unix)", raw, scheme)
	}
}

// TCP builds a TCP endpoint from separate host and port values, which is how
// the legacy OP_FORWARD_HOST / OP_FORWARD_PORT settings are expressed.
func TCP(host string, port int) Endpoint {
	return Endpoint{Network: "tcp", Address: net.JoinHostPort(host, strconv.Itoa(port))}
}

// String renders the endpoint in the same URL form Parse accepts.
func (e Endpoint) String() string {
	return e.Network + "://" + e.Address
}

// BaseURL is the http:// prefix for requests to this endpoint. For Unix
// sockets the host component is a placeholder: the custom dialer in
// HTTPClient ignores it and connects to the socket path instead.
func (e Endpoint) BaseURL() string {
	if e.Network == "unix" {
		return "http://unix"
	}
	return "http://" + e.Address
}

// IsLoopback reports whether the endpoint is reachable only from the local
// machine. Unix sockets are local by construction; TCP endpoints qualify only
// when the host is a loopback address or the literal "localhost".
func (e Endpoint) IsLoopback() bool {
	if e.Network == "unix" {
		return true
	}
	host, _, err := net.SplitHostPort(e.Address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
