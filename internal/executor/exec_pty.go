//go:build darwin || linux

package executor

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/creack/pty"
)

// runCommand runs cmd inside a full pseudo-TTY and returns its output.
//
// Running op inside a PTY lets it detect an interactive session on all file
// descriptors (stdin, stdout, stderr). Without a TTY, 1Password caches the
// biometric approval and skips Touch ID on subsequent calls. With a full
// PTY, each invocation triggers a fresh biometric prompt, matching the
// behavior of running op directly in a terminal.
//
// Trade-off: the PTY merges stdout and stderr into a single stream. The
// combined output is returned as stdout and stderr is always empty.
//
// The returned error is the raw result of cmd.Wait (an *exec.ExitError for a
// non-zero or signalled exit) or a *startError when op could not be started;
// the caller owns deadline and exit-status interpretation.
func runCommand(cmd *exec.Cmd) (stdout, stderr string, err error) {
	cmd.SysProcAttr = newSysProcAttr()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", "", &startError{err: fmt.Errorf("starting op with pty: %w", err)}
	}

	// Disable the PTY's output post-processing (OPOST) so the line discipline
	// does not convert \n to \r\n. This preserves the raw output from op and
	// avoids fragile string replacement that could corrupt binary data or
	// fields containing legitimate carriage returns. Platform-specific because
	// macOS uses TIOCGETA/TIOCSETA and Linux uses TCGETS/TCSETS.
	disableOutputPostProcessing(ptmx)

	// Read all output from the PTY master.
	var output bytes.Buffer
	_, _ = output.ReadFrom(ptmx)
	_ = ptmx.Close()

	return output.String(), "", cmd.Wait()
}
