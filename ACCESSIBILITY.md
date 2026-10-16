# Accessibility Statement

op-forward is a command-line tool with no graphical interface. This statement
describes what that means for users of assistive technology.

## Output

* The op-forward binary itself writes plain text to stdout and stderr. A review
  of the Go source and scripts found no ANSI escape sequences, color codes,
  cursor movement, or terminal-capability detection.
* Because no color is used, no information is conveyed by color alone.
* There is no interactive text user interface, progress animation, or prompt
  that requires visual interpretation. Output is suitable for screen readers
  and for capture in logs.
* Output of forwarded `op` commands is relayed from the 1Password CLI as
  received. The daemon runs `op` in a pseudo-terminal, so `op` behaves as it
  would interactively and may include ANSI color in its own output; that
  formatting comes from `op` and passes through op-forward unchanged.

## Exit codes

Every invocation reports its result through the process exit code:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | op-forward reported an error (for example, a request rejected by validation) |
| 124 | A forwarded command timed out on the host |
| 127 | The shim could not reach the daemon (no token, no tunnel, or daemon unavailable) |
| other | The exit code of the forwarded `op` command, relayed unchanged |

## Documentation

All documentation is published as plain Markdown in this repository, readable
in any text editor, in a terminal, or on GitHub.

## Supported environments

* Host: macOS (Touch ID required for authorization)
* Remote: Linux virtual machines and containers

## Reporting accessibility barriers

If you encounter a barrier using op-forward or its documentation, please open
an [issue](https://github.com/ekovshilovsky/op-forward/issues) describing the
problem and the assistive technology or terminal you use. We will treat it as a
bug.
