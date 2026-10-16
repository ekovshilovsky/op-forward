# Transport

op-forward carries the same HTTP protocol over two transports: loopback TCP and
Unix domain sockets. A single endpoint value selects the transport for both the
daemon (what it binds) and the proxy (what it dials). The implementation is in
[internal/endpoint/](../internal/endpoint), with the parsing in
[endpoint.go](../internal/endpoint/endpoint.go), the listen rules in
[transport.go](../internal/endpoint/transport.go), and the environment handling
in [config.go](../internal/endpoint/config.go).

## Endpoint syntax

An endpoint is a string of one of two forms:

| Form | Example | Notes |
|---|---|---|
| `tcp://HOST:PORT` | `tcp://127.0.0.1:18340`, `tcp://[::1]:18340` | Host and port are both required; the port must be numeric; IPv6 literals are bracketed |
| `unix:///ABSOLUTE/PATH` | `unix:///Users/me/Library/Caches/op-forward/op-forward.sock` | The path must be absolute and non-empty; it is cleaned (`filepath.Clean`) before use |

Anything else is rejected, including a bare `host:port`, an `http://` URL, a
relative Unix path (`unix://relative/x.sock`), an empty string, and a TCP
endpoint with a trailing path such as `tcp://127.0.0.1:18340/op`. A malformed
value fails at startup rather than silently falling back to the default, so a
typo in an environment variable is visible immediately. The test cases are in
[endpoint_test.go](../internal/endpoint/endpoint_test.go).

How an endpoint is chosen from flags and environment variables, including the
shorthand `OP_FORWARD_HOST` and `OP_FORWARD_PORT`, is covered in
[configuration.md](configuration.md#precedence-rules).

## Choosing a transport

| Requirement | TCP | Unix socket |
|---|---|---|
| SSH `-R` port forwarding | Yes | Not applicable |
| SSH `-R` socket forwarding | Not applicable | Yes |
| Docker Desktop containers | Yes, through `host.docker.internal` | No; a host socket cannot be bind-mounted into the container |
| Several VMs on one host without port collisions | Needs one port per VM | One socket path per VM |
| Kernel-enforced access control on the endpoint | No | Yes (mode and directory ownership, peer uid) |
| Default | Yes (`tcp://127.0.0.1:18340`) | No |

The bearer-token check, the argument validation, and the JSON bodies are
identical over both transports.

## TCP endpoints

The daemon refuses to bind a TCP endpoint unless its host is a loopback IP
literal. The check is `Endpoint.IsLoopback`:

- `127.0.0.1`, any other `127.x.y.z` address, and `::1` are accepted.
- `0.0.0.0`, `::`, private addresses such as `10.0.0.1`, and names such as
  `host.docker.internal` are refused.
- `localhost` is refused too. A name is resolved by whatever the system resolver
  returns, which can be a routable address, so only IP literals count.

A refused bind fails with `refusing to bind to non-loopback address: ...`. The
`serve` command has no host option; `OP_FORWARD_HOST` names where a client
dials and the daemon never reads it. The only way to expose the daemon beyond
the machine is through a tunnel or Docker Desktop's route, both of which arrive
on loopback.

Dialing has no such restriction: the proxy accepts any `tcp://host:port`, which
is how a container reaches `host.docker.internal`.

TCP connections carry no peer credentials, so the peer-uid check described below
does not apply to them. Any local process that can open the port still has to
present a valid bearer token.

## Unix socket endpoints

`Endpoint.Listen` applies these rules when it binds a `unix://` endpoint.

### Directory requirements

1. The socket's parent directory is created with mode 0700 if it does not exist
   (`os.MkdirAll`). Only newly created directories receive that mode.
2. The parent directory must then pass `checkPrivateDir`:
   - it is a directory;
   - it has no group or other permission bits (`mode & 0077 == 0`);
   - it is owned by the uid of the daemon process.

   A shared directory such as `/tmp` fails the check with a message of the form
   `socket directory /tmp is mode 0777; it must be 0700 so other users cannot
   reach the socket`.

Only the immediate parent is checked. The check applies on the machine where
the daemon runs; the VM-side socket created by `ssh -R` is governed by sshd (see
[Forwarding a Unix socket](#forwarding-a-unix-socket)).

### Socket creation and permissions

The socket is created with the process umask set to 0177, so it exists with mode
0600 from the first instant instead of being tightened by a later `chmod`. The
umask is restored immediately after the bind, and the daemon then applies an
explicit `chmod 0600` as a second step. If the `chmod` fails, the listener is
closed and the daemon exits with an error.

The effect is that other users on the host cannot connect to the socket at all,
independent of the bearer token.

### Stale and live sockets

Before binding, the daemon inspects the path with `Lstat`:

| What is at the path | Result |
|---|---|
| Nothing | Bind proceeds |
| A socket whose probe connection fails | The file is removed and the bind proceeds (treated as a "stale socket") |
| A socket that accepts the probe connection within 200 ms | Refused: `socket PATH is already in use by another daemon` |
| Anything that is not a socket (regular file, directory, symlink) | Refused: `PATH exists and is not a socket; refusing to replace it`. The file is never modified or removed |

The distinction is made by a single probe, not by proof that nobody is
listening. Any probe failure leads to removal, whatever its cause: connection
refused, a timeout, or a permission error all count as "stale". A live listener
that answers the probe within 200 ms is never removed.

The daemon installs no signal handler, so a SIGTERM from `op-forward update`,
`launchctl`, or a plain `kill` ends the process without closing the listener
and leaves the socket file behind. The next start treats it as stale and
replaces it. Two daemons configured with the same path cannot both run: the
second one finds the first one live and exits.

### Socket path length

Unix socket paths have an operating-system limit: about 104 bytes on macOS
(the figure used in the source's error hint) and, per the platform's
`sun_path` definition, typically 108 bytes on Linux. When a bind
fails and the path is longer than 100 bytes, the error includes the hint
`socket paths are limited to about 104 bytes on macOS; choose a shorter path`.
For `ssh -R` forwarding, the limit applies to both ends independently, so both
the host path and the VM path must fit.

## Peer credential check

For Unix connections the daemon asks the kernel who is on the other end of each
accepted connection and compares that uid with its own. The lookup is in
[peercred.go](../internal/endpoint/peercred.go) with one implementation per
platform:

| Platform | Mechanism | File |
|---|---|---|
| Linux | `SO_PEERCRED` (`GetsockoptUcred`) | [peercred_linux.go](../internal/endpoint/peercred_linux.go) |
| macOS | `LOCAL_PEERCRED` (`GetsockoptXucred`) | [peercred_darwin.go](../internal/endpoint/peercred_darwin.go) |
| Other | No credentials available | [peercred_other.go](../internal/endpoint/peercred_other.go) |

The uid is read once per connection in the server's `ConnContext` hook and stored
in the request context. A middleware, `rejectForeignPeer`, wraps the whole route
table in [server.go](../internal/daemon/server.go), so every route, including
`/health` and `/token/refresh`, is covered, as well as unknown paths.

| Situation | Outcome |
|---|---|
| Peer uid equals the daemon's uid | Request proceeds to its handler |
| Peer uid differs | `403 forbidden`; the daemon logs `refused connection from uid N (daemon runs as uid M)` |
| No credentials available (TCP connection, or a platform without support) | Request proceeds to the bearer-token check |

The check is a second layer behind the 0600 mode and the 0700 directory. Its
purpose, as stated in the source, is to refuse another user even if the socket
file is exposed with looser permissions, for example through a shared directory
or a permissive `StreamLocalBindMask` in sshd.

### What the check can and cannot see

The kernel reports the identity of the process that opened the connection to
the daemon's socket. This follows from how OpenSSH forwards streams: over
`ssh -R` socket forwarding the host-side `ssh` client makes the local connection
to the daemon's socket, so the process the kernel reports is that `ssh` client
running as the host user, not the process on the VM that connected to the
forwarded socket. The arrangement was exercised end to end during development.
Consequently:

- Every forwarded request arrives with the host user's uid. The peer check
  passes for all VM users; it cannot distinguish one VM user from another.
- Access control between users on the VM is provided by the VM-side socket's
  permissions (set by the VM's sshd) and by the bearer token files, which are
  readable only by their owner when created by op-forward.
- The peer check is meaningful for processes that connect directly to the host
  socket, that is, other accounts on the host.

The repository's automated tests exercise the check with direct connections and
with injected peer uids ([peercred_test.go](../internal/endpoint/peercred_test.go),
[transport_test.go](../internal/daemon/transport_test.go)); they do not go
through SSH.

## SSH remote forwarding

### Forwarding a TCP port

```bash
# Run on the host. <vm> is an SSH destination.
ssh -R 18340:127.0.0.1:18340 <vm>
```

Per the OpenSSH documentation, the listening port is bound to the VM's loopback
interface unless `GatewayPorts` is enabled on the VM's sshd (it is off by
default). Any user on the VM can open
that port, so the bearer token is the only barrier between VM users. The VM
side needs no endpoint configuration if it uses the default port.

For long-lived tunnels:

```bash
ssh -fN -R 18340:127.0.0.1:18340 -o ControlMaster=no -o ControlPath=none <vm>
```

`ControlMaster=no` matters when multiplexing is configured elsewhere: per the
OpenSSH documentation, a multiplexed session reuses the first connection, so
`RemoteForward` is established only by the first connection.

### Forwarding a Unix socket

```bash
# Host: bind the daemon to a socket
export OP_FORWARD_LISTEN="unix://$HOME/Library/Caches/op-forward/op-forward.sock"
op-forward serve

# Host: forward it. The VM-side path is spelled out because the host shell
# expands $HOME and ~ before ssh sees them.
ssh -R /home/<vm-user>/.cache/op-forward/op-forward.sock:$HOME/Library/Caches/op-forward/op-forward.sock <vm>

# VM: point the proxy at the forwarded socket
export OP_FORWARD_ADDR="unix:///home/<vm-user>/.cache/op-forward/op-forward.sock"
```

Points to know:

- **Leftover sockets on the VM.** sshd does not remove a forwarded socket when
  the connection drops, so the next `ssh -R` fails with OpenSSH's `Address already
  in use` error. Set `StreamLocalBindUnlink yes` in the VM's `/etc/ssh/sshd_config` so
  sshd replaces the stale file.
- **Parent directory.** The VM-side directory must already exist; sshd does not
  create it.
- **VM-side permissions.** sshd creates the VM-side socket using its
  `StreamLocalBindMask` (per the OpenSSH documentation the default is 0177,
  owner only). Leave it restrictive.
- **Path lengths.** See [Socket path length](#socket-path-length).
- **Host-side rules.** The host path must satisfy the directory and permission
  rules above. `~/Library/Caches/op-forward` on macOS satisfies them because the
  daemon creates it with mode 0700 when it writes the token files, provided the
  directory did not already exist with looser permissions.

The `ssh` and `sshd` options named here are OpenSSH behavior as documented by
OpenSSH, not behavior implemented in this repository.

## Docker Desktop containers

Containers on Docker Desktop reach the host through the `host.docker.internal`
name. Point the proxy at it with TCP:

```bash
# Inside the container
export OP_FORWARD_HOST=host.docker.internal
# or the full form:
export OP_FORWARD_ADDR=tcp://host.docker.internal:18340
```

The daemon stays bound to `127.0.0.1`; Docker Desktop routes the name to the
host, so no SSH tunnel is needed. This is the case that motivated the dial-side
exception to loopback-only addressing. Reachability through `host.docker.internal`
was verified empirically against Docker Desktop during development.

Use TCP for containers. A Unix socket on the host cannot be bind-mounted into a
Docker Desktop container: `docker run` fails at mount time with `operation not
supported`, so a `unix://` endpoint cannot serve a container on that platform.
This too was verified empirically against Docker Desktop during development. The container still needs the token files; see
[deployment.md](deployment.md#docker-desktop-containers).

## Dial side behavior

The proxy builds an HTTP client from the endpoint. For TCP it uses the default
transport. For Unix endpoints it uses a custom dialer that ignores the host in
the request URL (the placeholder `http://unix`) and connects to the socket path.

Before any request, the proxy probes the endpoint by opening and closing a
connection with a timeout from `OP_FORWARD_PROBE_TIMEOUT_MS` (default 500 ms).
If the probe fails, the proxy prints `daemon not reachable at ENDPOINT` and exits
with status 127, which makes the shim fall back to the real `op`. The dial side
performs no directory or ownership checks on a Unix socket path.

The `update` command uses the same client to wait for `/health` after it
restarts the daemon (see [cli.md](cli.md#update)).

## See also

- [protocol.md](protocol.md) for what runs over the transport
- [security.md](security.md#1-loopback-only-binding) for how the transport rules fit the threat model
- [deployment.md](deployment.md#ssh-tunnel-patterns) for tunnel recipes and troubleshooting
- [configuration.md](configuration.md) for the variables that select an endpoint
