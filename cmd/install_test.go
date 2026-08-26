package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The shim records the absolute path of op-forward when it is installed. If
// the binary later moves (for example a package upgrade that relocates it
// from /usr/local/bin to /usr/bin), the shim must find op-forward on PATH
// instead of silently falling through to the real op binary.
func TestShimFallsBackToOpForwardOnPath(t *testing.T) {
	dir := t.TempDir()

	// A stand-in op-forward that records how it was invoked.
	marker := filepath.Join(dir, "invoked")
	fake := filepath.Join(dir, "op-forward")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" > "+marker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	shim := renderShim(filepath.Join(dir, "does-not-exist", "op-forward"), "")
	shimPath := filepath.Join(dir, "op")
	if err := os.WriteFile(shimPath, []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(shimPath, "item", "get", "x")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shim failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("op-forward on PATH was not invoked: %v", err)
	}
	if strings.TrimSpace(string(got)) != "proxy -- item get x" {
		t.Fatalf("op-forward invoked with %q, want %q", strings.TrimSpace(string(got)), "proxy -- item get x")
	}
}
