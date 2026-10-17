package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test. It is the child half of exitErrorWithCode:
// when the helper environment variable is set, the test binary exits with the
// requested status, producing a genuine *exec.ExitError on every platform
// without depending on a shell or a particular external binary.
func TestHelperProcess(t *testing.T) {
	raw := os.Getenv("OP_FORWARD_TEST_HELPER_EXIT")
	if raw == "" {
		return
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		os.Exit(99)
	}
	os.Exit(code)
}

// exitErrorWithCode returns the error cmd.Wait produces for a child that
// exited with the given non-zero status.
func exitErrorWithCode(t *testing.T, code int) error {
	t.Helper()
	// #nosec G204 -- the command is this test binary re-executed with a fixed flag
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "OP_FORWARD_TEST_HELPER_EXIT="+strconv.Itoa(code))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper process error = %v, want *exec.ExitError", err)
	}
	return err
}

func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}

// The deadline must win over the exit status of the killed child: a command
// the context killed surfaces as an ExitError, and the 124 contract is only
// reachable when the deadline is consulted first. This is the shared path
// every platform runs, so it is verified independently of any runner.
func TestInterpretRunDeadlineWinsOverExitError(t *testing.T) {
	res, err := interpretRun(expiredContext(t), "partial", "partial-err", exitErrorWithCode(t, 3))
	if err != nil {
		t.Fatalf("interpretRun() error: %v", err)
	}
	if res.ExitCode != 124 {
		t.Errorf("ExitCode = %d, want 124", res.ExitCode)
	}
	if res.Stderr != "command timed out" {
		t.Errorf("Stderr = %q, want the timeout message", res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("Stdout = %q, want empty on timeout", res.Stdout)
	}
}

// A cancellation that is not a deadline expiry is not the timeout contract.
func TestInterpretRunCancellationIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := interpretRun(ctx, "", "", exitErrorWithCode(t, 5))
	if err != nil {
		t.Fatalf("interpretRun() error: %v", err)
	}
	if res.ExitCode != 5 {
		t.Errorf("ExitCode = %d, want the relayed status 5", res.ExitCode)
	}
}

func TestInterpretRunRelaysExitCodeAndStreams(t *testing.T) {
	res, err := interpretRun(context.Background(), "out", "err", exitErrorWithCode(t, 7))
	if err != nil {
		t.Fatalf("interpretRun() error: %v", err)
	}
	if res.ExitCode != 7 || res.Stdout != "out" || res.Stderr != "err" {
		t.Errorf("Result = %+v, want exit 7 with stdout %q and stderr %q kept separate", res, "out", "err")
	}
}

func TestInterpretRunSuccess(t *testing.T) {
	res, err := interpretRun(context.Background(), "ok\n", "", nil)
	if err != nil {
		t.Fatalf("interpretRun() error: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "ok\n" || res.Stderr != "" {
		t.Errorf("Result = %+v, want exit 0 with stdout %q", res, "ok\n")
	}
}

// A launch failure is an infrastructure error and is reported verbatim, even
// when the deadline has also passed: op never ran, so there is nothing to
// time out.
func TestInterpretRunStartFailureIsInfrastructureError(t *testing.T) {
	cause := errors.New("exec format error")
	runErr := &startError{err: errors.New("starting op: " + cause.Error())}

	res, err := interpretRun(expiredContext(t), "", "", runErr)
	if res != nil {
		t.Errorf("Result = %+v, want nil alongside the error", res)
	}
	if err == nil || err.Error() != "starting op: exec format error" {
		t.Fatalf("error = %v, want the start error verbatim", err)
	}
}

func TestInterpretRunWaitFailureIsInfrastructureError(t *testing.T) {
	res, err := interpretRun(context.Background(), "", "", errors.New("pipe closed"))
	if res != nil {
		t.Errorf("Result = %+v, want nil alongside the error", res)
	}
	if err == nil || !strings.Contains(err.Error(), "executing op: pipe closed") {
		t.Fatalf("error = %v, want the wrapped execution error", err)
	}
}

// startError must behave as an ordinary error that exposes its cause, so
// callers using errors.Is or errors.As on a launch failure still see the
// underlying operating-system error.
func TestStartErrorExposesItsCause(t *testing.T) {
	cause := os.ErrPermission
	err := error(&startError{err: cause})

	if err.Error() != cause.Error() {
		t.Errorf("Error() = %q, want %q", err.Error(), cause.Error())
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Error("errors.Is(startError, cause) = false, want true through Unwrap")
	}
}
