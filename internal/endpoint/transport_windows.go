//go:build windows

package endpoint

import "net"

// listenUnix always fails on Windows. The unix transport depends on POSIX
// permission semantics (a 0700 parent directory, a umask-protected 0600
// socket, and kernel-reported peer credentials) that Windows does not
// provide in a form the daemon can verify, so serving a socket there would
// silently weaken the access control the unix transport exists to provide.
// Parse already rejects unix:// endpoints on Windows; this guard covers an
// Endpoint value that was constructed directly.
func (e Endpoint) listenUnix() (net.Listener, error) {
	return nil, errUnixUnsupported
}
