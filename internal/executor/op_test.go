package executor

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestValidate_EmptyArgs(t *testing.T) {
	req := &Request{Args: []string{}}
	if err := req.Validate(); err == nil {
		t.Error("Validate() should reject empty args")
	}
}

func TestValidate_BlockedSubcommands(t *testing.T) {
	blocked := []string{"signin", "signout", "update", "completion"}
	for _, cmd := range blocked {
		req := &Request{Args: []string{cmd}}
		err := req.Validate()
		if err == nil {
			t.Errorf("Validate() should block subcommand %q", cmd)
		}
		if err != nil && !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("Validate(%q) error = %q, should mention 'not allowed'", cmd, err)
		}
	}
}

func TestValidate_AllowedSubcommands(t *testing.T) {
	allowed := []string{
		"account", "item", "vault", "document", "whoami",
		"read", "inject", "run", "plugin", "connect",
	}
	for _, cmd := range allowed {
		req := &Request{Args: []string{cmd, "list"}}
		if err := req.Validate(); err != nil {
			t.Errorf("Validate() should allow subcommand %q, got: %v", cmd, err)
		}
	}
}

func TestValidate_ShellMetacharacters(t *testing.T) {
	tests := []struct {
		name string
		arg  string
	}{
		{"backtick", "test`whoami`"},
		{"dollar", "test$(id)"},
		{"pipe", "test|cat /etc/passwd"},
		{"semicolon", "test; rm -rf /"},
		{"ampersand", "test & echo pwned"},
		{"newline", "test\nmalicious"},
		{"carriage return", "test\rmalicious"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Args: []string{"item", "get", tt.arg}}
			err := req.Validate()
			if err == nil {
				t.Errorf("Validate() should reject arg with %s", tt.name)
			}
			if err != nil && !strings.Contains(err.Error(), "disallowed character") {
				t.Errorf("error = %q, should mention 'disallowed character'", err)
			}
		})
	}
}

func TestValidate_LegitimateArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"simple get", []string{"item", "get", "abc123"}},
		{"with fields", []string{"item", "get", "abc123", "--fields", "username"}},
		{"with reveal", []string{"item", "get", "abc123", "--fields", "password", "--reveal"}},
		{"uuid format", []string{"item", "get", "puag4io26exg6xdv2lutur2k7q"}},
		{"vault list", []string{"vault", "list"}},
		{"account list", []string{"account", "list"}},
		{"with format", []string{"item", "list", "--format", "json"}},
		{"with dashes", []string{"item", "get", "my-item-name"}},
		{"with dots", []string{"item", "get", "my.item.name"}},
		{"with slashes", []string{"read", "op://vault/item/field"}},
		{"with equals", []string{"item", "get", "--vault=Personal"}},
		{"with spaces", []string{"item", "get", "My Item Name"}},
		{"with at sign", []string{"item", "get", "user@example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Args: tt.args}
			if err := req.Validate(); err != nil {
				t.Errorf("Validate() rejected legitimate args %v: %v", tt.args, err)
			}
		})
	}
}

func TestValidate_TimeoutClamping(t *testing.T) {
	// Timeout values are handled in Execute(), not Validate(),
	// but we verify the request passes validation regardless of timeout
	req := &Request{
		Args:      []string{"account", "list"},
		TimeoutMs: 999999,
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() should not reject based on timeout: %v", err)
	}
}

func TestValidate_SingleArg(t *testing.T) {
	req := &Request{Args: []string{"whoami"}}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() should allow single-arg commands: %v", err)
	}
}

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
	defer ptmx.Close()

	disableOutputPostProcessing(ptmx)

	cmd := exec.Command("/bin/sh", "-c", "printf 'line1\nline2\n'")
	cmd.SysProcAttr = newSysProcAttr()
	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	if err := cmd.Start(); err != nil {
		pts.Close()
		t.Fatalf("cmd.Start() failed: %v", err)
	}
	pts.Close()

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
