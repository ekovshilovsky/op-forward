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
op-forward version
op-forward help | --help | -h
```

Subcommands recognized by the dispatcher: `serve`, `install`, `proxy`,
`service`, `update`, `version`, and `help` (also `--help` and `-h`).

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

The shim is a Bash script (`#!/bin/bash`), so `/bin/bash` must exist on the
machine. If the recorded `op-forward` path is no longer executable (for example
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
