//go:build windows

package cmd

import "errors"

// terminateProcess is unreachable in practice because self-update is refused
// on Windows before any daemon is signalled (see selfUpdateUnsupported). It
// exists so the update workflow compiles, and it fails closed rather than
// forcibly killing a process: Windows has no SIGTERM equivalent for a console
// process without a purpose-built control channel.
func terminateProcess(pid int) error {
	return errors.New("signalling a daemon process is not supported on Windows")
}
