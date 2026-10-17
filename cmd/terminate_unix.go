//go:build !windows

package cmd

import "syscall"

// terminateProcess asks the process to shut down cleanly with SIGTERM, which
// the launchd-supervised daemon answers by exiting so it is respawned from
// the updated binary.
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
