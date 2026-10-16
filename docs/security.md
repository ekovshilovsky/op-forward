# Security model

This page describes what op-forward is designed to protect, the layers it
applies, and what falls outside its protection. Behavior is described as the
code implements it today. The source for most of the checks is
[internal/daemon/server.go](../internal/daemon/server.go),
[internal/executor/op.go](../internal/executor/op.go), and
[internal/endpoint/](../internal/endpoint).

## Threat model

op-forward gives processes on a remote machine the ability to run `op` on the
host, as the host user, with the host's 1Password session. The assets at stake
are the secrets that `op` can read or change and any side effects `op` can have
on the host.

| Actor | Assumed trust | Considered by the design |
|---|---|---|
| Host operating system and the host user's own processes | Trusted | No. A process running as the host user can read the token files and talk to the daemon directly |
| 1Password desktop application and Touch ID | Trusted; the primary authorization boundary | Yes, as the gate on privileged operations |
| Other user accounts on the host | Untrusted | Yes: loopback binding, socket permissions, peer uid check, bearer token |
| Network peers of the host | Untrusted | Yes: TCP binds are loopback only |
| Remote machine (VM or container) | Partly trusted: it is allowed to ask, not to bypass approval | Yes: token authentication, argument validation, blocklist, Touch ID |
| Other users on the remote machine | Untrusted | Partly: token files are mode 0600 and a Unix socket's mode is set by sshd; a forwarded TCP port is open to every user on the VM |
| The SSH connection | Trusted for confidentiality | No separate encryption: the HTTP traffic is plain and relies on the tunnel |

## Defense layers

### 1. Loopback-only binding

TCP endpoints must have a loopback IP literal as the host (127.0.0.0/8 or
`::1`); anything else, including `localhost` and `0.0.0.0`, is refused at
startup. The daemon has no option to listen on another interface. See
[transport.md](transport.md#tcp-endpoints).

Unix socket endpoints must live in a directory owned by the daemon's user with
no group or other permission bits. The socket is created with mode 0600
(umask 0177 at creation, followed by an explicit `chmod`). A non-socket file at
the path is never replaced. A socket that answers a probe connection is treated as
live and is never taken over; one whose probe fails for any reason, including a
timeout or a permission error, is treated as stale and removed. See
[transport.md](transport.md#unix-socket-endpoints).

### 2. Peer credential check

On Unix connections the daemon reads the connecting process's uid from the
kernel (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS) and answers
`403 forbidden` if it differs from the daemon's own uid. The check wraps every
route, including `/health` and `/token/refresh`. It does not apply to TCP, and
it does not distinguish between users on a VM behind an `ssh -R` forward, because
the connecting process on the host is always the `ssh` client. See
[transport.md](transport.md#what-the-check-can-and-cannot-see).

### 3. Bearer tokens

`/op/execute` requires a valid, unexpired access token and `/token/refresh`
requires the current refresh token. Tokens are 32 random bytes, compared in
constant time, stored in files with mode 0600 in a directory created with mode
0700, and rotated on refresh. Access tokens last one hour and refresh tokens 30
days. The daemon accepts only its single in-memory pair. See
[tokens.md](tokens.md).

There is no limit on authentication attempts and no lockout. The 256-bit token
space is the protection against guessing.

### 4. No shell execution

The executor runs `op` directly with `exec.Command(opPath, args...)`. Arguments
are passed as an argument vector, never interpreted by a shell. The path to `op`
is found with `exec.LookPath("op")` in the daemon's environment; under the
launchd service the `PATH` is
`/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`.

### 5. Argument validation

`Request.Validate` rejects a request when:

- there are no arguments, or more than 64;
- any argument is longer than 4096 bytes;
- any argument contains a backtick, `$`, `|`, `;`, `&`, a newline, or a carriage
  return;
- the first argument is a blocked subcommand (next section).

A rejected request is answered with HTTP 200, `exit_code` 1, and the reason in
`stderr` (see [protocol.md](protocol.md#execute-response)). Because no shell is
involved, the metacharacter rule is a conservative extra layer. Its practical
effect is that arguments which legitimately contain those characters, such as an
item field value with `$` or `&`, cannot be sent through the proxy.

### 6. Blocked subcommands

| Subcommand | Reason recorded in the source |
|---|---|
| `signin` | Interactive authentication must happen locally |
| `signout` | Would sign out the host's session |
| `update` | Would update the host's `op` binary |
| `completion` | Shell completion is not useful over the proxy |

The comparison is made against the first argument only. An invocation whose
first argument is something else, such as a global flag, is compared as that
argument. All other subcommands are forwarded, including those that change
1Password data and those that execute programs or write files on the host (for
example `op run` and options that write to a file path).

### 7. Audit logging and redaction

Before executing, the daemon logs one line through Go's `log` package, with a
timestamp:

```
[op-forward] executing: op <arguments>
```

Rejected requests are logged too, because the line is written before validation.
Other log lines record executor errors, refused peer uids
(`refused connection from uid N (daemon runs as uid M)`), and token refreshes
(with expiry times, never token values). Output from `op` is not logged, and the
log does not record which client sent the request.

Redaction replaces the argument that follows `--password` or `-p` with
`[REDACTED]`. Other forms are logged as given, including `--password=value`, and
assignment-style arguments such as `field=value` that carry secrets.
`--reveal` is printed as is. Treat the log as sensitive when commands that carry
secrets in their arguments are forwarded.

Under the launchd service, standard output and standard error go to
`~/Library/Logs/op-forward.log`.

### 8. Biometric approval per call

Biometric approval is behavior of the 1Password desktop application and `op`,
not something op-forward enforces. The executor runs `op` under a pseudo-terminal
so that it sees an interactive session on all three standard streams, which
preserves the prompting behavior 1Password documents for a terminal session; the
executor's design note records that without a terminal, 1Password may reuse an
earlier approval and skip the prompt. This is the primary authorization boundary:
the daemon cannot approve a request on the user's behalf. How often a prompt
appears is controlled by the 1Password application's own settings, which
op-forward does not control or verify.

### 9. Resource limits

| Limit | Value |
|---|---|
| Request body | 1 MiB |
| Arguments per request | 64 |
| Bytes per argument | 4096 |
| Execution timeout | 60 s default, 300 s maximum |
| Server read / write / idle timeouts | 30 s / 310 s / 120 s |

There is no cap on concurrent requests and no rate limiting.

### 10. Version negotiation

The daemon can reject clients older than `MinClientVersion` with `426`. The check
runs only when the client sends `X-Client-Version`, so it is advisory and not an
access control. See [protocol.md](protocol.md#version-negotiation).

## What is not protected

- **A holder of valid tokens.** Anyone who can read the token files on a remote
  machine, or who can open the daemon's endpoint and present a token, can ask the
  daemon to run any non-blocked `op` subcommand as the host user. Touch ID is the
  remaining control.
- **Touch ID configuration.** If 1Password is configured so that `op` does not
  require approval for every invocation, forwarded commands run without a
  per-command prompt.
- **Unblocked subcommands.** The blocklist is short, matches the first argument
  only, and is not a policy engine. Write operations and subcommands that execute
  programs or write files are forwarded.
- **TCP peers on the host.** TCP connections carry no peer identity. Any local
  process that can reach the port is held back only by the bearer token.
- **Users on the VM.** A forwarded TCP port on the VM is open to every VM user;
  separation relies on token file permissions.
- **Transport confidentiality.** Requests, tokens, and output travel as plain
  HTTP. Over a tunnel or loopback this is as safe as the tunnel. The proxy sends
  its tokens to whatever endpoint it is configured with, with no certificate or
  host verification, so pointing `OP_FORWARD_ADDR` or `OP_FORWARD_HOST` at an
  untrusted host discloses them.
- **Secrets in arguments and output.** Arguments are logged with the limited
  redaction above, and `op` output (which can contain secrets) is returned to the
  caller, and may be shown or stored wherever the caller sends it.
- **Standard input.** It is not forwarded, so it is neither an exposure nor a
  capability.
- **Binary integrity.** `op-forward update` and `scripts/install.sh` download
  release archives over HTTPS from GitHub; neither verifies a separate checksum
  or signature. The package repositories are signed in different ways: RPM and
  Alpine packages carry per-package signatures (and the RPM and Alpine
  repository metadata is signed), while for APT the `Release` and `InRelease`
  files are signed and the individual `.deb` files are not. See
  [development.md](development.md#package-repositories).
- **Compromise of the host or the host user.** Out of scope.
- **Revocation without restart.** The daemon has no revocation call; see
  [tokens.md](tokens.md#revoking-access).

## Hardening checklist

- Keep the default loopback TCP binding, or use a Unix socket where the topology
  allows it, so that the kernel enforces who can connect.
- Leave `StreamLocalBindMask` restrictive on VMs that receive a forwarded socket,
  and set `StreamLocalBindUnlink yes` only for the stale-socket case described in
  [transport.md](transport.md#forwarding-a-unix-socket).
- Verify the token directories are mode 0700 on both sides and the files 0600.
- Deploy only `refresh.token` to a remote machine; the access token is obtained
  by refresh.
- Keep 1Password configured to require approval for each `op` invocation.
- Review `~/Library/Logs/op-forward.log` and restrict who can read it.
- Follow the procedure in [tokens.md](tokens.md#revoking-access) after removing a
  machine from the set of trusted clients.
- Use package-manager updates on Linux hosts, where the repositories are signed
  (see [What is not protected](#what-is-not-protected) for how).
- Do not point a client at a host you do not control.

## See also

- [transport.md](transport.md) for the endpoint and peer checks
- [tokens.md](tokens.md) for the authentication lifecycle
- [protocol.md](protocol.md) for status codes and limits
- [architecture.md](architecture.md) for how the components fit together
