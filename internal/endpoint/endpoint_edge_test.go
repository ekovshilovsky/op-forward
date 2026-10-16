package endpoint

import (
	"strings"
	"testing"
)

// Parse always produces host:port TCP addresses, but Endpoint values can be
// constructed directly, so IsLoopback must refuse an address it cannot split
// rather than treat it as bindable.
func TestIsLoopbackMalformedAddress(t *testing.T) {
	ep := Endpoint{Network: "tcp", Address: "127.0.0.1"}
	if ep.IsLoopback() {
		t.Error("IsLoopback() = true for an address without a port, want false")
	}
}

// Listen dispatches on the network field, and a directly constructed value
// with a network Parse never emits must be rejected by name instead of
// falling through to a nil listener.
func TestListenRejectsUnsupportedNetwork(t *testing.T) {
	ep := Endpoint{Network: "udp", Address: "127.0.0.1:0"}
	ln, err := ep.Listen()
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen() succeeded for network udp, want an error")
	}
	if !strings.Contains(err.Error(), `unsupported network "udp"`) {
		t.Errorf("Listen() error = %v, want the unsupported-network message", err)
	}
}
