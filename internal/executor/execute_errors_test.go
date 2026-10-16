package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeOp writes an executable named "op" containing script into a fresh
// directory and points PATH exclusively at that directory, so Execute resolves
// the stub and nothing else. The PATH change is process-wide, so tests using
// this helper must not run in parallel.
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

// ---------- Validate boundaries ----------

func TestValidate_TooManyArguments(t *testing.T) {
	args := make([]string, MaxArgs+1)
	args[0] = "item"
	for i := 1; i < len(args); i++ {
		args[i] = "x"
	}

	err := (&Request{Args: args}).Validate()
	if err == nil || !strings.Contains(err.Error(), "too many arguments: 65 (max 64)") {
		t.Fatalf("Validate() error = %v, want the 65-argument rejection", err)
	}
}

func TestValidate_ArgumentCountBoundaryIsInclusive(t *testing.T) {
	args := make([]string, MaxArgs)
	args[0] = "item"
	for i := 1; i < len(args); i++ {
		args[i] = "x"
	}

	if err := (&Request{Args: args}).Validate(); err != nil {
		t.Fatalf("Validate() rejected exactly %d arguments: %v", MaxArgs, err)
	}
}

func TestValidate_OversizedArgument(t *testing.T) {
	req := &Request{Args: []string{"item", "get", strings.Repeat("a", MaxArgLength+1)}}

	err := req.Validate()
	if err == nil || !strings.Contains(err.Error(), "argument too long: 4097 bytes (max 4096)") {
		t.Fatalf("Validate() error = %v, want the 4097-byte rejection", err)
	}
}

func TestValidate_ArgumentLengthBoundaryIsInclusive(t *testing.T) {
	req := &Request{Args: []string{"item", "get", strings.Repeat("a", MaxArgLength)}}

	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() rejected a %d-byte argument: %v", MaxArgLength, err)
	}
}

// ---------- Execute through a PATH-stubbed op ----------

func TestExecute_RelaysStdoutAndExitCode(t *testing.T) {
	// The brief sleep keeps the child from writing before the parent has
	// cleared OPOST on the PTY; see the report for why that ordering matters.
	installFakeOp(t, 0o755, "#!/bin/sh\n/bin/sleep 0.2\necho a\necho b\nexit 3\n")

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

func TestExecute_OpNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err == nil || !strings.Contains(err.Error(), "op CLI not found in PATH") {
		t.Fatalf("Execute() error = %v, want the op-not-found error", err)
	}
	if res != nil {
		t.Errorf("Execute() result = %+v, want nil alongside the error", res)
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

func TestExecute_InvalidRequestReturnsResultNotError(t *testing.T) {
	// PATH deliberately holds no op: validation must reject the request
	// before the binary lookup is attempted.
	t.Setenv("PATH", t.TempDir())

	res, err := Execute(&Request{Args: []string{"signin"}})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 1 || !strings.Contains(res.Stderr, "not allowed") {
		t.Fatalf("Execute() = %+v, want exit 1 with a 'not allowed' message", res)
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
