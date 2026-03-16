//go:build linux

package executor

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
}

func disableOutputPostProcessing(ptmx *os.File) {
	termios, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TCGETS)
	if err != nil {
		return
	}
	termios.Oflag &^= unix.OPOST
	_ = unix.IoctlSetTermios(int(ptmx.Fd()), unix.TCSETS, termios)
}
