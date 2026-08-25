package cmd

import (
	"os"
	"strconv"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
)

// resolveDialEndpoint picks the daemon address the proxy connects to. An
// explicit endpoint (--addr / OP_FORWARD_ADDR) wins; otherwise the host and
// port shorthand (--host / OP_FORWARD_HOST, --port / OP_FORWARD_PORT) is
// combined into a TCP endpoint.
func resolveDialEndpoint(addr, host string, port int) (endpoint.Endpoint, error) {
	if addr != "" {
		return endpoint.Parse(addr)
	}
	return endpoint.TCP(host, port), nil
}

// resolveListenEndpoint picks the address the daemon binds. An explicit
// endpoint (--listen / OP_FORWARD_LISTEN) wins; otherwise the daemon binds
// loopback TCP on the given port. There is intentionally no host shorthand
// on the listen side: the daemon must not be reachable off the machine.
func resolveListenEndpoint(listen string, port int) (endpoint.Endpoint, error) {
	if listen != "" {
		return endpoint.Parse(listen)
	}
	return endpoint.TCP(endpoint.DefaultHost, port), nil
}

// getProbeTimeoutMs returns how long the proxy waits for the daemon to accept
// a connection before declaring the tunnel down and letting the shim fall
// back to the local op binary. Controlled by OP_FORWARD_PROBE_TIMEOUT_MS.
func getProbeTimeoutMs() int {
	if t := os.Getenv("OP_FORWARD_PROBE_TIMEOUT_MS"); t != "" {
		if ms, err := strconv.Atoi(t); err == nil {
			return ms
		}
	}
	return 500
}

// getProxyTimeout returns the per-request execution timeout in milliseconds,
// controlled by OP_FORWARD_FETCH_TIMEOUT_MS.
func getProxyTimeout() int {
	if t := os.Getenv("OP_FORWARD_FETCH_TIMEOUT_MS"); t != "" {
		if ms, err := strconv.Atoi(t); err == nil {
			return ms
		}
	}
	return 60000
}
