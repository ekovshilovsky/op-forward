//go:build !windows

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekovshilovsky/op-forward/internal/opcheck"
)

// The shim records the version of the binary that installed it, so doctor can
// tell a shim written by an older release from a current one.
func TestRenderShimStampsTheInstallingVersion(t *testing.T) {
	withVersion(t, "3.4.5-rc1")

	shim := renderShim("/opt/op-forward", "/usr/bin/op")

	if want := "\n# op-forward-shim-version: 3.4.5-rc1\n"; !strings.Contains(shim, want) {
		t.Errorf("shim does not carry the version marker %q:\n%s", want, shim)
	}
	if strings.Contains(shim, "{{") {
		t.Error("shim still contains an unreplaced template placeholder")
	}
	if !strings.HasPrefix(shim, "#!/bin/sh\n") {
		t.Errorf("shim must stay POSIX sh, starts with %q", strings.SplitN(shim, "\n", 2)[0])
	}
	// opcheck recognizes the shim by this signature; losing it would make a
	// client's own shim look like the real op.
	if !strings.Contains(shim, opcheck.ShimSignature) {
		t.Errorf("shim header no longer contains the signature %q", opcheck.ShimSignature)
	}
}

// A version containing a newline or other shell-significant text must not be
// able to end the comment line and add commands to the shim.
func TestShimVersionStampCannotInjectShellCode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1.2.3", "1.2.3"},
		{"1.2.3-rc1+dirty", "1.2.3-rc1+dirty"},
		{"dev", "dev"},
		{"1.0\nrm -rf /", "1.0_rm_-rf__"},
		{"a b;c$(d)", "a_b_c__d_"},
	}
	for _, tt := range tests {
		if got := shimVersionStamp(tt.in); got != tt.want {
			t.Errorf("shimVersionStamp(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	withVersion(t, "1.0\nrm -rf /")
	shim := renderShim("/opt/op-forward", "/usr/bin/op")
	for _, line := range strings.Split(shim, "\n") {
		if strings.Contains(line, "rm -rf") && !strings.HasPrefix(line, "#") {
			t.Errorf("version leaked into executable shim code: %q", line)
		}
	}
	// The stamp round-trips through the parser used by doctor.
	if got, ok := parseShimVersion(shim); !ok || got != shimVersionStamp(Version) {
		t.Errorf("parseShimVersion = %q, %v; want the sanitized stamp", got, ok)
	}
}

func TestParseShimVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		ok      bool
	}{
		{"stamped shim", "#!/bin/sh\n# op-forward shim\n# op-forward-shim-version: 1.2.3\n\nREAL_OP=x\n", "1.2.3", true},
		{"surrounding whitespace is trimmed", "#!/bin/sh\n# op-forward-shim-version:   1.2.3  \n", "1.2.3", true},
		{"shim from before stamping", "#!/bin/bash\n# op-forward shim\n\nREAL_OP=x\n", "", false},
		{"marker text after code is not a marker", "#!/bin/sh\nREAL_OP=x\n# op-forward-shim-version: 9.9.9\n", "", false},
		{"empty file", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseShimVersion(tt.content)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseShimVersion() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// The version comment must not disturb the shim's exit-code contract: 127 from
// the proxy means infrastructure failure and runs the real op; any other
// status, success or failure, is relayed unchanged.
func TestStampedShimKeepsItsExitCodeContract(t *testing.T) {
	tests := []struct {
		name      string
		proxyExit string
		wantExit  int
		wantReal  bool
	}{
		{"success is relayed", "0", 0, false},
		{"op failure is relayed", "1", 1, false},
		{"another op status is relayed", "7", 7, false},
		{"timeout status is relayed", "124", 124, false},
		{"infrastructure failure falls back to the real op", "127", 23, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			proxy := filepath.Join(dir, "op-forward")
			writeFile(t, proxy, "#!/bin/sh\nexit "+tt.proxyExit+"\n", 0o755)
			realOp := filepath.Join(dir, "real-op")
			marker := filepath.Join(dir, "real-op-ran")
			writeFile(t, realOp, "#!/bin/sh\ntouch '"+marker+"'\nexit 23\n", 0o755)
			shimPath := filepath.Join(dir, "op")
			writeFile(t, shimPath, renderShim(proxy, realOp), 0o755)

			err := exec.Command(shimPath, "item", "list").Run()

			code := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("running shim: %v", err)
			}
			if code != tt.wantExit {
				t.Errorf("shim exit = %d, want %d", code, tt.wantExit)
			}
			if _, statErr := os.Stat(marker); (statErr == nil) != tt.wantReal {
				t.Errorf("real op ran = %v, want %v", statErr == nil, tt.wantReal)
			}
		})
	}
}
