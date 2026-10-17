package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
)

// This file holds the shared harness for the cmd package tests. Every helper
// restores the process-wide state it touches (arguments, standard streams,
// PATH, the default HTTP transport, the umask, the build version) through
// t.Cleanup, so one test can never leak configuration into the next.
//
// Helpers that mutate process-wide state are incompatible with t.Parallel.
// Tests that use them must run serially, which t.Setenv already enforces for
// the environment-touching ones.

// setArgs replaces os.Args for the duration of the test. The cmd entry points
// read their subcommand and flags straight from os.Args, so tests drive them
// by setting it to the argv the operator would have typed.
func setArgs(t *testing.T, args ...string) {
	t.Helper()
	saved := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = saved })
}

// captureStdio redirects os.Stdout and os.Stderr into pipes for the duration
// of the test and returns a function that stops capturing and reports what
// was written to each stream. Both pipes are drained by goroutines while the
// code under test runs; without that, output beyond the operating system pipe
// buffer (typically 64 KB) would block the writer and deadlock the test.
//
// The returned read function is idempotent: the first call flushes and
// restores the streams, later calls return the same captured text. The
// original streams are also restored on cleanup if read is never called.
func captureStdio(t *testing.T) (read func() (stdout, stderr string)) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	var outBuf, errBuf bytes.Buffer
	var drained sync.WaitGroup
	drained.Add(2)
	go func() {
		defer drained.Done()
		_, _ = io.Copy(&outBuf, outR)
	}()
	go func() {
		defer drained.Done()
		_, _ = io.Copy(&errBuf, errR)
	}()

	var once sync.Once
	finish := func() {
		once.Do(func() {
			os.Stdout, os.Stderr = origOut, origErr
			// Closing the write ends lets the drain goroutines reach EOF.
			_ = outW.Close()
			_ = errW.Close()
			drained.Wait()
			_ = outR.Close()
			_ = errR.Close()
		})
	}
	t.Cleanup(finish)

	return func() (string, string) {
		finish()
		return outBuf.String(), errBuf.String()
	}
}

// tempHome points HOME at a fresh temp directory and clears the variables
// that would otherwise relocate caches away from it.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	return home
}

// requireExitStatus fails unless err is an ExitStatus carrying want.
func requireExitStatus(t *testing.T, err error, want int) {
	t.Helper()
	var status ExitStatus
	if !errors.As(err, &status) {
		t.Fatalf("error = %v (%T), want ExitStatus(%d)", err, err, want)
	}
	if int(status) != want {
		t.Fatalf("exit status = %d, want %d", int(status), want)
	}
}
