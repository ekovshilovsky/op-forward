# op-forward

[![CI](https://github.com/ekovshilovsky/op-forward/actions/workflows/ci.yml/badge.svg)](https://github.com/ekovshilovsky/op-forward/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ekovshilovsky/op-forward)](https://github.com/ekovshilovsky/op-forward/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/ekovshilovsky/op-forward)](https://goreportcard.com/report/github.com/ekovshilovsky/op-forward)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ekovshilovsky/op-forward)](go.mod)

Forward 1Password CLI (`op`) commands across SSH boundaries with biometric authentication.

## The Problem

The 1Password CLI requires desktop integration for biometric unlock (Touch ID on macOS). Inside headless VMs, containers, or remote SSH sessions, `op` commands fail because the biometric chain is broken — there's no display server, no security framework, no Touch ID sensor.

## How It Works

op-forward runs a small HTTP daemon on the host machine (where Touch ID works) and installs a transparent `op` shim on the remote side. Every `op` command in the VM is intercepted by the shim, forwarded through an SSH tunnel to the host daemon, and executed locally — triggering Touch ID for each privileged operation.

```
Remote VM: op shim → HTTP → SSH RemoteForward → Host daemon → op CLI → Touch ID
```

The developer experience is transparent: run `op account list` or `op item get <uuid> --fields username` inside any VM, and it works exactly as if `op` were running locally.

## Quick Start

### Install on macOS (host)

Via Homebrew:

```bash
brew install ekovshilovsky/tap/op-forward
```

Via the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/ekovshilovsky/op-forward/main/scripts/install.sh | sh
```

Or build from source:

```bash
git clone https://github.com/ekovshilovsky/op-forward.git
cd op-forward
go build -ldflags="-s -w" -o op-forward .
cp op-forward ~/.local/bin/
```

### Start the daemon

```bash
# Foreground (for testing)
op-forward serve

# Or install as a persistent launchd service (macOS)
op-forward service install
```

The daemon listens on `127.0.0.1:18340` (loopback only) and generates a bearer token at `~/Library/Caches/op-forward/session.token`.

### Set up the remote side (VM)

1. Copy the Linux binary and install the shim:

```bash
# Build for Linux (if cross-compiling from macOS)
GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" -o op-forward-linux-arm64 .

# Copy to VM
scp op-forward-linux-arm64 vm:~/.local/bin/op-forward

# Install the op shim on the VM
ssh vm '~/.local/bin/op-forward install --port 18340'
```

2. Deploy the auth token:

```bash
scp ~/Library/Caches/op-forward/session.token vm:~/.cache/op-forward/session.token
```

3. Start the SSH tunnel:

```bash
ssh -R 18340:127.0.0.1:18340 vm
```

Now `op` commands inside the VM are forwarded to the host.

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `OP_FORWARD_PORT` | `18340` | Daemon listen port |
| `OP_FORWARD_TOKEN_DIR` | `~/Library/Caches/op-forward` (macOS) / `~/.cache/op-forward` (Linux) | Token storage directory |
| `OP_FORWARD_TOKEN_FILE` | `$TOKEN_DIR/session.token` | Full path to token file |
| `OP_FORWARD_PROBE_TIMEOUT_MS` | `500` | Shim TCP probe timeout |
| `OP_FORWARD_FETCH_TIMEOUT_MS` | `60000` | Shim HTTP request timeout |

## Commands

```
op-forward serve [--port PORT]    Start the host daemon
op-forward install [--port PORT]  Install the op shim on the remote side
op-forward service install        Install as a launchd daemon (macOS)
op-forward service uninstall      Remove the launchd daemon
op-forward update                 Update to the latest release
op-forward version                Print version
```

## Security Model

op-forward is designed for environments where the host is trusted and the remote side connects over a secure SSH tunnel.

**Touch ID is the primary security boundary.** Every privileged 1Password operation triggers biometric approval on the host. The proxy cannot bypass this.

Additional layers:

- **Loopback-only binding**: The daemon hard-codes `127.0.0.1` and refuses to bind to any non-loopback address. It is unreachable from the network.
- **Bearer token authentication**: A 32-byte random hex token with 30-day sliding expiry. Generated on first run, stored with 0600 permissions.
- **No shell execution**: Commands are executed via `os/exec` (direct exec), not through a shell. Shell injection is structurally impossible.
- **Argument sanitization**: Arguments containing shell metacharacters (`` ` ``, `$`, `|`, `;`, `&`, newlines) are rejected before execution.
- **Blocked subcommands**: `signin`, `signout`, `update`, and `completion` are blocked — they either require interactive input or would modify the host's op configuration.
- **Audit logging**: All commands are logged with sensitive arguments (`--password`, `--reveal`) redacted.

### What this does NOT protect against

- A compromised VM with access to the token file can execute any non-blocked `op` command, subject to Touch ID approval.
- If Touch ID is configured to not require approval for every `op` invocation (unusual but possible), the proxy would execute commands without biometric gates.

The threat model assumes: SSH tunnels are secure, the host machine is not compromised, and Touch ID provides the authorization boundary.

## Use with VMs (Colima, Lima, etc.)

op-forward works with any SSH-accessible VM. For VMs managed by [Colima](https://github.com/abiosoft/colima) or [Lima](https://github.com/lima-vm/lima), use the VM's SSH config directly:

```bash
# Start tunnel (ControlMaster disabled to avoid SSH multiplexing conflicts)
ssh -fN -R 18340:127.0.0.1:18340 \
    -o ControlMaster=no \
    -o ControlPath=none \
    -F ~/.colima/_lima/<vm-profile>/ssh.config \
    lima-<vm-profile>
```

For standard SSH hosts:

```bash
ssh -fN -R 18340:127.0.0.1:18340 user@remote-host
```

The `ControlMaster=no` flag is important when using SSH multiplexing — multiplexed connections only establish `RemoteForward` on the first connection. A dedicated tunnel connection avoids this.

## Updating

Self-update to the latest release:

```bash
op-forward update
```

This downloads the latest binary from GitHub Releases for your platform, replaces the running binary in-place, and the launchd service restarts automatically.

If installed via Homebrew:

```bash
brew upgrade ekovshilovsky/tap/op-forward
```

The Homebrew formula is updated automatically on each release.

## Building

```bash
make build          # Build for current platform
make build-all      # Cross-compile for darwin/linux × arm64/amd64
make test           # Run tests
make clean          # Remove build artifacts
```

## License

MIT
