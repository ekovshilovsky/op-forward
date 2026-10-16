package executor

import "testing"

// A requested timeout above MaxTimeout must be clamped rather than honored,
// so a client cannot hold a daemon execution slot for an arbitrary duration.
// The fake op exits immediately; the assertion is that the oversized request
// is accepted and completes normally under the clamped limit.
func TestExecuteClampsTimeoutAboveMaximum(t *testing.T) {
	installFakeOp(t, 0o755, "#!/bin/sh\nexit 0\n")

	req := &Request{
		Args:      []string{"item", "get", "x"},
		TimeoutMs: int(MaxTimeout.Milliseconds()) * 2,
	}
	res, err := Execute(req)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}
