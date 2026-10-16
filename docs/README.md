# op-forward documentation

op-forward forwards 1Password CLI (`op`) commands from VMs, containers, and
remote SSH sessions to a macOS host, where the real `op` runs and Touch ID
approves the request. The top-level [README](../README.md) is the quick start;
the pages here are the reference. They describe the code at the current release
line (v0.7.x), and where a page and the code disagree, the code is correct.

## Reading order

Read the quick start in [../README.md](../README.md) first, then follow this
order. Each page links to the others where topics overlap.

| Order | Page | What it covers |
|---|---|---|
| 1 | [architecture.md](architecture.md) | Components, request flow, and why each piece exists |
| 2 | [transport.md](transport.md) | The `tcp://` and `unix://` endpoints, bind rules, peer credential check, SSH forwarding, Docker Desktop |
| 3 | [tokens.md](tokens.md) | Access and refresh tokens, rotation, expiry, file locations, legacy migration |
| 4 | [security.md](security.md) | Threat model, defense layers, blocked subcommands, logging, and what is not protected |
| 5 | [protocol.md](protocol.md) | HTTP API, status codes, headers, exit codes, timeouts, version negotiation |
| 6 | [configuration.md](configuration.md) | Every environment variable and flag, with precedence rules |
| 7 | [deployment.md](deployment.md) | Host and remote setup, token deployment, SSH tunnels, troubleshooting |
| 8 | [cli.md](cli.md) | Subcommand reference: `serve`, `install`, `proxy`, `service`, `update`, `version` |
| 9 | [development.md](development.md) | Build, test, packaging, repositories, release and republish workflows |

To operate op-forward, the shortest path is deployment, then configuration, then
the troubleshooting section of deployment. To review it, read architecture,
transport, tokens, and security.

## Quick links

Find a symptom, then the section that explains it.

| Symptom or question | Go to |
|---|---|
| `daemon not reachable at ...` and the shim falls back to the real `op` | [deployment.md: Daemon not reachable](deployment.md#daemon-not-reachable) |
| `op` works but there is no Touch ID prompt | [deployment.md: Shim falling back](deployment.md#shim-falling-back-to-the-real-op) |
| `daemon returned HTTP 403` or `forbidden` | [transport.md: Peer credential check](transport.md#peer-credential-check), [deployment.md: 403 forbidden](deployment.md#403-forbidden) |
| `token refresh failed` or `refresh_token_expired` | [tokens.md: Failure modes](tokens.md#failure-modes), [deployment.md: 401 or token refresh failed](deployment.md#401-or-token-refresh-failed) |
| `no authentication token found` | [tokens.md: no authentication token found](tokens.md#no-authentication-token-found) |
| One VM stops working after another refreshes | [tokens.md: Refresh endpoint behavior](tokens.md#refresh-endpoint-behavior) |
| `refusing to bind to non-loopback address` | [transport.md: TCP endpoints](transport.md#tcp-endpoints) |
| `socket ... is already in use`, `not a socket`, or a socket directory error | [deployment.md: Stale sockets](deployment.md#stale-sockets-and-address-in-use), [transport.md: Unix socket endpoints](transport.md#unix-socket-endpoints) |
| `ssh -R` fails with `Address already in use` on a socket | [transport.md: Forwarding a Unix socket](transport.md#forwarding-a-unix-socket) |
| Unix socket path too long | [transport.md: Socket path length](transport.md#socket-path-length) |
| Using op-forward from a Docker Desktop container | [transport.md: Docker Desktop containers](transport.md#docker-desktop-containers), [deployment.md](deployment.md#docker-desktop-containers) |
| HTTP 426 or `below minimum required version` | [protocol.md: Version negotiation](protocol.md#version-negotiation), [deployment.md: 426](deployment.md#426-upgrade-required) |
| `op CLI not found in PATH` (HTTP 500) | [deployment.md: op not found on the host](deployment.md#op-not-found-on-the-host) |
| Color escape sequences or merged stdout and stderr in output | [protocol.md: Output formatting](protocol.md#output-formatting) |
| Exit status 127, 124, or 1 from `op` | [protocol.md: Exit codes](protocol.md#exit-codes) |
| A subcommand is rejected by the proxy | [security.md: Blocked subcommands](security.md#6-blocked-subcommands) |
| An argument is rejected for a character | [security.md: Argument validation](security.md#5-argument-validation) |
| Which settings take precedence | [configuration.md: Precedence rules](configuration.md#precedence-rules) |
| What the launchd service does and does not inherit | [deployment.md: launchd service](deployment.md#launchd-service) |
| `update` reports the daemon did not come back | [cli.md: update](cli.md#update), [deployment.md](deployment.md#daemon-did-not-come-back-after-update) |
| Rebuild the APT, RPM, or Alpine repositories | [development.md: Recovery](development.md#recovery), [Republish workflow](development.md#republish-workflow) |
| Revoke access for a remote machine | [tokens.md: Revoking access](tokens.md#revoking-access) |
| Where the threat model stops | [security.md: What is not protected](security.md#what-is-not-protected) |

## Source of truth

The pages are written from the source. These are the files they cite most:

| Topic | File |
|---|---|
| Subcommand dispatch | [cmd/root.go](../cmd/root.go) |
| Flag and environment precedence | [cmd/config.go](../cmd/config.go) |
| Daemon startup | [cmd/serve.go](../cmd/serve.go) |
| Proxy client and token flow | [cmd/proxy.go](../cmd/proxy.go) |
| Shim template and installer | [cmd/install.go](../cmd/install.go) |
| launchd service | [cmd/service.go](../cmd/service.go) |
| Self-update | [cmd/update.go](../cmd/update.go) |
| HTTP server, routes, peer middleware | [internal/daemon/server.go](../internal/daemon/server.go) |
| Endpoint parsing, bind rules, peer credentials | [internal/endpoint/](../internal/endpoint) |
| Tokens and file locations | [internal/auth/token.go](../internal/auth/token.go) |
| Validation, blocklist, PTY execution | [internal/executor/op.go](../internal/executor/op.go) |
| Version comparison | [internal/version/compare.go](../internal/version/compare.go) |
| Packaging | [nfpm.yaml](../nfpm.yaml), [scripts/](../scripts) |
| Workflows | [.github/workflows/](../.github/workflows) |
