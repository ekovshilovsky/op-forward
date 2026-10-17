# Deployment

This page covers installing op-forward on the host and on remote machines,
deploying tokens, connecting the two with SSH, and diagnosing problems. Command
details are in [cli.md](cli.md); variables are in
[configuration.md](configuration.md).

## Topologies

| Topology | Transport | Section |
|---|---|---|
| VM or server reached over SSH, forwarded port | TCP | [TCP port](#tcp-port) |
| VM or server reached over SSH, forwarded socket | Unix socket | [Unix socket](#unix-socket) |
| Colima or Lima VM | TCP or Unix socket over the VM's SSH config | [Colima and Lima](#colima-and-lima) |
| Docker Desktop container | TCP to `host.docker.internal` | [Docker Desktop containers](#docker-desktop-containers) |

In every case the host runs one daemon, and each remote machine has the
`op-forward` binary, the `op` shim, and token files.

## Host setup

The host is the machine with the 1Password desktop application and Touch ID,
normally a Mac. It needs the 1Password CLI (`op`) installed and integrated with
the desktop application, so that `op` works in a local terminal.

Install the binary with Homebrew, the install script, or a source build:

```bash
brew install ekovshilovsky/tap/op-forward
# or
curl -fsSL https://raw.githubusercontent.com/ekovshilovsky/op-forward/main/scripts/install.sh | sh
```

The install script places the binary in `~/.local/bin` (override with
`OP_FORWARD_INSTALL_DIR`) for macOS or Linux on amd64 or arm64. It does not
verify a checksum.

### Foreground daemon

```bash
op-forward serve
```

This listens on `tcp://127.0.0.1:18340` and writes tokens to
`~/Library/Caches/op-forward`. Use it for testing. To select a Unix socket or
another port:

```bash
op-forward serve --listen unix://$HOME/Library/Caches/op-forward/op-forward.sock
op-forward serve --port 19000
```

Verify from the host:

```bash
curl -s http://127.0.0.1:18340/health
```

### launchd service

```bash
op-forward service install
```

This writes `~/Library/LaunchAgents/com.op-forward.daemon.plist` and loads it.
The service starts at login (`RunAtLoad`), is restarted if it exits
(`KeepAlive`), and logs to `~/Library/Logs/op-forward.log`.

Because launchd does not inherit your shell environment:

- the listen endpoint is resolved when you run `service install` and stored as
  `--listen` in the plist; to change it, export `OP_FORWARD_LISTEN` (or
  `OP_FORWARD_PORT`) and run `service install` again;
- `OP_FORWARD_TOKEN_DIR` and `OP_FORWARD_TOKEN_FILE` are not copied, so the
  service uses the default token directory;
- the service runs with `PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`,
  so `op` must be installed in one of those directories.

The plist records the resolved path of the binary. Run `service install` again
after the binary moves, for example after a Homebrew upgrade replaces a
versioned directory. Remove the service with `op-forward service uninstall`.

### Windows host

> **Experimental.** The Windows build has not yet been exercised on a real
> Windows host. In particular, the guarantee that every forwarded command
> raises its own approval prompt is **unverified** there: on macOS the daemon
> runs `op` inside a pseudo-TTY precisely so 1Password treats each invocation
> as interactive and asks for Touch ID again instead of reusing a cached
> approval. Windows has no equivalent mechanism here, so a Windows host may
> reuse an approval for a period. Until that is measured, do not rely on
> per-command approval on Windows.

The daemon can run on a Windows machine that has the 1Password desktop app and
the 1Password CLI (`op.exe`) installed. Download
`op-forward_<version>_windows_<amd64|arm64>.zip` from the release page, extract
`op-forward.exe` to a directory on `PATH`, and run it in a terminal:

```powershell
op-forward.exe serve
```

What differs from a macOS or Linux host:

- Only loopback TCP is supported. `unix://` endpoints are refused with
  `unix sockets are not supported on Windows; use tcp://127.0.0.1:PORT`.
- Tokens live under `%LocalAppData%\op-forward`. Windows ignores the POSIX
  0700/0600 modes the daemon requests; protection comes from the access control
  list the user profile directory carries by default, which does not grant other
  standard users access (verify this on your machine if the profile ACLs have been
  customized). Do not point `OP_FORWARD_TOKEN_DIR` at a shared location.
- `op` output is captured through ordinary pipes rather than a pseudo-terminal,
  so stdout and stderr are returned separately. The macOS behavior of forcing a
  fresh biometric prompt on every call comes from the pseudo-terminal and is
  **not verified** on Windows: whether each call raises its own Windows Hello
  prompt or the 1Password app reuses an approval has to be observed on the
  machine.
- The daemon only runs a native `op.exe`. If the `op` it resolves from `PATH` is
  a `.bat` or `.cmd` file (or anything whose extension is not `.exe`), the
  daemon refuses to execute it and every forwarded command fails with exit
  status 127. Windows runs batch files through `cmd.exe`, which re-parses the
  argument string, so no argument filter can make that safe; refusing the file
  is the control. Install the official 1Password CLI, which is `op.exe`. With the
  default `PATHEXT` order a native `op.exe` wins over an `op.cmd` in the same
  directory, but an `op.cmd` in an earlier `PATH` directory shadows it.
  `op-forward doctor` reports a refused `op` as a failure on the `op_cli` check
  (and on `daemon_op` from a client). This restriction does not exist on macOS
  or Linux, where `op` is executed directly.
- `op-forward service`, `op-forward install` (the POSIX shim) and
  `op-forward update` are not available. Start the daemon with
  `op-forward.exe serve`, or register that command as a Task Scheduler task
  yourself. To update, replace `op-forward.exe` by hand after stopping the daemon.

## Remote setup

Install `op-forward` on each remote machine by whichever route matches it, then
run `op-forward install` as each user who will run `op`. Packages install
`/usr/bin/op-forward`; the install script installs to `~/.local/bin`. Packages
declare no dependencies, and the shim is POSIX `sh`, so it runs on minimal images
such as stock Alpine that do not include Bash.

Repository URLs below use the project's Pages site, `https://ekovshilovsky.github.io/op-forward`.

### APT (Debian and Ubuntu)

```bash
curl -fsSL https://ekovshilovsky.github.io/op-forward/key.gpg | sudo gpg --dearmor -o /usr/share/keyrings/op-forward.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/op-forward.gpg] https://ekovshilovsky.github.io/op-forward stable main" | sudo tee /etc/apt/sources.list.d/op-forward.list
sudo apt-get update && sudo apt-get install op-forward
```

Architectures: amd64 and arm64. The index lists every published version, so an
older version can be installed explicitly (`apt-get install op-forward=VERSION`).

### RPM (Fedora, RHEL, openSUSE)

```bash
sudo curl -fsSL https://ekovshilovsky.github.io/op-forward/rpm/op-forward.repo -o /etc/yum.repos.d/op-forward.repo
sudo dnf install op-forward
```

Architectures: x86_64 and aarch64. The repository file enables `gpgcheck` and
`repo_gpgcheck` and points `gpgkey` at the same `key.gpg` as the APT repository
when the repository was built with a signing key.

### Alpine (apk)

```bash
sudo curl -fsSL https://ekovshilovsky.github.io/op-forward/apk/op-forward.rsa.pub -o /etc/apk/keys/op-forward.rsa.pub
echo "https://ekovshilovsky.github.io/op-forward/apk" | sudo tee -a /etc/apk/repositories
sudo apk add op-forward
```

Architectures: x86_64 and aarch64. The Alpine repository is published only when
the project's Alpine signing key is configured in the release pipeline.

### Arch Linux

Download the `.pkg.tar.zst` for your architecture from the
[latest release](https://github.com/ekovshilovsky/op-forward/releases/latest)
and install it:

```bash
sudo pacman -U op-forward-<version>-1-x86_64.pkg.tar.zst
```

The release pipeline also generates an AUR definition for `op-forward-bin`
(see [development.md](development.md#aur-package)), which it pushes when the
project's AUR credentials are configured.

### Static tarball and install script

For distributions without a package, or for a per-user install:

```bash
curl -fsSL https://raw.githubusercontent.com/ekovshilovsky/op-forward/main/scripts/install.sh | sh
```

Or download `op-forward_<version>_<os>_<arch>.tar.gz` from the release page and
copy the `op-forward` binary to a directory on `PATH`. Linux binaries are
statically linked, so they run on musl and older glibc systems.

### Installing the shim

```bash
op-forward install
```

This writes `~/.local/bin/op`. Ensure `~/.local/bin` precedes the directory
containing the real `op` on `PATH`; the command reports whether `op` now
resolves to the shim.

Packages from releases 0.7.2 and later install to `/usr/bin`; earlier releases
used `/usr/local/bin`. The package's post-install script repairs shims it can
find in `/home/*/.local/bin/op` and `/root/.local/bin/op`, rewriting only the
recorded binary path and preserving ownership. A shim anywhere else needs
`op-forward install` re-run. Shims written by the current template also look for
`op-forward` on `PATH` when their recorded path is gone.

## Token deployment

Each remote machine needs the host's current `refresh.token`. Copy it into the
remote token directory (`~/.cache/op-forward` on Linux):

```bash
ssh <vm> 'mkdir -p -m 700 ~/.cache/op-forward'
scp ~/Library/Caches/op-forward/refresh.token <vm>:.cache/op-forward/refresh.token
ssh <vm> 'chmod 600 ~/.cache/op-forward/refresh.token'
```

No access token needs to be copied: on the first command the proxy finds no
access token, refreshes, and writes `access.token` and a new `refresh.token`
itself.

The daemon keeps one token pair, and every refresh replaces it. If you copy the
same token files to several machines, the first one to refresh invalidates the
copies on the others. Machines that share a home directory share one token
directory and see each other's refreshed files; for the rest, redeploy
`refresh.token` to a machine that stops working. The mechanics are in [tokens.md](tokens.md#refresh-endpoint-behavior).

Containers that start fresh each time need the token files provided at startup,
by whatever mechanism your environment uses to supply files to containers; the
proxy writes refreshed tokens back to its token directory, which is lost if the
directory is not persistent.

The older `session.token` file is still honored for machines that have it; see
[tokens.md](tokens.md#legacy-token-migration).

## SSH tunnel patterns

### TCP port

```bash
# From the host
ssh -R 18340:127.0.0.1:18340 <vm>

# Background, dedicated connection
ssh -fN -R 18340:127.0.0.1:18340 -o ControlMaster=no -o ControlPath=none <vm>
```

No configuration is needed on the remote side for the default port. For a
different port, run the daemon with `--port` or `OP_FORWARD_PORT` on the host and
set `OP_FORWARD_PORT` on the remote machine, or use full endpoints. A persistent
`RemoteForward 18340 127.0.0.1:18340` entry in `~/.ssh/config` is equivalent.

Use `ControlMaster=no` and `ControlPath=none` (or a dedicated host entry) when
you rely on connection multiplexing elsewhere: per the OpenSSH documentation, a
multiplexed session reuses the first connection, so `RemoteForward` is
established only by the first connection.

### Unix socket

```bash
# Host
export OP_FORWARD_LISTEN="unix://$HOME/Library/Caches/op-forward/op-forward.sock"
op-forward serve            # or: op-forward service install

# Host: forward the socket (VM-side path written out in full)
ssh -R /home/<vm-user>/.cache/op-forward/op-forward.sock:$HOME/Library/Caches/op-forward/op-forward.sock <vm>

# VM
export OP_FORWARD_ADDR="unix:///home/<vm-user>/.cache/op-forward/op-forward.sock"
```

On the VM, set `StreamLocalBindUnlink yes` in `/etc/ssh/sshd_config` so that a
socket left from a previous connection is replaced, and create the socket's
directory beforehand. Keep both paths under the operating-system limit of
roughly 104 bytes on macOS. Rules and caveats are in
[transport.md](transport.md#ssh-remote-forwarding).

## Colima and Lima

Colima and Lima VMs are reachable over SSH with a per-profile configuration
file. Use it directly:

```bash
ssh -fN -R 18340:127.0.0.1:18340 \
    -o ControlMaster=no \
    -o ControlPath=none \
    -F ~/.colima/_lima/<vm-profile>/ssh.config \
    lima-<vm-profile>
```

`ControlMaster=no` and `ControlPath=none` avoid the multiplexing issue
described above. The Unix socket variant works the same way with the `-R
socket:socket` form. Colima's profile names and file locations are defined by
Colima and Lima; check them against your installation.

## Docker Desktop containers

For containers on the same Mac as the daemon, no SSH tunnel is needed:

```bash
# Inside the container, after supplying the token files
export OP_FORWARD_HOST=host.docker.internal
op-forward install
```

The full form is `OP_FORWARD_ADDR=tcp://host.docker.internal:18340`. Keep the
daemon on its default TCP endpoint; a host Unix socket cannot be bind-mounted
into a Docker Desktop container (see
[transport.md](transport.md#docker-desktop-containers); both Docker Desktop
claims were verified empirically during development). The shim reads the
variable each time it runs, so set it in the container's environment (an image
`ENV`, a devcontainer setting, or a shell profile).

## Upgrading

| Installed with | Upgrade with | Notes |
|---|---|---|
| Homebrew | `brew upgrade ekovshilovsky/tap/op-forward` | Re-run `op-forward service install` if the service's recorded binary path changed |
| Install script or tarball | `op-forward update` | Stops the running daemon (SIGTERM) and relies on the supervisor (launchd) to start the new binary, then waits for `/health`; without a supervisor the daemon stays down |
| APT, dnf, apk | `apt-get upgrade`, `dnf upgrade`, `apk upgrade` | The binary lives in `/usr/bin` |
| Arch package | Download and `pacman -U` the new package | |

After upgrading the host binary, the running daemon must be restarted to run the
new version. `op-forward update` signals the daemon to stop and expects launchd to
start it again; a daemon started by hand is not restarted. Older clients keep working
unless the daemon's minimum version excludes them (see
[protocol.md](protocol.md#version-negotiation)).

## Troubleshooting

Daemon output goes to the terminal when run in the foreground and to
`~/Library/Logs/op-forward.log` under launchd. On the remote side, run the proxy
directly to see its message and status:

```bash
op-forward proxy -- account list; echo "exit=$?"
```

### Start with `op-forward doctor`

Run `op-forward doctor` first: it names the broken layer (shim, endpoint, daemon,
tokens, or the host's own 1Password session) instead of leaving you to guess from
a failed `op` command. Do not use `op whoami` to judge the host: it reports
`account is not signed in` both when the account is signed out and when the
1Password app is merely locked. By default doctor renews an expired access token
from the refresh token (which rotates the pair, as the proxy does); pass
`--no-refresh` for a strictly read-only run. See [cli.md](cli.md#doctor).

### Locked 1Password app

Check the app's lock state first. A locked 1Password desktop app is the most
common cause of "op does not work" with a healthy tunnel, and `op` hides it.
Measured on a host with the app locked:

| Command on the host | Result |
|---|---|
| `op account list` | Succeeds in well under a second and lists the accounts: metadata works while locked |
| `op vault list` (any account) | Hangs with no output, and only exits 1 after about a minute |
| `op whoami` | Exits 1 immediately with `account is not signed in`: misleading, it is the same wording as a signed-out account |

Symptom through op-forward: `op` commands on the VM hang until the timeout
(exit 124), or `op-forward doctor --interactive` reports `app_locked`. Fix:
unlock the 1Password app on the host (a Touch ID approval is enough). Every
command works again immediately; nothing in op-forward needs restarting.
`op-forward doctor` without `--interactive` cannot see this state, because it
reads metadata only; run `op-forward doctor --interactive` to verify.

### Daemon not reachable

Symptom: `op-forward: daemon not reachable at tcp://127.0.0.1:18340` (or the
Unix endpoint), exit 127, and the shim runs the real `op` instead.

Causes and checks:

1. The daemon is not running: on the host, `curl -s http://127.0.0.1:18340/health`
   should print `{"status":"ok"}`; check the launchd job and the log.
2. The tunnel is down or was never started: restart `ssh -R ...`.
3. The remote side points at a different endpoint than the one forwarded; compare
   `OP_FORWARD_ADDR`, `OP_FORWARD_HOST`, and `OP_FORWARD_PORT` with the tunnel.
4. The remote port was already taken, so `ssh -R` could not bind it; OpenSSH
   reports the failure when the tunnel starts.
5. The probe timed out on a slow link; raise `OP_FORWARD_PROBE_TIMEOUT_MS`.

### 403 forbidden

Symptom: `op-forward: daemon returned HTTP 403` with body `forbidden`.

The daemon refused the connection because the connecting process's uid on the
host differs from the daemon's. This happens only with Unix sockets. The daemon
log shows `refused connection from uid N (daemon runs as uid M)`. Typical cause:
the daemon and the `ssh -R` client run as different users, for example a daemon
started with `sudo` and a tunnel started as a normal user. Run both as the same
user. See [transport.md](transport.md#peer-credential-check).

### 401 or token refresh failed

Symptom: `op-forward: token refresh failed — ...` or `daemon returned HTTP 401`.

Redeploy the host's `refresh.token` as shown in
[Token deployment](#token-deployment). If several machines share one daemon, see
the note there about rotation. Meanings of each message are in
[tokens.md](tokens.md#failure-modes).

### Stale sockets and address in use

- Host: `socket PATH is already in use by another daemon` means a daemon is
  listening there, for example a launchd job and a manual `serve` configured
  with the same path. Stop one. A socket that does not answer the daemon's probe
  (for example one left by a daemon that was killed) is treated as stale and
  replaced automatically.
- Host: `PATH exists and is not a socket; refusing to replace it` means a regular
  file or directory is at the path. Choose another path or remove the file
  yourself.
- Host: `socket directory ... is mode ...; it must be 0700` or `owned by uid`
  means the parent directory is shared or belongs to another user. Use a private
  directory such as `~/Library/Caches/op-forward`. If the path is also over about
  100 bytes (typical under `/tmp` or a macOS `/var/folders/...` temp directory),
  the same message ends with `also, socket paths are limited to about 104 bytes
  on macOS; choose a shorter path`: fix both together. The directory check runs
  first by design.
- VM: `ssh -R` reports `Address already in use` for the socket path (this is
  OpenSSH's error text, not an op-forward message). Set
  `StreamLocalBindUnlink yes` on the VM's sshd, or delete the leftover socket file.
- A bind failure for a long path (in a private directory) includes a hint about the roughly 104-byte limit
  on macOS; shorten the path.

### Shim falling back to the real op

Symptom: `op` works but no Touch ID prompt appears, or it fails with a message from
the local `op`. Every proxy exit of 127 makes the shim run the real `op` (see
[protocol.md](protocol.md#exit-codes)). Run
`op-forward proxy -- account list; echo "exit=$?"` to see the proxy's own message
and status. Also confirm `command -v op` resolves to `~/.local/bin/op`, and that
the shim's `OP_FORWARD_BIN` line names an executable file or that `op-forward` is
on `PATH`.

### 426 upgrade required

Symptom: the proxy prints a JSON error saying the client version is below the
minimum and exits 127. Update the remote binary through its package manager or
`op-forward update`.

### op not found on the host

Symptom: `execution error: op CLI not found in PATH` with HTTP 500. The daemon
could not find `op`. Under launchd the daemon's `PATH` is fixed in the plist (see
[launchd service](#launchd-service)) and is not taken from your shell, so install
or link `op` into one of the directories listed there.

### Escape sequences in output

`op` is run under a pseudo-terminal, so its output can contain ANSI color escape
sequences that reach the remote caller unchanged. See
[protocol.md](protocol.md#output-formatting).

### Daemon did not come back after update

`op-forward update` reports `daemon did not come back on ...`. Check the log,
and restart with `launchctl kickstart -k gui/$(id -u)/com.op-forward.daemon`.
Also confirm the `OP_FORWARD_LISTEN` or `OP_FORWARD_PORT` in the shell that ran
`update` matches the service's endpoint.

## See also

- [cli.md](cli.md) for command details
- [transport.md](transport.md) for tunnel and socket rules
- [tokens.md](tokens.md) for the token lifecycle
- [security.md](security.md#hardening-checklist) for hardening guidance
