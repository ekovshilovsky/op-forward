//go:build linux

package endpoint

import "golang.org/x/sys/unix"

// peerUID reads SO_PEERCRED, which Linux populates for every AF_UNIX
// stream connection with the credentials the peer had at connect() time.
func peerUID(fd uintptr) (int, bool) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, false
	}
	return int(cred.Uid), true
}
