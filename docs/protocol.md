# HTTP protocol

The daemon speaks plain HTTP/1.1 with JSON bodies. The protocol is identical
over loopback TCP and over a Unix domain socket; see [transport.md](transport.md)
for how the connection is made. For a Unix endpoint the request URL host is the
placeholder `http://unix` and is ignored. The server is in
[internal/daemon/server.go](../internal/daemon/server.go), the request and result
types in [internal/executor/op.go](../internal/executor/op.go), and the client in
[cmd/proxy.go](../cmd/proxy.go).

## Endpoints

| Path | Methods | Authentication | Purpose |
|---|---|---|---|
| `/health` | any | none | Liveness check |
| `/health/op` | `GET` | access token | Report the host's `op` integration (see [below](#op-health-endpoint)) |
| `/op/execute` | `POST` | access token | Run an `op` command |
| `/token/refresh` | `POST` | refresh token | Exchange the refresh token for a new pair |

Any other path returns the standard library's `404 page not found`. Every route
and every unknown path sits behind the peer-uid middleware, so a Unix connection
from another user receives `403 forbidden` before any handler runs (see
[transport.md](transport.md#peer-credential-check)).

## Common headers

Request headers sent by the proxy:

| Header | Sent on | Meaning |
|---|---|---|
| `Authorization: Bearer <token>` | execute, refresh | Access token for execute; refresh token for refresh. The scheme name is case-sensitive |
| `Content-Type: application/json` | execute | The daemon does not require it |
| `X-Client-Version` | execute, refresh | The client's version string; the daemon evaluates it on execute only |
| `User-Agent: op-forward/<version>` | execute | Informational; the daemon does not read it |

Response headers set by the daemon:

| Header | Meaning |
|---|---|
| `Content-Type: application/json` | Set on JSON responses. Plain-text error replies from `http.Error` use the standard library's text content type |
| `X-Update-Available: <version>` | Set on execute responses when the client's version is older than the daemon's; the value is the daemon's version |

## Health endpoint

```
GET /health
```

Response, `200 OK`:

```json
{"status":"ok"}
```

No token is required and the HTTP method is not checked. The proxy does not call
it; `op-forward update` polls it after restarting the daemon, and it is the
simplest manual probe:

```bash
curl -s http://127.0.0.1:18340/health
curl -s --unix-socket "$HOME/Library/Caches/op-forward/op-forward.sock" http://unix/health
```

## Op health endpoint

```
GET /health/op[?probe=1]
```

Describes the host's 1Password CLI integration for `op-forward doctor`. It
requires the access token, like `/op/execute`: the body is limited to booleans,
counts, versions, and classification strings, but it describes host state and
starts `op` processes, so it is not offered to unauthenticated callers. The plain
`/health` route stays open and unchanged.

Response, `200 OK` (`401` without a valid access token, `405` for other methods):

```json
{
  "op_present": true,
  "op_version": "2.41.0",
  "integration": "reachable",
  "account_count": 1,
  "session": "not_probed",
  "session_reason": ""
}
```

| Field | Values |
|---|---|
| `op_present` | A real `op` (not an op-forward shim) was found on the daemon's `PATH` |
| `op_version` | Output of `op --version`; empty if it could not be run |
| `integration` | `reachable` (`op account list` answered), `unreachable`, or `unavailable` (no `op`) |
| `account_count` | Number of accounts listed, or `null` if unknown. No account identifiers are ever returned |
| `session` | `not_probed` (metadata only), `healthy` (an authenticated read succeeded), or `failed` |
| `session_reason` | For `failed`: `app_locked` (`op account list` answered but the authenticated read produced no output before the timeout: the app is locked or an approval is pending on the host), `timeout` (the integration itself did not answer), `not_signed_in`, `authorization_denied`, or `op_error`; otherwise empty |

By default only metadata commands run, which do not raise a biometric prompt, and
`session` is `not_probed`: that value must never be read as healthy. With
`probe=1` the daemon also runs an authenticated read (`op vault list`, output
discarded), which **may raise a prompt on the host**. One probe runs at a time; a
concurrent `probe=1` request receives `409`. The probe is never `op whoami`,
which reports `account is not signed in` both for a signed-out account and for a
locked app.

A `probe=1` request can take up to 20 seconds: a locked app never answers, so the
probe runs to its timeout before `app_locked` is returned. The HTTP reply cannot
show progress, so the daemon logs `health probe: waiting up to 20s for 1Password
on this host` when the wait begins. Callers should allow at least 40 seconds.

## Execute endpoint

```
POST /op/execute
```

### Execute request

```json
{
  "args": ["item", "get", "<uuid>", "--fields", "username"],
  "timeout_ms": 60000
}
```

| Field | Type | Meaning |
|---|---|---|
| `args` | array of strings | Arguments passed to `op`, without the leading `op` |
| `timeout_ms` | integer, optional | Execution timeout in milliseconds; see [Timeouts](#timeouts) |

Unknown fields are ignored. The request body is limited to 1 MiB; a larger body,
an empty body, or malformed JSON returns 400. Trailing data after the JSON value
is rejected.

The daemon applies these steps in order and stops at the first that fails:

1. Peer-uid check (Unix connections only) -> `403`.
2. Method must be `POST` -> `405`.
3. Bearer value must equal the current access token and the token must be
   unexpired -> `401`.
4. If `X-Client-Version` is present, the version check -> `426` when the client
   is below the minimum. See [Version negotiation](#version-negotiation).
5. Parse the body -> `400`.
6. Log the command (with redaction; see
   [security.md](security.md#7-audit-logging-and-redaction)).
7. Validate the arguments and run `op` ([security.md](security.md#5-argument-validation)).
   An executor failure -> `500`.

### Execute response

On any request that reaches the executor and completes, the status is `200 OK`
with:

```json
{"stdout":"...","stderr":"","exit_code":0}
```

| Field | Meaning |
|---|---|
| `stdout` | Everything `op` wrote; see [Output formatting](#output-formatting) |
| `stderr` | Empty for output produced by `op`; carries a message when the daemon itself rejects the request or the command times out |
| `exit_code` | `op`'s exit status, or one of the daemon-generated values in [Exit codes](#exit-codes) |

A request that fails argument validation is still a `200` response, with
`exit_code` 1 and the reason in `stderr`. The messages are:

| Condition | `stderr` |
|---|---|
| No arguments | `no arguments provided` |
| More than 64 arguments | `too many arguments: N (max 64)` |
| First argument is a blocked subcommand | `subcommand "signin" is not allowed through the proxy` |
| An argument longer than 4096 bytes | `argument too long: N bytes (max 4096)` |
| An argument contains one of the characters in the list in [security.md](security.md#5-argument-validation) | `argument contains disallowed character: "$"` |

### Execute errors

| Status | Body | Cause |
|---|---|---|
| 400 | `invalid request: <detail>` | Body missing, malformed, or over 1 MiB |
| 401 | `unauthorized` | Missing, malformed, wrong, or expired access token |
| 403 | `forbidden` | Unix peer uid differs from the daemon's uid |
| 405 | `method not allowed` | Method other than `POST`; checked before authentication |
| 426 | `{"error":"client version X is below minimum required version Y — please update with: op-forward update"}` | Client older than `MinClientVersion` |
| 500 | `execution error: <detail>` | The executor could not run `op`, for example `op CLI not found in PATH` or a failure to allocate the pseudo-terminal |

The 426 body is JSON; the others are plain text produced by `http.Error`.

## Refresh endpoint

```
POST /token/refresh
Authorization: Bearer <refresh token>
```

There is no request body.

### Refresh response

`200 OK`:

```json
{
  "access_token": "<64 hex characters>",
  "access_expires": "2026-10-16T15:30:00Z",
  "refresh_token": "<64 hex characters>",
  "refresh_expires": "2026-11-15T14:30:00Z"
}
```

All four fields are always present, and expiry values are RFC 3339. The
previous access and refresh tokens are invalid as soon as the response is
produced. Lifetimes and rotation semantics are in [tokens.md](tokens.md#refresh-endpoint-behavior).

### Refresh errors

| Status | Body | Cause |
|---|---|---|
| 401 | `unauthorized` (plain text) | No `Authorization: Bearer` header |
| 401 | JSON with `error` set to `refresh_token_expired` and a human-readable `message` | The bearer value does not match the daemon's current refresh token, or that token has expired |
| 403 | `forbidden` | Unix peer uid differs from the daemon's uid |
| 405 | `method not allowed` | Method other than `POST` |
| 500 | `internal error` | Token generation failed |

`X-Client-Version` is sent on refresh requests but not evaluated, so an
out-of-date client can still refresh.

## Status codes

| Code | Produced by | Meaning |
|---|---|---|
| 200 | all endpoints | Success, including validation failures on execute (see above) |
| 400 | execute | Unreadable request body |
| 401 | execute, refresh | Authentication failed |
| 403 | all routes | Peer uid mismatch (Unix transport only) |
| 404 | unknown path | No such route |
| 405 | execute, refresh | Wrong HTTP method |
| 426 | execute | Client below the minimum version |
| 500 | execute, refresh | Server-side failure |

The proxy treats every status other than 200 on execute as an infrastructure
failure: it prints `daemon returned HTTP N` and the body to stderr and exits 127
(426 prints only the body). A 401 on execute is handled first by a refresh and
one retry.

## Exit codes

The exit status a caller of the shim observes is determined as follows:

| Status | Source | Meaning |
|---|---|---|
| 0 and other values relayed from `op` | `exit_code` in the response | The status `op` returned on the host |
| 1 | `exit_code` in the response | The daemon rejected the request during argument validation; the reason is on stderr |
| 124 | `exit_code` in the response | The command exceeded its host-side timeout |
| 127 | the proxy | Infrastructure failure; the shim reacts by running the real `op` |
| 1 | the shim | Proxy returned 127 and no fallback `op` binary is recorded or executable; prints `op-forward: proxy unavailable and no fallback op binary` |
| 2 | the proxy | Command-line flag error (the proxy's flag parser exits with 2) |

The proxy returns 127 for: an unparsable endpoint, no token files, a daemon that
does not accept a connection within the probe timeout, a transport error during
the request, a failed refresh, a 426, any other non-200 status, and an
unparsable response body.

Two consequences follow from using 127 as the signal:

- The shim runs the real `op` after a 127 whatever the reason, including 403,
  426, and refresh failures. On a machine without a usable local `op`, the user
  sees the proxy's message on stderr followed by the shim's fallback error.
- If `op` itself exits with 127 on the host, the value is relayed unchanged and
  the shim cannot distinguish it from an infrastructure failure.

The shim logic is in the template in [cmd/install.go](../cmd/install.go).

## Output formatting

The executor starts `op` under a pseudo-terminal, so `op` sees a terminal on
standard input, output, and error. Three consequences:

- **Streams are merged.** A terminal has one output channel, so everything `op`
  writes arrives in `stdout`. `stderr` in the response is empty for output
  produced by `op`.
- **Terminal-style output passes through unchanged.** Because `op` believes it
  is attached to an interactive terminal, it may format output for a terminal,
  including ANSI color escape sequences. The daemon and the proxy do not strip or
  rewrite them; the escape sequences reach the client as part of `stdout`. A
  caller that needs plain text must remove escapes itself.
- **Output is carried as a JSON string.** `stdout` is a JSON string, so output
  must be valid UTF-8 to arrive intact. Invalid UTF-8 sequences are replaced
  during JSON encoding, which means binary output is not preserved exactly.
- **Newlines are not translated, in practice.** The executor turns off the
  terminal's output post-processing (OPOST) immediately after starting `op`, so
  `\n` is normally not converted to `\r\n`. The change is applied after the
  process has started and the result of the terminal calls is not checked, so
  there is a narrow window in which very early output could still be translated,
  and no translation is guaranteed if those calls fail.

Standard input is not forwarded. The request carries arguments only, so
subcommands that read from stdin do not receive the caller's input.

## Timeouts

| Setting | Default | Where |
|---|---|---|
| Execution timeout | 60 s when `timeout_ms` is absent or not positive | `executor.DefaultTimeout` |
| Maximum execution timeout | 300 s; larger requests are reduced to this | `executor.MaxTimeout` |
| Proxy `--timeout` / `OP_FORWARD_FETCH_TIMEOUT_MS` | 60000 ms | Sent as `timeout_ms` |
| Proxy HTTP client timeout | `timeout_ms` + 5 s | Gives the host time to answer after its own timeout fires |
| Probe timeout (`OP_FORWARD_PROBE_TIMEOUT_MS`) | 500 ms | Connection attempt before the request |
| Server read timeout | 30 s | Headers and body |
| Server write timeout | 310 s | Maximum execution timeout plus 10 s |
| Server idle timeout | 120 s | Keep-alive connections |

The execution clock starts before `op` runs and includes the time the user
takes to answer the biometric prompt. When the clock expires, the daemon
terminates `op` and returns `exit_code` 124. This is the contract; it matches
the convention of `timeout(1)`.

## Version negotiation

The proxy sends its version in `X-Client-Version`. If the header is present, the
daemon (on `/op/execute` only, after authentication) evaluates it with
`version.CheckCompatibility`
([internal/version/compare.go](../internal/version/compare.go)):

| Client version | Result |
|---|---|
| Empty, or `dev` | No check; the request proceeds |
| Below `MinClientVersion` | `426 Upgrade Required` with `X-Update-Available` set to the daemon's version |
| At or above the minimum but below the daemon's version | Request proceeds; response carries `X-Update-Available` |
| Equal to or above the daemon's version | Request proceeds; no header |

If the header is absent, the check is skipped entirely and no
`X-Update-Available` is sent.

`MinClientVersion` is a package variable in the daemon, currently `0.1.0`
([internal/daemon/server.go](../internal/daemon/server.go)). Raising it means
changing that value and releasing a new daemon; nothing reads it from the
environment or a flag.

Versions are compared as `major.minor.patch`. A leading `v` is ignored. Missing
components count as 0. A component that is not a plain integer, including a
pre-release suffix such as `1.2.3-rc1`, makes the whole version compare as
`0.0.0`. A daemon built without a version (`dev`) compares as `0.0.0`, so it
does not report an update to a client that is at or above the minimum version.
The one exception is the `426` response: when a client below the minimum
triggers it, the daemon sets `X-Update-Available` to its own version even if
that version is `dev`.

The proxy prints a hint on stderr whenever the header is present, even if the
request otherwise succeeds:

```
op-forward: update available (v0.7.2 → v0.7.3) — run: op-forward update
```

On a 426 it prints the response body and exits 127. The text goes to stderr, so
`op` output on stdout is unaffected.

## Sequences

Successful execute:

```
proxy                               daemon
  | POST /op/execute (access token)   |
  |---------------------------------->| peer check, bearer check, version check
  |                                   | validate, run op under a PTY
  |<----------------------------------| 200 {"stdout","stderr","exit_code"}
```

Access token expired or daemon restarted:

```
proxy                               daemon
  | POST /op/execute                  |
  |---------------------------------->|
  |<----------------------------------| 401
  | POST /token/refresh (refresh)     |
  |---------------------------------->| rotate both tokens, persist
  |<----------------------------------| 200 {new pair}
  | save files; POST /op/execute      |
  |---------------------------------->|
  |<----------------------------------| 200
```

Refresh rejected:

```
proxy                               daemon
  | POST /token/refresh               |
  |---------------------------------->|
  |<----------------------------------| 401 {"error":"refresh_token_expired",...}
  | prints "token refresh failed", exit 127
  | shim runs the real op
```

## See also

- [tokens.md](tokens.md) for token lifetimes and the refresh flow
- [security.md](security.md) for validation, logging, and limits
- [transport.md](transport.md) for the connection layer
- [cli.md](cli.md#proxy) for the proxy command's flags
