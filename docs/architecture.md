# Architecture

This page describes the components that make up op-forward, how a single `op`
command travels from a remote machine to the host and back, and why each piece
exists. For wire-level detail see [protocol.md](protocol.md); for the
transports see [transport.md](transport.md).

## The problem

The 1Password CLI (`op`) unlocks through the 1Password desktop application,
which on macOS means Touch ID. That chain depends on a graphical session, the
macOS security framework, and a biometric sensor. Inside a headless VM, a
container, or a remote SSH session none of those exist, so `op` cannot
authenticate there.

op-forward keeps `op` where it works, on the host, and makes the remote side
believe it has a local `op`. A small daemon on the host executes commands on
behalf of remote callers; a shim on the remote side intercepts every `op`
invocation and forwards it. Each forwarded command is executed by the real `op`
on the host, under a pseudo-terminal so that `op` behaves as it does in an
interactive terminal. Biometric prompting is the 1Password desktop application's
documented behavior, which op-forward preserves rather than enforces; how often a
prompt appears is controlled by 1Password's own settings.

## Components

| Component | Where it runs | Entry point | Responsibility |
|---|---|---|---|
| Shim | Remote machine | script written by `op-forward install` ([cmd/install.go](../cmd/install.go)) | Appears on `PATH` as `op`; delegates to `op-forward proxy`; falls back to the real `op` when the proxy reports an infrastructure failure |
| Proxy | Remote machine | `op-forward proxy` ([cmd/proxy.go](../cmd/proxy.go)) | Resolves the endpoint, probes the daemon, authenticates, refreshes tokens, relays output and exit status |
| Daemon | Host | `op-forward serve` ([cmd/serve.go](../cmd/serve.go), [internal/daemon/server.go](../internal/daemon/server.go)) | HTTP server exposing `/health`, `/op/execute`, `/token/refresh`; owns the in-memory token pair |
| Executor | Host (inside the daemon) | [internal/executor/op.go](../internal/executor/op.go) | Validates a request, runs `op` under a pseudo-terminal, captures output and exit status |
| Endpoint | Both sides | [internal/endpoint/](../internal/endpoint) | Parses `tcp://` and `unix://` endpoints, applies bind rules, performs the peer-uid lookup, builds HTTP clients |
| Auth | Both sides | [internal/auth/token.go](../internal/auth/token.go) | Token generation, on-disk format, file locations, legacy migration |
| Version | Daemon | [internal/version/compare.go](../internal/version/compare.go) | Compares client and server versions to produce update hints and the upgrade-required response |

The same `op-forward` binary provides every component. `serve` runs on the host
and `proxy` runs on the remote side; `install`, `service`, `update`, and
`version` are management commands. The full list is in [cli.md](cli.md).

## Request flow

```
remote machine                                     host
--------------                                     ----
 op item get x
   |
   v
 shim (~/.local/bin/op)
   |  exec: op-forward proxy -- item get x
   v
 proxy
   | 1. resolve endpoint, read tokens
   | 2. probe the endpoint (500 ms by default)
   | 3. POST /op/execute  ---- SSH tunnel or ---->  daemon
   |    Authorization: Bearer <access token>        | peer-uid check (Unix only)
   |    X-Client-Version: <version>                 | bearer check
   |                                                | version check
   |                                                | argument validation
   |                                                v
   |                                              executor
   |                                                | op item get x   (under a PTY)
   |                                                v
   |                                              1Password desktop app: Touch ID
   |  <---- 200 {"stdout","stderr","exit_code"} ----+
   v
 proxy writes stdout/stderr, exits with exit_code
   |
   v
 shim exits with the same status
```

Step by step:

1. The user, or a script, runs `op ...`. The shim script found first on `PATH`
   runs `op-forward proxy -- <args>`.
2. The proxy resolves the endpoint from flags and the environment (see
   [configuration.md](configuration.md#precedence-rules)), then reads the access
   token. If the access token is missing or expired it uses the refresh token
   instead (see [tokens.md](tokens.md#client-request-flow)).
3. The proxy probes the endpoint with a short connection attempt. If nothing
   accepts the connection, it exits with status 127 without sending a request.
4. The proxy sends `POST /op/execute` with the arguments and a timeout. On a 401
   it calls `POST /token/refresh`, stores the new tokens, and retries once.
5. The daemon applies its checks in a fixed order, described in
   [protocol.md](protocol.md#execute-endpoint), and hands valid requests to the
   executor.
6. The executor starts the host's `op` under a pseudo-terminal, collects the
   output, and returns it with the exit status.
7. The proxy prints the output and exits with `op`'s status. Status 127 is
   reserved for infrastructure failures; when the shim sees it, it executes the
   real `op` instead (see [protocol.md](protocol.md#exit-codes)).

## Process layout

### Host

On the host, one long-lived process, `op-forward serve`, listens on the
configured endpoint. On macOS it is normally supervised by launchd through the
LaunchAgent that `op-forward service install` writes (see
[deployment.md](deployment.md#launchd-service)). The daemon executes `op` as the
user that runs the daemon, so the daemon must run as the user whose
1Password desktop application should approve the request. The launchd
integration installs a per-user LaunchAgent for that reason.

State on the host is limited to the token files described in
[tokens.md](tokens.md#file-locations-and-precedence) and, for Unix endpoints, the
socket file. The daemon keeps one access token and one refresh token in memory.

### Remote

On the remote machine there is no long-lived process. The shim and proxy are
short-lived: each `op` invocation starts a shell script, which starts
`op-forward proxy`, which opens a connection, exchanges one request (plus one
refresh when needed), and exits. State on the remote side consists of the shim
file, the `op-forward` binary, and the token files.

## Package map

| Path | Contents |
|---|---|
| [main.go](../main.go) | Calls `cmd.Execute` and prints any returned error |
| [cmd/](../cmd) | Subcommand implementations: `root.go` (dispatch and usage), `serve.go`, `install.go`, `proxy.go`, `service.go`, `update.go`, and `config.go` (flag and environment precedence) |
| [internal/auth/](../internal/auth) | Token type, TTL constants, file helpers, `LoadOrGenerateRefresh`, `MigrateLegacyToken` |
| [internal/daemon/](../internal/daemon) | HTTP server, route table, peer-uid middleware, token refresh handler, log redaction |
| [internal/endpoint/](../internal/endpoint) | Endpoint parsing, listen rules, probe, HTTP client, peer credentials per platform |
| [internal/executor/](../internal/executor) | Request validation, blocked subcommands, PTY execution for macOS and Linux |
| [internal/version/](../internal/version) | Version parsing and compatibility evaluation |

## Why each piece exists

**A daemon on the host.** Touch ID is only available where the 1Password desktop
application runs. Executing `op` on the host is the only way to reach it, and a
resident process avoids paying a startup cost per command.

**A shim named `op`.** Scripts and tools inside the VM call `op` by name. Placing
a shim earlier on `PATH` makes forwarding transparent: no script needs to know
that op-forward exists.

**A proxy subcommand instead of shell logic.** JSON encoding, HTTP, token
refresh, and exit-code handling are done in Go, not in the shim script. The shim
only decides between "use the proxy's result" and "fall back".

**A fallback to the real `op`.** If the tunnel is down or tokens are missing, the
proxy exits with 127 and the shim runs the real `op` recorded at install time.
Machines that also have a working local `op` keep working when the tunnel drops.

**Two transports behind one endpoint type.** SSH port forwarding and Docker
Desktop's `host.docker.internal` route both terminate on loopback TCP. Unix
domain sockets give a kernel-permissioned endpoint that avoids port collisions
between VMs. The endpoint package hides the difference so the HTTP, token, and
JSON layers are identical for both. See [transport.md](transport.md).

**Bearer tokens in addition to the transport.** Loopback TCP is reachable by any
local process, and a forwarded port is reachable by any user on the VM.
Authentication therefore does not rely on the transport alone. See
[security.md](security.md#3-bearer-tokens).

**Two token tiers.** A short-lived access token limits how long a leaked
credential is useful, and a longer-lived refresh token lets clients recover from
daemon restarts without a manual redeploy. See [tokens.md](tokens.md).

**A pseudo-terminal for `op`.** The executor runs `op` with a full PTY so that it
behaves as in an interactive terminal on all three standard streams. The
consequences are that stdout and stderr arrive merged and that `op` may emit ANSI
color escapes; see [protocol.md](protocol.md#output-formatting).

**Argument validation and a subcommand blocklist.** The daemon accepts
arguments from a network peer, so it restricts what it will pass to `op`. See
[security.md](security.md#5-argument-validation).

**Version negotiation.** The daemon can tell clients that an update exists and
can refuse clients older than a configured minimum. See
[protocol.md](protocol.md#version-negotiation).

## See also

- [transport.md](transport.md) for endpoints and tunnels
- [tokens.md](tokens.md) for the authentication lifecycle
- [protocol.md](protocol.md) for the HTTP API
- [security.md](security.md) for the threat model
- [development.md](development.md) for building and releasing
