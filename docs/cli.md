# Command-line reference

`op-forward` is a single binary with subcommands. Dispatch happens in the
`switch` in [cmd/root.go](../cmd/root.go); each subcommand is implemented in its
own file under [cmd/](../cmd). Environment variables and precedence are in
[configuration.md](configuration.md).

## Synopsis

```
op-forward serve [--listen EP] [--port N]
op-forward install [--port N]
op-forward proxy [--addr EP] [--host H] [--port N] [--timeout MS] [--] [op arguments...]
op-forward service install
op-forward service uninstall
op-forward update
op-forward doctor [--json] [--interactive] [--no-refresh] [--role auto|client|host] [--addr EP] [--listen EP] [--host H] [--port N]
op-forward version
op-forward help | --help | -h
```

Subcommands recognized by the dispatcher: `serve`, `install`, `proxy`,
`service`, `update`, `doctor`, `version`, and `help` (also `--help` and `-h`).

Behavior of the dispatcher:

- With no arguments it prints the usage text and exits 0.
- `help`, `--help`, and `-h` print the usage text and exit 0.
- An unknown subcommand prints `Unknown command: NAME` to stderr, then the usage
  text, and exits 1.
- The subcommands that take flags use Go's `flag` package with
  exit-on-error behavior: a flag error prints a message and usage and exits 2,
  and `-h` after a subcommand prints that subcommand's flags and exits 0.

## serve

```
op-forward serve [--listen EP] [--port N]
```

Starts the host daemon in the foreground. It does not detach; for a persistent
daemon use [`service install`](#service) on macOS.

| Flag | Default | Meaning |
|---|---|---|
| `--listen EP` | `$OP_FORWARD_LISTEN` | `tcp://127.0.0.1:PORT` or `unix:///absolute/path.sock`. Overrides `--port` |
| `--port N` | `$OP_FORWARD_PORT` or 18340 | Loopback TCP port, used when no endpoint is given |

Precedence between these and the environment is described in
[configuration.md](configuration.md#precedence-rules).

On startup it migrates a legacy `session.token`, loads or generates the refresh
token, generates and writes a fresh access token, and prints their locations:

```
Refresh token reused (expires 2026-11-15T14:30:00-07:00)
Refresh token at: /Users/me/Library/Caches/op-forward/refresh.token
Access token written to: /Users/me/Library/Caches/op-forward/access.token (expires 2026-10-16T15:30:00-07:00)
Starting daemon on tcp://127.0.0.1:18340
op-forward daemon listening on tcp://127.0.0.1:18340
```

The first line reads `Refresh token generated (no valid existing token found)`
when a new refresh token was created. The details are in
[tokens.md](tokens.md#daemon-startup). The command runs until the listener fails
and then returns the error. A TCP bind to a non-loopback address, a Unix socket
in a shared directory, an existing non-socket file, or a socket that is already
in use stops it before it serves (see [transport.md](transport.md)).

## install

```
op-forward install [--port N]
```

Writes the `op` shim on the machine where it runs, normally the remote side.

| Flag | Meaning |
|---|---|
| `--port N` | Accepted for compatibility with older scripts and ignored. The shim reads `OP_FORWARD_ADDR`, `OP_FORWARD_HOST`, and `OP_FORWARD_PORT` every time it runs, so nothing is stored at install time |

Steps:

1. Find the real `op`: the first file named `op` on `PATH`, skipping
   `~/.local/bin`; `/usr/bin/op` if none is found. The path is recorded in the
   shim as the fallback.
2. Choose the `op-forward` path to record: `~/.local/bin/op-forward` if that
   file exists, otherwise the path of the running executable.
3. Create `~/.local/bin` (mode 0755) and write `~/.local/bin/op` with mode 0755,
   replacing any existing file there.
4. Print the shim path, the real `op` path, and whether `op` on `PATH` now
   resolves to the shim. If it does not, `~/.local/bin` is not first on `PATH`.

The shim is a POSIX `sh` script (`#!/bin/sh`), so it runs on minimal systems
without Bash. Its header carries a version marker line,
`# op-forward-shim-version: <version>`, filled in from the installing binary;
[`doctor`](#doctor) reads it to detect a shim left behind by an older release.
If the recorded `op-forward` path is no longer executable (for example
after a package moved the binary) it uses `op-forward` from `PATH`. It then runs
`op-forward proxy -- "$@"`; if the exit status is anything other than 127 it
exits with that status. If `op-forward` cannot be found or the proxy exits 127,
the shim runs the recorded real `op`. If that is not executable either, it prints
`op-forward: proxy unavailable and no fallback op binary` and exits 1.
The template is `shimTemplate` in [cmd/install.go](../cmd/install.go).

Re-run `install` after changing which `op` is the real one or after installing
`op-forward` to a different location.

## proxy

```
op-forward proxy [--addr EP] [--host H] [--port N] [--timeout MS] [--] [op arguments...]
```

Forwards one `op` invocation to the daemon and reproduces its output and exit
status. The shim calls it; calling it directly is useful for diagnosis.

| Flag | Default | Meaning |
|---|---|---|
| `--addr EP` | `$OP_FORWARD_ADDR` | Daemon endpoint (`tcp://host:port` or `unix:///path.sock`). Overrides `--host` and `--port` |
| `--host H` | `$OP_FORWARD_HOST` or `127.0.0.1` | Daemon host. Use `host.docker.internal` for Docker Desktop containers |
| `--port N` | `$OP_FORWARD_PORT` or 18340 | Daemon port |
| `--timeout MS` | `$OP_FORWARD_FETCH_TIMEOUT_MS` or 60000 | Execution timeout sent to the daemon (the daemon caps it at 300000) |

Put `--` before the `op` arguments so that flags such as `--fields` are not
interpreted as proxy flags. The shim always does this.

Exit status:

| Status | Meaning |
|---|---|
| `op`'s own status | The command ran on the host; stdout and stderr were reproduced locally |
| 1 | The daemon rejected the arguments (for example a blocked subcommand, or no arguments) |
| 124 | The command timed out on the host |
| 127 | Infrastructure failure: bad endpoint, no tokens, daemon unreachable, failed refresh, upgrade required, or any non-200 reply |
| 2 | Invalid proxy flag |

The conditions behind 127 and the output rules are in
[protocol.md](protocol.md#exit-codes). Diagnostics, including
`update available (...)` hints, are written to stderr so they never mix with
`op` output on stdout.

## service

```
op-forward service install
op-forward service uninstall
```

Manages a launchd LaunchAgent for the daemon. Available on macOS only; elsewhere
the command fails with `service management is only supported on macOS`. With no
second word it prints `Usage: op-forward service [install|uninstall]`; any other
word is an error (`unknown service command: NAME`). No flags are accepted.

### service install

1. Resolve the path of the running binary, following symlinks.
2. Resolve the listen endpoint now, from `OP_FORWARD_LISTEN` or
   `OP_FORWARD_PORT` in the current environment (default
   `tcp://127.0.0.1:18340`). launchd does not inherit the shell environment, so
   the endpoint is written into the plist.
3. Write `~/Library/LaunchAgents/com.op-forward.daemon.plist`. Its program
   arguments are `<binary> serve --listen <endpoint>`; its environment sets
   `PATH` to `/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`
   and `HOME`; it sets `RunAtLoad` and `KeepAlive`; it sends stdout and stderr
   to `~/Library/Logs/op-forward.log`. Values are XML-escaped.
4. Unload any existing job, then `launchctl load -w`. If that fails it tries
   `launchctl bootout` followed by `launchctl bootstrap gui/<uid>`.
5. Print the plist and log paths.

Run `service install` again after moving the binary, or after changing the
endpoint, because the plist records both.

### service uninstall

Unloads the job (ignoring errors), removes the plist if it exists, and prints
`Launchd service uninstalled.` Token files and logs are left in place.

## update

```
op-forward update
```

Replaces the running binary with the latest GitHub release.

1. Print the current version and query
   `https://api.github.com/repos/ekovshilovsky/op-forward/releases/latest`.
2. If the tag without its leading `v` equals the current version string, print
   `Already up to date` and stop. A build whose version is `dev` therefore always
   proceeds.
3. Download `op-forward_<version>_<os>_<arch>.tar.gz` for the running platform
   and extract the entry named `op-forward`. No checksum or signature is
   verified.
4. Write the new binary next to the current one (`<path>.update`, mode 0755) and
   rename it over the current path, which is resolved through symlinks. The
   user needs write access to that directory.
5. Look for a running daemon with `pgrep -f "<binary path> serve"`. If one is
   found, send it SIGTERM and print its PID. If none is found, print that it will
   pick up the new version on its next start and finish.
6. If the daemon was signaled, wait for it to return: poll `GET /health` on the
   endpoint every 500 ms for up to 30 seconds (each attempt has a 2-second
   timeout). On success print `Daemon is serving again on <endpoint>`. On timeout
   return an error naming the endpoint, the last failure, and a hint to check
   `~/Library/Logs/op-forward.log` or run `launchctl kickstart -k
   gui/$(id -u)/com.op-forward.daemon`.

Points to know:

- The endpoint polled in step 6 is resolved from `OP_FORWARD_LISTEN` and
  `OP_FORWARD_PORT` in the environment of the `update` command. If the daemon
  was installed with a different endpoint, set the same variables when running
  `update`.
- The daemon is expected to be restarted by a supervisor. launchd's `KeepAlive`
  does so; a daemon started by hand with `serve` is stopped by the signal and not
  restarted, in which case the wait reports a failure.
- launchd throttles respawns, so the daemon is typically unreachable for a few
  seconds. This is why the command waits instead of returning immediately.
- Installations from a package manager or Homebrew should be updated with that
  tool. The binary of a package lives in `/usr/bin`, which a normal user cannot
  write.

## doctor

```
op-forward doctor [--json] [--interactive] [--no-refresh] [--role auto|client|host]
                  [--addr EP] [--listen EP] [--host H] [--port N]
```

Diagnoses the op-forward chain layer by layer and prints which layer, if any, is
broken. It never runs an authenticated 1Password command unless `--interactive`
is given. **By default it refreshes an expired access token**: see
[Token renewal](#token-renewal) below; `--no-refresh` makes the run strictly
read-only.

| Flag | Default | Meaning |
|---|---|---|
| `--json` | off | Print the report as JSON (see below) |
| `--interactive` | off | Also run an authenticated read to verify the 1Password session. **This may raise a biometric prompt on the host**, so it is never the default |
| `--no-refresh` | off (doctor refreshes) | Do not renew an expired access token; never call `/token/refresh` and never write a token file. `daemon_op` is then skipped when the access token is unusable |
| `--role` | `auto` | `auto` treats a machine with an op-forward shim at `~/.local/bin/op` as a client, otherwise a machine with a real `op` on `PATH` as the host, otherwise a client. `client` and `host` force the role |
| `--addr EP`, `--host H`, `--port N` | as for `proxy` | Endpoint to check in the client role |
| `--listen EP`, `--port N` | as for `serve` | Endpoint to check in the host role. `--addr` is also honored on a host; the `OP_FORWARD_ADDR` variable is not |

Checks, in order. Each reports `ok`, `warn`, `fail`, or `skip` with a one-line
action when not `ok`:

| Check id | What it verifies |
|---|---|
| `binary` | The running version |
| `shim` | `~/.local/bin/op`: current, stale (version differs from the binary), unstamped (installed before stamping existed), not an op-forward shim, or absent. Stale and unstamped are warnings: re-run `op-forward install` |
| `endpoint` | The endpoint resolves and accepts connections. Failure: start the daemon, check the SSH tunnel |
| `daemon_health` | `GET /health` answers `{"status":"ok"}` |
| `daemon_op` | What the daemon reports at [`/health/op`](protocol.md#op-health-endpoint) about the host's own `op` integration |
| `tokens` | Which token files exist, whether they are expired, whether a refresh token is present, and whether doctor just renewed the access token. Values are never printed |
| `op_cli`, `op_integration`, `op_session` | Host role only: the same classification computed locally. Skipped, with a note, in the client role |

### Token renewal

Access tokens last one hour, so on an idle machine, a fresh VM or a fresh
container the access token is usually missing or expired while a valid refresh
token is present. `/health/op` needs an access token, and it is the most useful
remote check, so by default doctor renews the token instead of skipping the
check: it calls the daemon's `/token/refresh` exactly as `op-forward proxy`
does (the same code), then saves the new pair in the same files with the same
permissions (0600 files, 0700 directory). It does this when the access token
is missing or expired, and once more if the daemon rejects an access token that
still looked valid (a restarted daemon).

This is ordinary protocol traffic and changes no 1Password or user data, but it
does change state: the daemon **rotates** the refresh token, so any other
machine holding a copy of the old token files stops being accepted until it is
redeployed (see [tokens.md](tokens.md)), exactly as when the proxy refreshes.
Run with `--no-refresh` to avoid that.

When doctor renewed the token, the `daemon_op` summary starts with `renewed the
access token from the refresh token`, the `tokens` summary says doctor renewed
it, and both carry `"token_refreshed": true` in `data`. If the renewal fails,
that is the finding (status `fail`, with the daemon's reason) in both `daemon_op`
and `tokens`, with the deployment action `copy refresh.token from the host`.
With no valid refresh token to renew from, both also fail with that action. If
the daemon rotated the tokens but they could not be written locally, doctor
says so, because this machine then holds a refresh token the daemon no longer
accepts. Token values are never printed.

A failure is reported against the lowest broken layer, and the layers above it
are skipped instead of failed: an unreachable daemon is an `endpoint` failure
and never a 1Password failure, and a failed host session is reported as
host-side without implicating the tunnel.

**The session probe, and why it is not `op whoami`.** When the 1Password
desktop app is locked, `op whoami` exits 1 immediately with `account is not
signed in`: the same wording as a genuinely signed-out account. A health check
built on it cannot tell a locked app from a signed-out one, and it sent a long
investigation down the wrong path when the app was merely locked. The probe is
an authenticated read (`op vault list`) whose output is discarded unread.
`op` has no flag or environment variable that makes the desktop-app
authorization fail instead of prompting (the prompt comes from the 1Password
app, not from `op`), so by default doctor stops after the metadata checks
(`op --version`, `op account list`) and reports `op_session` as `skip` with the
command to run. `--interactive` runs the probe; in the client role it asks the
daemon to run it (`/health/op?probe=1`), in the host role it runs it locally and
queries the daemon for metadata only, so one run raises at most one prompt.

The probe waits up to 20 seconds for an answer: long enough to unlock the app
or approve a Touch ID prompt, short enough not to hang a health check. Because
a locked app gives no answer at all, a locked app always spends the full wait,
so `--interactive` prints a notice on stderr before it starts (stdout stays
valid JSON with `--json`) and the daemon logs the same notice on the host.

**Locked app.** A probe that gets no output before its timeout, on a host where
`op account list` answered, is reported as `app_locked`: the 1Password app on
the host is locked or an approval is pending there. Unlock it, or approve the
prompt, on the host and retry. If `op account list` also failed, the report is
`timeout` instead and the fault is the integration, not the lock state. See
[Troubleshooting](deployment.md#locked-1password-app).

Exit status:

| Status | Meaning |
|---|---|
| 0 | No check failed (warnings and skipped checks do not change the status) |
| 1 | At least one check failed |
| 2 | Usage error: an unknown flag or an invalid `--role` |

`--json` prints one object with the keys `version`, `role`, `endpoint`,
`interactive`, `status` (`ok`, `warn`, or `fail`), `exit_code`, and `checks`. Each
check has `id`, `status`, `summary`, optionally `action`, and optionally `data`
(booleans, counts, versions, and classification strings only). The check ids
above are stable and always present, in that order.

## version

```
op-forward version
```

Prints `op-forward <version>`, where the version is stamped at build time or is
`dev` for an unstamped build.

## help

`op-forward help`, `--help`, and `-h` print the usage text, which lists the
subcommands above and the one-line architecture summary.

## See also

- [configuration.md](configuration.md) for variables and precedence
- [deployment.md](deployment.md) for end-to-end setup
- [protocol.md](protocol.md) for what `proxy` sends and receives
- [development.md](development.md) for how releases are produced
