//go:build darwin

package executor

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// newSysProcAttr returns process attributes that create a new session and
// set the PTY as the controlling terminal. This isolates each op invocation
// so the PTY does not become the controlling terminal for the daemon itself.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
}

// disableOutputPostProcessing clears the OPOST flag on the PTY so the line
// discipline does not convert \n to \r\n. This preserves raw output and
// avoids fragile string replacement that could corrupt binary data.
func disableOutputPostProcessing(ptmx *os.File) {
	termios, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TIOCGETA)
	if err != nil {
		return
	}
	termios.Oflag &^= unix.OPOST
	_ = unix.IoctlSetTermios(int(ptmx.Fd()), unix.TIOCSETA, termios)
}
