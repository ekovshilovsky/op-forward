# Configuration

op-forward is configured through environment variables and a small set of
command-line flags. This page lists every one, says which side reads it, and
defines the precedence rules. The logic lives in
[cmd/config.go](../cmd/config.go) and
[internal/endpoint/config.go](../internal/endpoint/config.go); token paths are
resolved in [internal/auth/token.go](../internal/auth/token.go).

"Daemon" means the process started by `op-forward serve` on the host. "Proxy"
means `op-forward proxy`, which the shim runs on the remote side.

## Precedence rules

The endpoint can be given as a full URL (`tcp://host:port` or
`unix:///path`) or as shorthand values (host and port). Each side applies the
same order of sources, from highest to lowest:

1. The typed endpoint flag
2. Typed shorthand flags
3. The environment's full endpoint variable
4. The environment's shorthand variables
5. Built-in defaults, used when nothing above is set

| | Daemon (`serve`) | Proxy (`proxy`) |
|---|---|---|
| 1. Typed endpoint flag | `--listen` | `--addr` |
| 2. Typed shorthand flags | `--port` | `--host`, `--port` |
| 3. Environment endpoint | `OP_FORWARD_LISTEN` | `OP_FORWARD_ADDR` |
| 4. Environment shorthand | `OP_FORWARD_PORT` | `OP_FORWARD_HOST`, `OP_FORWARD_PORT` |
| 5. Default | `tcp://127.0.0.1:18340` | `tcp://127.0.0.1:18340` |

The flags take their defaults from the environment variables, so the code tracks
which flags were actually typed. The reason is that an `OP_FORWARD_ADDR` exported
in a shell profile must not override a `--host` typed on the command line. Rules
that follow:

- If `--listen` or `--addr` is typed, it wins and the shorthand flags are
  ignored.
- If any shorthand flag is typed (and the endpoint flag is not), the environment
  endpoint is discarded and a TCP endpoint is built from host and port. The host
  and port come from the typed flags, or from the shorthand environment
  variables and defaults for any shorthand not typed.
- Otherwise the environment endpoint is used if set, and the shorthand values
  after that.

Examples (proxy side):

| Command line | Environment | Resulting endpoint |
|---|---|---|
| (none) | (none) | `tcp://127.0.0.1:18340` |
| (none) | `OP_FORWARD_PORT=19000` | `tcp://127.0.0.1:19000` |
| (none) | `OP_FORWARD_ADDR=unix:///run/op.sock`, `OP_FORWARD_PORT=19000` | `unix:///run/op.sock` |
| `--host box` | `OP_FORWARD_ADDR=unix:///run/op.sock` | `tcp://box:18340` |
| `--addr unix:///a.sock --host box` | any | `unix:///a.sock` |

The daemon never uses a host. `OP_FORWARD_HOST` names where a client connects
and is not read by `serve`, so the daemon cannot be told to bind a non-loopback
address by that route. A malformed endpoint string is an error on either side.

## Environment variables

| Variable | Read by | Default | Meaning |
|---|---|---|---|
| `OP_FORWARD_LISTEN` | Daemon; `service install`; `update` | unset | Endpoint the daemon binds: `tcp://127.0.0.1:PORT` or `unix:///absolute/path.sock`. Default value of `serve --listen` |
| `OP_FORWARD_ADDR` | Proxy | unset | Endpoint the proxy dials: `tcp://HOST:PORT` or `unix:///absolute/path.sock`. Default value of `proxy --addr` |
| `OP_FORWARD_PORT` | Daemon, proxy, `service install`, `update` | `18340` | TCP port shorthand. Non-numeric values and values below 1 are ignored in favor of the default |
| `OP_FORWARD_HOST` | Proxy | `127.0.0.1` | TCP host shorthand for dialing. Set to `host.docker.internal` for Docker Desktop containers |
| `OP_FORWARD_TOKEN_DIR` | Daemon, proxy | OS cache directory plus `op-forward` | Directory holding `access.token`, `refresh.token`, `session.token` |
| `OP_FORWARD_TOKEN_FILE` | Daemon, proxy | `<token dir>/access.token` | Full path of the access token file only |
| `OP_FORWARD_PROBE_TIMEOUT_MS` | Proxy | `500` | How long the proxy waits for the endpoint to accept a connection before giving up with exit 127. Values below 1 are ignored |
| `OP_FORWARD_FETCH_TIMEOUT_MS` | Proxy | `60000` | Default execution timeout the proxy requests (`--timeout`). Values below 1 are ignored |
| `OP_FORWARD_INSTALL_DIR` | `scripts/install.sh` | `$HOME/.local/bin` | Where the install script places the binary |
| `HOME` | `install`, `service` | from the environment | Locations of the shim, LaunchAgent, and logs. The launchd service sets its own `HOME` to the installing user's |
| `PATH` | `install` (to find the real `op`); daemon (to find `op` on the host) | from the environment | Under launchd the daemon's `PATH` is fixed in the plist |
| `XDG_CACHE_HOME` | Daemon, proxy (Linux) | unset | Moves the default token directory (`os.UserCacheDir`) |

Numeric variables are never allowed to be zero or negative, because for a port
or a duration those values are never intended; they fall back to the default
rather than being applied (`endpoint.IntFromEnv`).

The launchd service does not inherit the installing shell's environment. The
endpoint is resolved when `service install` runs and written into the plist as
`--listen`; token variables are not copied (see
[deployment.md](deployment.md#launchd-service)).

## Flags

| Command | Flag | Default | Meaning |
|---|---|---|---|
| `serve` | `--listen EP` | `$OP_FORWARD_LISTEN` | Listen endpoint; overrides `--port` |
| `serve` | `--port N` | `$OP_FORWARD_PORT` or 18340 | Loopback TCP port |
| `proxy` | `--addr EP` | `$OP_FORWARD_ADDR` | Daemon endpoint; overrides `--host` and `--port` |
| `proxy` | `--host H` | `$OP_FORWARD_HOST` or `127.0.0.1` | Daemon host |
| `proxy` | `--port N` | `$OP_FORWARD_PORT` or 18340 | Daemon port |
| `proxy` | `--timeout MS` | `$OP_FORWARD_FETCH_TIMEOUT_MS` or 60000 | Execution timeout requested from the daemon |
| `install` | `--port N` | `$OP_FORWARD_PORT` or 18340 | Accepted for compatibility and ignored; the shim reads the environment each time it runs |

Flags use Go's standard syntax: `-flag` or `--flag`, with `--flag=value` or
`--flag value`. `service`, `update`, and `version` take no flags. Full command
details are in [cli.md](cli.md).

## Defaults at a glance

| Setting | Value |
|---|---|
| Endpoint | `tcp://127.0.0.1:18340` |
| Access token lifetime | 1 hour |
| Refresh token lifetime | 30 days |
| Execution timeout | 60 s default, 300 s maximum |
| Probe timeout | 500 ms |
| Request body limit | 1 MiB |
| Launchd label | `com.op-forward.daemon` |
| Launchd log | `~/Library/Logs/op-forward.log` |
| Shim | `~/.local/bin/op` |
| Package binary | `/usr/bin/op-forward` |

## Platform paths

| Item | macOS | Linux |
|---|---|---|
| Token directory | `~/Library/Caches/op-forward` | `$XDG_CACHE_HOME/op-forward` if set, else `~/.cache/op-forward` |
| LaunchAgent plist | `~/Library/LaunchAgents/com.op-forward.daemon.plist` | not applicable |
| Daemon log under launchd | `~/Library/Logs/op-forward.log` | not applicable |
| Shim | `~/.local/bin/op` | `~/.local/bin/op` |
| Binary from a Linux package | not applicable | `/usr/bin/op-forward` |

The `service` commands work on macOS only. The daemon and proxy work on macOS and
Linux (the executor has implementations for both).

## Build-time values

| Value | Set by | Default |
|---|---|---|
| `cmd.Version` | `-ldflags "-X github.com/ekovshilovsky/op-forward/cmd.Version=X"` | `dev` |
| `daemon.MinClientVersion` | Source edit | `0.1.0` |

A binary built without the version flag reports `dev`, which the daemon exempts
from version checks and which `op-forward update` always treats as out of date.
See [development.md](development.md#build-and-test).

## See also

- [cli.md](cli.md) for command reference
- [transport.md](transport.md) for what each endpoint form means
- [tokens.md](tokens.md#file-locations-and-precedence) for token paths
- [deployment.md](deployment.md) for worked setups
