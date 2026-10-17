package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
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
// The command is executed directly (no shell), preventing injection. On
// Windows that guarantee depends on ValidateResolvedPath, which refuses any
// resolved op that is not a native .exe.
func Execute(req *Request) (*Result, error) {
	return execute(req, ValidateResolvedPath)
}

// execute implements Execute with the resolved-path validator injected, so
// the refusal branch can be exercised on platforms whose real validator
// accepts everything.
func execute(req *Request, validate func(string) error) (*Result, error) {
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

	// The resolved path must be launchable without a shell. This runs before
	// anything is started, so a refused op never executes at all.
	if err := validate(opPath); err != nil {
		return nil, err
	}

	// #nosec G204 -- opPath is the absolute path returned by exec.LookPath("op") and has passed ValidateResolvedPath, and every argument has passed Request.Validate (argument count and length limits, blocked subcommands, shell metacharacters rejected); no shell is involved on any platform: unix executes the file directly, and on Windows ValidateResolvedPath requires a native .exe, because CreateProcess runs .bat and .cmd files through cmd.exe, which would re-parse the arguments
	cmd := exec.CommandContext(ctx, opPath, req.Args...)

	// Platform-specific launch. The unix implementation runs op inside a
	// pseudo-TTY (merged output); the Windows implementation uses plain pipes
	// (separate stdout and stderr). See exec_pty.go and exec_windows.go.
	stdout, stderr, runErr := runCommand(cmd)

	return interpretRun(ctx, stdout, stderr, runErr)
}

// interpretRun converts the outcome of a platform runner into the Result
// contract shared by every platform. It holds the ordering rules that must
// not vary by operating system.
func interpretRun(ctx context.Context, stdout, stderr string, runErr error) (*Result, error) {
	// A launch failure means op never ran, so neither the deadline nor an
	// exit status applies; report it as the infrastructure error it is.
	var startErr *startError
	if errors.As(runErr, &startErr) {
		return nil, startErr.err
	}

	// The context cancels a running command by killing it, so a timed-out
	// child reaches this point as an *exec.ExitError with exit status -1,
	// not as a bare context error. The deadline must be consulted before
	// the exit status is interpreted, or the timeout contract (exit 124)
	// is unreachable and clients see the killed child's raw status instead.
	if ctx.Err() == context.DeadlineExceeded {
		return &Result{
			Stderr:   "command timed out",
			ExitCode: 124,
		}, nil
	}

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return nil, fmt.Errorf("executing op: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	return &Result{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}, nil
}

// startError marks a failure to launch op at all, as opposed to a failure
// observed while waiting for it. Launch failures are infrastructure errors
// reported to the caller as-is, and they must never be mistaken for a relayed
// op exit status.
type startError struct{ err error }

func (e *startError) Error() string { return e.err.Error() }
func (e *startError) Unwrap() error { return e.err }
