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

The daemon listens on `tcp://127.0.0.1:18340` (loopback only) by default and generates bearer tokens under `~/Library/Caches/op-forward/`. To listen on a Unix domain socket instead, see [Transports](#transports).

### Set up the remote side (VM / Linux)

Via APT (Ubuntu/Debian — recommended for VMs):

```bash
# Add the repository signing key and source
curl -fsSL https://ekovshilovsky.github.io/op-forward/key.gpg | sudo gpg --dearmor -o /usr/share/keyrings/op-forward.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/op-forward.gpg] https://ekovshilovsky.github.io/op-forward stable main" | sudo tee /etc/apt/sources.list.d/op-forward.list
sudo apt-get update
sudo apt-get install op-forward

# Install the op shim
op-forward install
```

To upgrade: `sudo apt-get update && sudo apt-get upgrade op-forward`

Via manual download:

```bash
# Download the latest release for your architecture
curl -fsSL https://github.com/ekovshilovsky/op-forward/releases/latest/download/op-forward_$(uname -m | sed 's/aarch64/arm64/;s/x86_64/amd64/').tar.gz | tar -xz -C ~/.local/bin/

# Install the op shim
op-forward install
```

After installing, deploy the auth token and start the SSH tunnel:

```bash
# Deploy auth token (from host)
scp ~/Library/Caches/op-forward/session.token vm:~/.cache/op-forward/session.token
```

Start the SSH tunnel:

```bash
ssh -R 18340:127.0.0.1:18340 vm
```

Now `op` commands inside the VM are forwarded to the host.

### Unix socket instead of a port

The daemon can listen on a Unix domain socket, and SSH can forward a socket
the same way it forwards a port. This keeps the VM-side endpoint private to
your user (the socket is `0600`) and avoids port collisions when several VMs
share one host.

```bash
# Host: listen on a socket instead of a port
export OP_FORWARD_LISTEN="unix://$HOME/Library/Caches/op-forward/op-forward.sock"
op-forward serve        # or: op-forward service install (the endpoint is written into the plist)

# Host: forward the socket into the VM (paths are expanded by the host shell,
# so spell out the VM-side path explicitly)
ssh -R /home/<vm-user>/.cache/op-forward/op-forward.sock:$HOME/Library/Caches/op-forward/op-forward.sock vm

# VM: point the shim at the forwarded socket
export OP_FORWARD_ADDR="unix://$HOME/.cache/op-forward/op-forward.sock"
```

Two things to know before choosing this mode:

- `sshd` on the VM does not remove a forwarded socket when the connection
  drops, so the next `ssh -R` fails with `bind: Address already in use`. Set
  `StreamLocalBindUnlink yes` in the VM's `/etc/ssh/sshd_config` to have it
  replaced automatically.
- The socket's directory must be owned by you and mode `0700`; the daemon
  refuses shared locations such as `/tmp`. `~/Library/Caches/op-forward` on
  macOS and `$XDG_RUNTIME_DIR` on Linux both qualify.
- Unix socket paths are limited to about 104 bytes on macOS; keep the host
  socket path short.

### Docker Desktop containers (no SSH tunnel)

For a local Docker Desktop container (e.g. a VS Code dev container) you don't need
an SSH reverse tunnel — the container can reach the host daemon directly over
`host.docker.internal`. Point the shim at it instead:

```bash
# in the container (after deploying the token as above)
export OP_FORWARD_HOST=host.docker.internal
op-forward install
```

The daemon still binds loopback only; Docker Desktop routes `host.docker.internal`
to the host, so no tunnel or extra proxy is needed.
`OP_FORWARD_ADDR=tcp://host.docker.internal:18340` is the equivalent full form.
Use TCP here: a host Unix socket cannot be bind-mounted into a Docker Desktop
container (`docker run` fails at mount time with `operation not supported`).

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `OP_FORWARD_LISTEN` | `tcp://127.0.0.1:$OP_FORWARD_PORT` | Endpoint the daemon binds: `tcp://127.0.0.1:PORT` (loopback only) or `unix:///absolute/path.sock` |
| `OP_FORWARD_ADDR` | `tcp://$OP_FORWARD_HOST:$OP_FORWARD_PORT` | Endpoint the shim dials: `tcp://host:port` or `unix:///absolute/path.sock`. Overrides `OP_FORWARD_HOST`/`OP_FORWARD_PORT` when set. |
| `OP_FORWARD_PORT` | `18340` | TCP port shorthand, used by both sides when the full endpoint form above is unset |
| `OP_FORWARD_HOST` | `127.0.0.1` | TCP host shorthand for the shim. Set to `host.docker.internal` to reach the host from a Docker Desktop container without an SSH tunnel. Never used by the daemon. |
| `OP_FORWARD_TOKEN_DIR` | `~/Library/Caches/op-forward` (macOS) / `~/.cache/op-forward` (Linux) | Token storage directory |
| `OP_FORWARD_TOKEN_FILE` | `$TOKEN_DIR/session.token` | Full path to token file |
| `OP_FORWARD_PROBE_TIMEOUT_MS` | `500` | How long the shim waits for the daemon to accept a connection before falling back to the local `op` |
| `OP_FORWARD_FETCH_TIMEOUT_MS` | `60000` | Shim HTTP request timeout |

## Commands

```
op-forward serve [--listen EP]    Start the host daemon (EP: tcp://127.0.0.1:PORT or unix:///path.sock)
op-forward install                Install the op shim on the remote side
op-forward service install        Install as a launchd daemon (macOS)
op-forward service uninstall      Remove the launchd daemon
op-forward update                 Update to the latest release
op-forward version                Print version
```

## Security Model

op-forward is designed for environments where the host is trusted and the remote side connects over a secure SSH tunnel.

**Touch ID is the primary security boundary.** Every privileged 1Password operation triggers biometric approval on the host. The proxy cannot bypass this.

Additional layers:

- **Local-only binding**: TCP endpoints must be loopback; the daemon refuses to bind anything else, so it is unreachable from the network. Unix socket endpoints are created `0600` inside a `0700` directory, and the daemon additionally checks the connecting process's uid (`SO_PEERCRED` / `LOCAL_PEERCRED`) and refuses other users even if the socket file is exposed.
- **Bearer token authentication**: A 32-byte random hex token with 30-day sliding expiry. Generated on first run, stored with 0600 permissions.
- **No shell execution**: Commands are executed via `os/exec` (direct exec), not through a shell. Shell injection is structurally impossible.
- **Argument sanitization**: Arguments containing shell metacharacters (`` ` ``, `$`, `|`, `;`, `&`, newlines) are rejected before execution.
- **Blocked subcommands**: `signin`, `signout`, `update`, and `completion` are blocked — they either require interactive input or would modify the host's op configuration.
- **Audit logging**: All commands are logged with sensitive arguments (`--password`, `--reveal`) redacted.

### What this does NOT protect against

- A compromised VM with access to the token file can execute any non-blocked `op` command, subject to Touch ID approval.
- If Touch ID is configured to not require approval for every `op` invocation (unusual but possible), the proxy would execute commands without biometric gates.

The threat model assumes: SSH tunnels are secure, the host machine is not compromised, and Touch ID provides the authorization boundary.

## Transports

Both sides accept a single endpoint value that selects the transport:

| Form | Reachable by | Use when |
|---|---|---|
| `tcp://127.0.0.1:18340` (default) | SSH port forwarding (`ssh -R 18340:127.0.0.1:18340`), Docker Desktop's `host.docker.internal` | Standard setups; Docker Desktop containers (a host Unix socket cannot be bind-mounted into the container, so `unix://` cannot serve them) |
| `unix:///absolute/path.sock` | SSH socket forwarding (`ssh -R remote.sock:local.sock`) | You want the VM-side endpoint private to your user, or several VMs would otherwise fight over one port |

The HTTP protocol, bearer tokens, and command validation are identical over both transports.

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
