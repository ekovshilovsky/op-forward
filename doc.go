// Command op-forward forwards 1Password CLI (op) commands across SSH
// boundaries with biometric authentication.
//
// The 1Password CLI requires desktop integration for biometric unlock, so op
// commands fail inside headless VMs, containers, and remote SSH sessions.
// op-forward bridges that gap: a daemon on the host machine executes op
// commands locally, triggering Touch ID through the 1Password desktop app,
// while remote environments reach it through an SSH tunnel or a Unix socket.
//
//	Remote VM: op shim -> HTTP -> SSH RemoteForward -> Host daemon -> op CLI -> Touch ID
//
// # Commands
//
//	op-forward serve [--listen EP]    Start the host daemon
//	op-forward install                Install the op shim on the remote side
//	op-forward proxy [args...]        Forward one op command to the host daemon
//	op-forward service install        Install the daemon under launchd (macOS)
//	op-forward service uninstall      Remove the launchd daemon
//	op-forward update                 Update to the latest release
//	op-forward version                Print the version
//
// # Endpoints
//
// The daemon listens on, and the proxy dials, an endpoint of the form
// tcp://host:port or unix:///path.sock. OP_FORWARD_LISTEN configures the
// daemon and OP_FORWARD_ADDR the proxy; the --listen and --addr flags
// override them. The legacy OP_FORWARD_HOST and OP_FORWARD_PORT variables
// remain supported as shorthand for a TCP endpoint and are consulted only
// when the full form is unset. Without any configuration both sides use
// tcp://127.0.0.1:18340.
//
// # Authentication
//
// The daemon issues a short-lived access token and a sliding refresh token,
// stored under the invoking user's cache directory (overridden with
// OP_FORWARD_TOKEN_DIR or OP_FORWARD_TOKEN_FILE). The proxy attaches the
// access token as a bearer credential and refreshes it transparently when it
// expires. Interactive and configuration-changing op subcommands are refused
// by the daemon.
//
// # Exit codes
//
// The proxy relays the forwarded op command's exit code unchanged and
// reserves 127 for its own infrastructure failures (daemon unreachable,
// missing credentials, protocol errors). The installed op shim treats 127 as
// the signal to fall back to the real op binary, so a broken tunnel degrades
// to local op behavior instead of failing the caller.
package main
