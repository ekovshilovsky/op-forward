//go:build windows

package executor

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// waitDelay bounds how long Wait lingers after op has exited or been killed
// when a descendant process still holds the output pipes open. Without it, a
// timed-out run could block past its deadline and violate the exit-124
// contract, because killing op.exe on Windows does not kill processes it
// started.
const waitDelay = 3 * time.Second

// runCommand runs cmd with ordinary pipes and returns its output.
//
// PLATFORM DIFFERENCES FROM THE UNIX IMPLEMENTATION (exec_pty.go)
//
//   - stdout and stderr are captured separately and returned separately.
//     This is deliberate. The unix implementation merges both streams
//     because a PTY has a single output channel; here no PTY exists, so
//     there is nothing to merge and callers receive op's real stderr.
//
//   - The fresh-prompt-per-call behavior is UNVERIFIED on Windows. On macOS
//     the PTY exists specifically so 1Password treats every invocation as
//     interactive and raises a new Touch ID prompt instead of reusing a
//     cached approval. There is no equivalent here: ConPTY is not supported
//     by github.com/creack/pty, and whether 1Password for Windows decides
//     "interactive" from a console, from the desktop-app integration, or
//     from neither is not known. Whether each op call produces its own
//     Windows Hello prompt, or approval is cached for a period, has not been
//     observed and must be settled by running on a real Windows host.
//
//   - stdin is the null device (os/exec default for a nil Stdin), not a
//     terminal, and op.exe inherits the daemon's console if it has one.
//
// The returned error is the raw result of cmd.Wait (an *exec.ExitError for a
// non-zero or killed exit) or a *startError when op could not be started; the
// caller owns deadline and exit-status interpretation, which keeps the
// exit-124 timeout contract identical on every platform.
func runCommand(cmd *exec.Cmd) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.WaitDelay = waitDelay

	if err := cmd.Start(); err != nil {
		return "", "", &startError{err: fmt.Errorf("starting op: %w", err)}
	}

	err = cmd.Wait()
	// ErrWaitDelay means op itself exited successfully but a descendant kept
	// the pipes open past waitDelay. The output captured up to that point is
	// complete for op's own writes, so it is not an execution failure.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return outBuf.String(), errBuf.String(), err
}
