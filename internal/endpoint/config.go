package endpoint

import (
	"os"
	"strconv"
)

// DefaultPort is the loopback TCP port used when nothing else is configured.
const DefaultPort = 18340

// DefaultHost is the dial target used when OP_FORWARD_HOST is unset.
const DefaultHost = "127.0.0.1"

// Environment variables. OP_FORWARD_ADDR and OP_FORWARD_LISTEN carry a full
// endpoint (tcp://host:port or unix:///path). OP_FORWARD_HOST and
// OP_FORWARD_PORT predate them and remain supported as shorthand for a TCP
// endpoint; they are consulted only when the full form is unset, so existing
// installs keep working without any change.
const (
	EnvAddr   = "OP_FORWARD_ADDR"
	EnvListen = "OP_FORWARD_LISTEN"
	EnvHost   = "OP_FORWARD_HOST"
	EnvPort   = "OP_FORWARD_PORT"
)

// PortFromEnv returns OP_FORWARD_PORT, or DefaultPort when unset or invalid.
func PortFromEnv() int {
	if p := os.Getenv(EnvPort); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	return DefaultPort
}

// HostFromEnv returns OP_FORWARD_HOST, or DefaultHost when unset. Setting it
// to host.docker.internal lets a Docker Desktop container reach the host
// daemon without an SSH tunnel.
func HostFromEnv() string {
	if h := os.Getenv(EnvHost); h != "" {
		return h
	}
	return DefaultHost
}

// DialFromEnv resolves the endpoint the proxy connects to:
// OP_FORWARD_ADDR if set, otherwise tcp://$OP_FORWARD_HOST:$OP_FORWARD_PORT.
func DialFromEnv() (Endpoint, error) {
	if raw := os.Getenv(EnvAddr); raw != "" {
		return Parse(raw)
	}
	return TCP(HostFromEnv(), PortFromEnv()), nil
}

// ListenFromEnv resolves the endpoint the daemon binds:
// OP_FORWARD_LISTEN if set, otherwise tcp://127.0.0.1:$OP_FORWARD_PORT.
//
// OP_FORWARD_HOST is deliberately not consulted here. It names where a
// client should dial, which may be a Docker Desktop alias for the host; the
// daemon itself must stay on loopback.
func ListenFromEnv() (Endpoint, error) {
	if raw := os.Getenv(EnvListen); raw != "" {
		return Parse(raw)
	}
	return TCP(DefaultHost, PortFromEnv()), nil
}
