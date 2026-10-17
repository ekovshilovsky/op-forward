//go:build darwin || linux

package executor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeOp writes an executable named "op" containing script into a fresh
// directory and points PATH exclusively at that directory, so Execute resolves
// the stub and nothing else. The PATH change is process-wide, so tests using
// this helper must not run in parallel.
// sleepPath resolves the sleep binary on the test process's PATH. The fake
// op scripts run with PATH reduced to their own directory, and a hardcoded
// /bin/sleep does not exist in every build environment (the Nix sandbox and
// NixOS have no /bin beyond sh), so the fixture embeds the resolved path.
func sleepPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary on PATH: %v", err)
	}
	return p
}

func installFakeOp(t *testing.T, mode os.FileMode, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte(script), mode); err != nil {
		t.Fatalf("writing fake op: %v", err)
	}
	// WriteFile applies the umask and ignores the mode for pre-existing files,
	// so set the final mode explicitly.
	if err := os.Chmod(filepath.Join(dir, "op"), mode); err != nil {
		t.Fatalf("chmod fake op: %v", err)
	}
	t.Setenv("PATH", dir)
	return dir
}

// ---------- Execute through a PATH-stubbed op ----------

func TestExecute_RelaysStdoutAndExitCode(t *testing.T) {
	// The brief sleep keeps the child from writing before the parent has
	// cleared OPOST on the PTY; see the report for why that ordering matters.
	installFakeOp(t, 0o755, "#!/bin/sh\n"+sleepPath(t)+" 0.2\necho a\necho b\nexit 3\n")

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	// Exact comparison: any carriage return means OPOST was not disabled and
	// the relay contract (raw bytes from op) is broken.
	if res.Stdout != "a\nb\n" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "a\nb\n")
	}
	if res.Stderr != "" {
		t.Errorf("Stderr = %q, want empty because the PTY merges streams into stdout", res.Stderr)
	}
}

// A file named op without execute permission never reaches pty.Start:
// exec.LookPath rejects it first, so the failure surfaces as op-not-found.
func TestExecute_NonExecutableOpIsReportedAsNotFound(t *testing.T) {
	installFakeOp(t, 0o644, "#!/bin/sh\necho unreachable\n")

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err == nil || !strings.Contains(err.Error(), "op CLI not found in PATH") {
		t.Fatalf("Execute() error = %v, want the op-not-found error", err)
	}
	if res != nil {
		t.Errorf("Execute() result = %+v, want nil alongside the error", res)
	}
}

// An executable whose interpreter does not exist passes LookPath but fails at
// exec time, which is the reachable way to hit the pty start error.
func TestExecute_MissingInterpreterFailsToStart(t *testing.T) {
	installFakeOp(t, 0o755, "#!/nonexistent/op-forward-interpreter\n")

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err == nil || !strings.Contains(err.Error(), "starting op with pty") {
		t.Fatalf("Execute() error = %v, want the pty start error", err)
	}
	if res != nil {
		t.Errorf("Execute() result = %+v, want nil alongside the error", res)
	}
}

// ---------- disableOutputPostProcessing ----------

// On a descriptor that is not a terminal the terminal-attributes ioctl fails
// with ENOTTY, and the function must return quietly without touching the file.
func TestDisableOutputPostProcessing_NonTerminalIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	disableOutputPostProcessing(f)

	got, err := os.ReadFile(path)
	if err != nil || string(got) != "payload" {
		t.Fatalf("regular file changed: err=%v content=%q", err, got)
	}
}

// ---------- resolved-path validation ----------

// On unix the real validator accepts any path: the program is executed
// directly and no shell re-parses its arguments.
func TestValidateResolvedPathIsNoOpOnUnix(t *testing.T) {
	for _, path := range []string{"/usr/local/bin/op", "/tmp/op.cmd", "/tmp/op.bat", "op"} {
		if err := ValidateResolvedPath(path); err != nil {
			t.Errorf("ValidateResolvedPath(%q) = %v, want nil on unix", path, err)
		}
	}
}

// A path the validator refuses must stop Execute before anything is started:
// the fake op would leave a marker file if it ran at all.
func TestExecuteRefusedPathNeverStartsOp(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	installFakeOp(t, 0o755, "#!/bin/sh\ntouch '"+marker+"'\nexit 0\n")
	refusal := &NotNativeExecutableError{Name: "op.cmd", Ext: ".cmd"}

	res, err := execute(&Request{Args: []string{"whoami"}}, func(string) error { return refusal })
	if !errors.Is(err, refusal) {
		t.Fatalf("execute() error = %v, want the validator's refusal", err)
	}
	if res != nil {
		t.Errorf("execute() result = %+v, want nil alongside the error", res)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("refused op was started anyway (stat err: %v)", statErr)
	}
}

// The validator receives the path LookPath resolved, so it judges exactly the
// file that would be executed.
func TestExecutePassesResolvedPathToValidator(t *testing.T) {
	dir := installFakeOp(t, 0o755, "#!/bin/sh\nexit 0\n")
	var got string

	res, err := execute(&Request{Args: []string{"whoami"}}, func(p string) error { got = p; return nil })
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("execute() = %+v, %v", res, err)
	}
	if want := filepath.Join(dir, "op"); got != want {
		t.Errorf("validator saw %q, want the resolved path %q", got, want)
	}
}
