//go:build !windows

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/cmd"
)

// setArgs replaces os.Args for the duration of the test; the commands read
// their subcommand and flags from it.
func setArgs(t *testing.T, args ...string) {
	t.Helper()
	saved := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = saved })
}

// redirectStdio points os.Stdout and os.Stderr at files for the duration of
// the test and returns a function that restores them and reports what was
// written to each. Files rather than pipes keep the helper free of drain
// goroutines: the volume written by these commands is small and a file never
// blocks the writer.
func redirectStdio(t *testing.T) (read func() (stdout, stderr string)) {
	t.Helper()

	dir := t.TempDir()
	outPath, errPath := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	restore := func() { os.Stdout, os.Stderr = origOut, origErr }
	t.Cleanup(func() {
		restore()
		_ = outFile.Close()
		_ = errFile.Close()
	})

	return func() (string, string) {
		restore()
		out, outErr := os.ReadFile(outPath)
		if outErr != nil {
			t.Fatal(outErr)
		}
		errOut, errErr := os.ReadFile(errPath)
		if errErr != nil {
			t.Fatal(errErr)
		}
		return string(out), string(errOut)
	}
}

// proxyEnv isolates the proxy from the developer's environment and points it
// at endpoint, with a valid access token cached in a private token directory.
func proxyEnv(t *testing.T, endpoint string) {
	t.Helper()

	tokenDir := filepath.Join(t.TempDir(), "tokens")
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(tokenDir, "access.token"), []byte("tok\n"+expiry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OP_FORWARD_TOKEN_DIR", tokenDir)
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv("OP_FORWARD_ADDR", endpoint)
	t.Setenv("OP_FORWARD_HOST", "")
	t.Setenv("OP_FORWARD_PORT", "")
}

// An unknown command is reported twice by design: the dispatcher prints its
// notice and the usage text itself, and run prints the returned error to the
// writer it was given. The exit code is 1.
func TestRunUnknownCommandPrintsErrorAndReturnsOne(t *testing.T) {
	setArgs(t, "op-forward", "frobnicate")
	read := redirectStdio(t)
	var stderr bytes.Buffer

	code := run(&stderr)
	stdout, processStderr := read()

	if code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if got, want := stderr.String(), "unknown command: frobnicate\n"; got != want {
		t.Errorf("writer received %q, want %q", got, want)
	}
	if !strings.Contains(processStderr, "Unknown command: frobnicate") {
		t.Errorf("dispatcher notice missing from stderr: %q", processStderr)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("usage missing from stdout: %q", stdout)
	}
}

// A command that completes without error exits 0 and writes nothing to the
// error writer.
func TestRunReturnsZeroOnSuccess(t *testing.T) {
	setArgs(t, "op-forward", "version")
	read := redirectStdio(t)
	var stderr bytes.Buffer

	code := run(&stderr)
	stdout, _ := read()

	if code != 0 {
		t.Errorf("run() = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("writer received %q, want nothing", stderr.String())
	}
	if want := "op-forward " + cmd.Version + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// A help request ends successfully. The usage text comes from the flag
// package on the process stderr; the writer given to run stays empty because
// an ExitStatus is never printed.
func TestRunHelpFlagReturnsZeroSilently(t *testing.T) {
	setArgs(t, "op-forward", "proxy", "-h")
	read := redirectStdio(t)
	var stderr bytes.Buffer

	code := run(&stderr)
	_, processStderr := read()

	if code != 0 {
		t.Errorf("run() = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("writer received %q, want nothing", stderr.String())
	}
	if !strings.Contains(processStderr, "Usage of proxy:") {
		t.Errorf("usage missing from stderr: %q", processStderr)
	}
}

// A flag error is a usage error: exit code 2, already explained by the flag
// package, so nothing further is printed.
func TestRunUnknownFlagReturnsTwoSilently(t *testing.T) {
	setArgs(t, "op-forward", "proxy", "--bogus")
	read := redirectStdio(t)
	var stderr bytes.Buffer

	code := run(&stderr)
	_, processStderr := read()

	if code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("writer received %q, want nothing", stderr.String())
	}
	if !strings.Contains(processStderr, "flag provided but not defined: -bogus") {
		t.Errorf("flag diagnostic missing from stderr: %q", processStderr)
	}
}

// The forwarded op exit code is the process exit code, and the proxy's own
// output is not duplicated onto the error writer.
func TestRunRelaysForwardedExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want the cached access token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"stdout":"out\n","stderr":"err\n","exit_code":7}`))
	}))
	t.Cleanup(srv.Close)
	proxyEnv(t, "tcp://"+srv.Listener.Addr().String())

	setArgs(t, "op-forward", "proxy", "--", "whoami")
	read := redirectStdio(t)
	var stderr bytes.Buffer

	code := run(&stderr)
	stdout, processStderr := read()

	if code != 7 {
		t.Errorf("run() = %d, want the forwarded exit code 7", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("writer received %q, want nothing", stderr.String())
	}
	if stdout != "out\n" || processStderr != "err\n" {
		t.Errorf("relayed stdout/stderr = %q/%q, want %q/%q", stdout, processStderr, "out\n", "err\n")
	}
}

// Infrastructure failures exit 127, the code the op shim treats as "fall back
// to the real op binary". The diagnostic is printed by the proxy, not by run.
func TestRunReturns127WhenDaemonIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := "tcp://" + srv.Listener.Addr().String()
	srv.Close() // Nothing listens on the address any more.
	proxyEnv(t, endpoint)

	setArgs(t, "op-forward", "proxy", "--", "whoami")
	read := redirectStdio(t)
	var stderr bytes.Buffer

	code := run(&stderr)
	stdout, processStderr := read()

	if code != 127 {
		t.Errorf("run() = %d, want 127", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("writer received %q, want nothing", stderr.String())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if want := "op-forward: daemon not reachable at " + endpoint + "\n"; processStderr != want {
		t.Errorf("stderr = %q, want %q", processStderr, want)
	}
}
