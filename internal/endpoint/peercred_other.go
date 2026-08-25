//go:build !linux && !darwin

package endpoint

// peerUID reports no credentials on platforms without a peer-credential
// socket option; the bearer token check still applies.
func peerUID(fd uintptr) (int, bool) { return 0, false }
