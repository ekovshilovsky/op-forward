package endpoint

import "net"

// PeerUID returns the effective user ID of the process on the other end of a
// Unix socket connection, as reported by the kernel. The second result is
// false when the connection is not a Unix socket or the platform does not
// expose peer credentials; callers should treat that as "unknown" rather
// than as a failed check, because the bearer token remains the primary gate.
//
// This is defense in depth on top of the socket's 0600 mode: even if the
// socket file is exposed with looser permissions (a shared directory, a
// misconfigured sshd StreamLocalBindMask), a connection from another user is
// still refused.
func PeerUID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	uid, ok := -1, false
	if err := raw.Control(func(fd uintptr) { uid, ok = peerUID(fd) }); err != nil {
		return 0, false
	}
	return uid, ok
}
