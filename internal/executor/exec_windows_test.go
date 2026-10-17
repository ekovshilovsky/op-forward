//go:build windows

package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests exercise the Windows runner end to end. They cannot run on a
// non-Windows build host; `GOOS=windows go vet` compiles them and a Windows
// machine executes them.
//
// A successful run needs a real op.exe, because the daemon refuses anything
// else (see ValidateResolvedPath). The test binary itself serves as the fake
// op: it is copied to a temporary directory as op.exe, and when the
// environment variable below is set TestMain makes it behave as the fake
// instead of running tests. The variable is inherited by the process Execute
// starts, which is how a test chooses the behavior.

const (
	// fakeOpEnv selects the fake op behavior inside a copied test binary.
	fakeOpEnv = "OP_FORWARD_TEST_FAKE_OP"
	// fakeOpSelfEnv names the original (uncopied) test binary. The "orphan"
	// behavior starts its descendant from that file rather than from the
	// copy in the temporary directory, because a running executable cannot be
	// deleted and the descendant outlives the test; running it from the copy
	// would make the temporary directory impossible to remove.
	fakeOpSelfEnv = "OP_FORWARD_TEST_FAKE_OP_SELF"
	// orphanLifetime keeps the descendant alive well past the 3s WaitDelay
	// and the 15s bound asserted by the timeout test, so the test can tell
	// a bounded wait from one that merely waited for the descendant to exit.
	orphanLifetime = 20 * time.Second
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeOpEnv); mode != "" {
		os.Exit(runFakeOp(mode))
	}
	os.Exit(m.Run())
}

// runFakeOp implements the fake op behaviors and returns the exit status.
func runFakeOp(mode string) int {
	switch mode {
	case "ok":
		_, _ = fmt.Fprintln(os.Stdout, "ok")
		return 0
	case "streams":
		_, _ = fmt.Fprintln(os.Stdout, "to-stdout")
		_, _ = fmt.Fprintln(os.Stderr, "to-stderr")
		return 3
	case "echoargs":
		for _, arg := range os.Args[1:] {
			_, _ = fmt.Fprintln(os.Stdout, arg)
		}
		return 0
	case "sleep":
		time.Sleep(orphanLifetime)
		return 0
	case "orphan":
		// Leave a descendant that keeps the output pipes open and outlives
		// this process, as a real op helper could. Killing this process on a
		// timeout does not kill the descendant.
		child := exec.Command(os.Getenv(fakeOpSelfEnv))
		child.Env = append(os.Environ(), fakeOpEnv+"=sleep")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return 98
		}
		time.Sleep(orphanLifetime + 10*time.Second)
		return 0
	}
	return 97
}

// useDefaultPathExt pins PATHEXT to the Windows default so resolution does
// not depend on the machine running the tests.
func useDefaultPathExt(t *testing.T) {
	t.Helper()
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
}

// installFakeOpExe copies the running test binary to op.exe in a fresh
// directory, points PATH at that directory only, and selects the fake
// behavior. It returns the directory.
func installFakeOpExe(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatalf("opening the test binary: %v", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(filepath.Join(dir, "op.exe"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("creating op.exe: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copying the test binary: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("closing op.exe: %v", err)
	}

	useDefaultPathExt(t)
	t.Setenv("PATH", dir)
	t.Setenv(fakeOpEnv, mode)
	t.Setenv(fakeOpSelfEnv, self)
	return dir
}

// writeBatchOp writes a batch file named name into dir. When it runs it
// creates marker, so a test can prove it never executed.
func writeBatchOp(t *testing.T, dir, name, marker string) {
	t.Helper()
	script := "@echo off\r\necho ran> \"" + marker + "\"\r\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestExecuteWindowsSeparatesStdoutAndStderr(t *testing.T) {
	installFakeOpExe(t, "streams")

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if got := strings.TrimSpace(res.Stdout); got != "to-stdout" {
		t.Errorf("Stdout = %q, want only the stdout line", res.Stdout)
	}
	if got := strings.TrimSpace(res.Stderr); got != "to-stderr" {
		t.Errorf("Stderr = %q, want only the stderr line (streams are separate on Windows)", res.Stderr)
	}
}

func TestExecuteWindowsSuccess(t *testing.T) {
	installFakeOpExe(t, "ok")

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" || res.Stderr != "" {
		t.Errorf("Result = %+v, want exit 0 with stdout ok and empty stderr", res)
	}
}

// With a native executable no shell parses the arguments, so the characters
// cmd.exe treats specially (which Request.Validate deliberately allows,
// because item titles legitimately contain them) must arrive verbatim.
func TestExecuteWindowsArgumentsReachOpVerbatim(t *testing.T) {
	installFakeOpExe(t, "echoargs")
	args := []string{"item", "get", "a%PATH%b", "x^y", "(parens)", "<redirect>", "bang!", "100%"}

	res, err := Execute(&Request{Args: args})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	got := strings.Split(strings.TrimRight(res.Stdout, "\r\n"), "\n")
	for i := range got {
		got[i] = strings.TrimRight(got[i], "\r")
	}
	if strings.Join(got, "\x00") != strings.Join(args, "\x00") {
		t.Errorf("op received %q, want the arguments unchanged: %q", got, args)
	}
}

// The timeout contract must hold on Windows even when a descendant of op
// keeps the output pipes open after op is killed. WaitDelay is what bounds
// the wait; without it this test would block until the descendant exits.
func TestExecuteWindowsTimeoutExitsWith124(t *testing.T) {
	installFakeOpExe(t, "orphan")

	start := time.Now()
	res, err := Execute(&Request{Args: []string{"whoami"}, TimeoutMs: 500})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 124 {
		t.Fatalf("ExitCode = %d, want 124 for a timed-out command", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "timed out") {
		t.Fatalf("Stderr = %q, want a timeout message", res.Stderr)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("Execute() took %v after a 500ms timeout; the wait was not bounded", elapsed)
	}
}

// A batch file resolved as op is refused before anything runs: cmd.exe would
// re-parse the arguments. The marker proves the file never executed.
func TestExecuteWindowsRefusesBatchOp(t *testing.T) {
	for _, name := range []string{"op.cmd", "op.bat", "op.CMD"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "ran")
			writeBatchOp(t, dir, name, marker)
			useDefaultPathExt(t)
			t.Setenv("PATH", dir)

			res, err := Execute(&Request{Args: []string{"item", "get", "x"}})
			var refusal *NotNativeExecutableError
			if !errors.As(err, &refusal) {
				t.Fatalf("Execute() error = %v, want *NotNativeExecutableError", err)
			}
			if !strings.EqualFold(refusal.Name, name) || !strings.EqualFold(refusal.Ext, filepath.Ext(name)) {
				t.Errorf("refusal = %+v, want it to name %s", refusal, name)
			}
			for _, fragment := range []string{"op.exe", "cmd.exe", "refuses"} {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("error = %q, want it to contain %q", err, fragment)
				}
			}
			if res != nil {
				t.Errorf("Execute() result = %+v, want nil alongside the error", res)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Errorf("the batch file ran anyway (stat err: %v)", statErr)
			}
		})
	}
}

// With the default PATHEXT order a native op.exe wins over a batch file in
// the same directory, so installing a helper op.cmd beside the real CLI does
// not disable forwarding.
func TestExecuteWindowsPrefersExeOverBatchInSameDirectory(t *testing.T) {
	dir := installFakeOpExe(t, "ok")
	marker := filepath.Join(dir, "ran")
	writeBatchOp(t, dir, "op.cmd", marker)

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" {
		t.Errorf("Result = %+v, want the native op.exe to run", res)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("the batch file ran although op.exe was present (stat err: %v)", statErr)
	}
}

// The real Windows validator accepts exactly the native executable form.
func TestValidateResolvedPathOnWindows(t *testing.T) {
	if err := ValidateResolvedPath(`C:\Tools\op.exe`); err != nil {
		t.Errorf("ValidateResolvedPath(op.exe) = %v, want nil", err)
	}
	if err := ValidateResolvedPath(`C:\Tools\op.cmd`); err == nil {
		t.Error("ValidateResolvedPath(op.cmd) = nil, want a refusal")
	}
}
