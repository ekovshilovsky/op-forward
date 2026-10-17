//go:build darwin || linux

package executor

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestPTY_StdoutIsTerminal(t *testing.T) {
	// Verify that pty.Start allocates a full PTY — the subprocess should
	// see stdout as a terminal. This is the mechanism that makes op CLI
	// treat each invocation as interactive and trigger biometric auth.
	//
	// Uses "test -t 1" which exits 0 if stdout is a TTY, 1 otherwise.
	cmd := exec.Command("/bin/sh", "-c", "test -t 1 && echo tty || echo not-tty")
	cmd.SysProcAttr = newSysProcAttr()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start() failed: %v", err)
	}

	disableOutputPostProcessing(ptmx)

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(ptmx)
	_ = ptmx.Close()
	_ = cmd.Wait()

	output := strings.TrimSpace(buf.String())
	if output != "tty" {
		t.Errorf("stdout inside PTY should be a terminal, got %q", output)
	}
}

func TestPTY_StdinIsTerminal(t *testing.T) {
	// Verify stdin is also a TTY inside the PTY — some tools check stdin
	// for interactive detection rather than stdout.
	cmd := exec.Command("/bin/sh", "-c", "test -t 0 && echo tty || echo not-tty")
	cmd.SysProcAttr = newSysProcAttr()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start() failed: %v", err)
	}

	disableOutputPostProcessing(ptmx)

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(ptmx)
	_ = ptmx.Close()
	_ = cmd.Wait()

	output := strings.TrimSpace(buf.String())
	if output != "tty" {
		t.Errorf("stdin inside PTY should be a terminal, got %q", output)
	}
}

func TestDisableOutputPostProcessing_NoCarriageReturn(t *testing.T) {
	// Verify that disableOutputPostProcessing prevents the PTY line
	// discipline from converting \n to \r\n. Without this, output from
	// op would contain \r\n which could corrupt JSON or binary data.
	//
	// Open the PTY pair manually and disable OPOST before starting the
	// command so that no output is produced with the default line discipline.
	ptmx, pts, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open() failed: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	disableOutputPostProcessing(ptmx)

	cmd := exec.Command("/bin/sh", "-c", "printf 'line1\nline2\n'")
	cmd.SysProcAttr = newSysProcAttr()
	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	if err := cmd.Start(); err != nil {
		_ = pts.Close()
		t.Fatalf("cmd.Start() failed: %v", err)
	}
	_ = pts.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(ptmx)
	_ = cmd.Wait()

	output := buf.String()
	if strings.Contains(output, "\r\n") {
		t.Errorf("output should not contain \\r\\n after disabling OPOST, got %q", output)
	}
	if !strings.Contains(output, "\n") {
		t.Errorf("output should still contain \\n, got %q", output)
	}
}

func TestPTY_WithoutOPOST_HasCarriageReturn(t *testing.T) {
	// Verify that WITHOUT disableOutputPostProcessing, the PTY does add
	// \r\n — proving the fix is necessary.
	cmd := exec.Command("/bin/sh", "-c", "printf 'hello\n'")
	cmd.SysProcAttr = newSysProcAttr()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start() failed: %v", err)
	}

	// Intentionally NOT calling disableOutputPostProcessing

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(ptmx)
	_ = ptmx.Close()
	_ = cmd.Wait()

	output := buf.String()
	if !strings.Contains(output, "\r\n") {
		t.Errorf("PTY without OPOST fix should produce \\r\\n, got %q", output)
	}
}
