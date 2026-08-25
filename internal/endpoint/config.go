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

// IntFromEnv reads an integer setting, returning def when the variable is
// unset, not a number, or below min. Every numeric knob in op-forward is a
// port or a duration, for which zero and negative values are never what the
// operator meant, so they fall back to the default instead of being applied.
func IntFromEnv(key string, def, min int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min {
		return def
	}
	return v
}

// PortFromEnv returns OP_FORWARD_PORT, or DefaultPort when unset or invalid.
func PortFromEnv() int {
	return IntFromEnv(EnvPort, DefaultPort, 1)
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

// ForDial resolves the endpoint the proxy connects to. A full endpoint
// string (from --addr or OP_FORWARD_ADDR) wins; otherwise host and port are
// combined into a TCP endpoint.
func ForDial(addr, host string, port int) (Endpoint, error) {
	if addr != "" {
		return Parse(addr)
	}
	return TCP(host, port), nil
}

// ForListen resolves the endpoint the daemon binds. A full endpoint string
// (from --listen or OP_FORWARD_LISTEN) wins; otherwise the daemon binds
// loopback TCP on port. There is intentionally no host parameter: the
// daemon must never be reachable off the machine, so OP_FORWARD_HOST, which
// names where a client dials, is not consulted here.
func ForListen(listen string, port int) (Endpoint, error) {
	if listen != "" {
		return Parse(listen)
	}
	return TCP(DefaultHost, port), nil
}
