package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

const (
	DefaultTimeout = 60 * time.Second
	MaxTimeout     = 5 * 60 * time.Second
)

// Result holds the output of an op command execution.
type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Request represents an incoming op command execution request.
type Request struct {
	Args      []string `json:"args"`
	TimeoutMs int      `json:"timeout_ms,omitempty"`
}

// blockedSubcommands are op subcommands that should never be proxied.
// These either modify the local op configuration or are interactive-only.
var blockedSubcommands = map[string]bool{
	"signin":     true, // Interactive authentication — must happen locally
	"signout":    true, // Would sign out the host's session
	"update":     true, // Would update the host's op binary
	"completion": true, // Shell completion — useless over proxy
}

const (
	MaxArgs      = 64   // Maximum number of arguments per request
	MaxArgLength = 4096 // Maximum length of a single argument in bytes
)

// Validate checks the request for safety issues.
func (r *Request) Validate() error {
	if len(r.Args) == 0 {
		return fmt.Errorf("no arguments provided")
	}

	if len(r.Args) > MaxArgs {
		return fmt.Errorf("too many arguments: %d (max %d)", len(r.Args), MaxArgs)
	}

	// Check for blocked subcommands
	subcmd := r.Args[0]
	if blockedSubcommands[subcmd] {
		return fmt.Errorf("subcommand %q is not allowed through the proxy", subcmd)
	}

	// Reject arguments that look like shell injection attempts.
	// op uses structured arguments — no legitimate arg contains shell metacharacters.
	for _, arg := range r.Args {
		if len(arg) > MaxArgLength {
			return fmt.Errorf("argument too long: %d bytes (max %d)", len(arg), MaxArgLength)
		}
		for _, c := range arg {
			switch c {
			case '`', '$', '|', ';', '&', '\n', '\r':
				return fmt.Errorf("argument contains disallowed character: %q", string(c))
			}
		}
	}

	return nil
}

// Execute runs an op command with the given arguments.
// The command is executed directly (no shell), preventing injection.
func Execute(req *Request) (*Result, error) {
	if err := req.Validate(); err != nil {
		return &Result{
			Stderr:   err.Error(),
			ExitCode: 1,
		}, nil
	}

	timeout := DefaultTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opPath, err := exec.LookPath("op")
	if err != nil {
		return nil, fmt.Errorf("op CLI not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, opPath, req.Args...)

	// Run op inside a full pseudo-TTY so it detects an interactive session
	// on all file descriptors (stdin, stdout, stderr). Without a TTY,
	// 1Password caches the biometric approval and skips Touch ID on
	// subsequent calls. With a full PTY, each invocation triggers a fresh
	// biometric prompt — matching the behavior of running op directly in
	// a terminal.
	//
	// Trade-off: PTY merges stdout and stderr into a single stream. We
	// capture the combined output as stdout and leave stderr empty.
	cmd.SysProcAttr = newSysProcAttr()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("starting op with pty: %w", err)
	}

	// Disable the PTY's output post-processing (OPOST) so the line discipline
	// does not convert \n to \r\n. This preserves the raw output from op and
	// avoids fragile string replacement that could corrupt binary data or
	// fields containing legitimate carriage returns. Platform-specific because
	// macOS uses TIOCGETA/TIOCSETA and Linux uses TCGETS/TCSETS.
	disableOutputPostProcessing(ptmx)

	// Read all output from the PTY master
	var output bytes.Buffer
	_, _ = output.ReadFrom(ptmx)
	_ = ptmx.Close()

	// Wait for the command to finish
	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Stderr:   "command timed out",
				ExitCode: 124,
			}, nil
		} else {
			return nil, fmt.Errorf("executing op: %w", err)
		}
	}

	return &Result{
		Stdout:   output.String(),
		Stderr:   "",
		ExitCode: exitCode,
	}, nil
}
