//go:build darwin

package endpoint

import "golang.org/x/sys/unix"

// peerUID reads LOCAL_PEERCRED, the macOS equivalent of Linux's SO_PEERCRED.
func peerUID(fd uintptr) (int, bool) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, false
	}
	return int(cred.Uid), true
}
