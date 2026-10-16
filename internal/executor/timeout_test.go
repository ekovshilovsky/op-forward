package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A timed-out command must surface the documented timeout contract (exit
// 124 with a clear message), not the raw -1 exit status of the killed
// child, which the proxy would relay and the shell would show as 255.
func TestExecute_TimeoutExitsWith124(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "op")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	res, err := Execute(&Request{Args: []string{"whoami"}, TimeoutMs: 300})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 124 {
		t.Fatalf("ExitCode = %d, want 124 for a timed-out command", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "timed out") {
		t.Fatalf("Stderr = %q, want a timeout message", res.Stderr)
	}
}
