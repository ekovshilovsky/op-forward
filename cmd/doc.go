// Package cmd implements the op-forward subcommands behind [Execute], which
// dispatches on the first argument of os.Args.
//
// Commands signal a specific process exit code by returning an [ExitStatus];
// every other non-nil error is reported on stderr and exits with code 1. The
// proxy command's contract is the one external callers depend on: 127 marks
// an infrastructure failure (the installed op shim falls back to the real op
// binary on it), and any other value is the forwarded op command's own exit
// code, relayed unchanged.
package cmd
