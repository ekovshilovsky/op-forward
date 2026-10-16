# Token model

op-forward authenticates every `/op/execute` request with a bearer token. The
tokens are credentials for the op-forward daemon only; they are unrelated to
1Password credentials, and they do not grant access to anything except the
ability to ask the daemon to run `op` (which still requires approval on the
host). The implementation is in [internal/auth/token.go](../internal/auth/token.go),
the client logic in [cmd/proxy.go](../cmd/proxy.go), and the refresh handler in
[internal/daemon/server.go](../internal/daemon/server.go).

## Two-tier design

| | Access token | Refresh token |
|---|---|---|
| Used for | `POST /op/execute` | `POST /token/refresh` |
| Lifetime | 1 hour (`AccessTokenTTL`) | 30 days (`RefreshTokenTTL`) |
| Generated | On every daemon start, and on every refresh | On first start, when the stored one is missing or expired, and on every refresh |
| File | `access.token` | `refresh.token` |
| Rotated by refresh | Yes | Yes |

Both tokens are 32 random bytes from `crypto/rand`, written as 64 hexadecimal
characters. The daemon holds exactly one access token and one refresh token in
memory. A request is authenticated by comparing the presented bearer value with
the in-memory value in constant time and checking that the token has not expired.
An access token is not accepted by `/token/refresh`, and a refresh token is not
accepted by `/op/execute`.

The short access lifetime limits the usefulness of a leaked access token. The
refresh token lets a client obtain new tokens after the daemon restarts or the
access token expires, without anyone redeploying files.

## File format

A token file has two lines:

```
<64 hexadecimal characters>
<expiry as RFC 3339, for example 2026-10-16T14:30:00-07:00>
```

Files are written atomically (to `<name>.tmp`, then renamed) with mode 0600, and
missing parent directories are created with mode 0700. The daemon and the proxy
write the same format, and the proxy treats a file without a parseable expiry
line as expired. The lifetime (TTL) itself is not stored on disk.

## File locations and precedence

The token directory is resolved as follows, identically on the daemon and the
proxy side (both call into the `auth` package):

1. `OP_FORWARD_TOKEN_DIR`, if set.
2. Otherwise the operating system's user cache directory plus `op-forward`:
   `~/Library/Caches/op-forward` on macOS, and `$XDG_CACHE_HOME/op-forward` or
   `~/.cache/op-forward` on Linux.

Within that directory:

| File | Contents |
|---|---|
| `access.token` | Access token |
| `refresh.token` | Refresh token |
| `session.token` | Legacy file; see [Legacy token migration](#legacy-token-migration) |

`OP_FORWARD_TOKEN_FILE`, if set, replaces the full path of the access token
file only. The refresh token and legacy file always live in the token
directory. Because the daemon and the proxy resolve paths the same way, setting
the variable affects where each side reads and writes its access token.

If the user cache directory cannot be determined (for example when `HOME` is
unset), the proxy falls back to `~/.cache/op-forward`.

The launchd service does not copy the installing shell's environment, so
`OP_FORWARD_TOKEN_DIR` and `OP_FORWARD_TOKEN_FILE` exported before
`op-forward service install` are not seen by the service. Only the endpoint is
resolved at install time (see [deployment.md](deployment.md#launchd-service)).

## Daemon startup

`op-forward serve` performs these steps in order
([cmd/serve.go](../cmd/serve.go)):

1. Resolve the endpoint. An invalid endpoint stops startup before any token is
   written.
2. Run `MigrateLegacyToken` (see below). A migration error is printed as a
   warning and startup continues.
3. Load the refresh token with `LoadOrGenerateRefresh`. If `refresh.token`
   exists, parses, and has not expired, it is reused with its stored expiry;
   otherwise a new refresh token is generated and saved. The daemon prints either
   `Refresh token reused (expires ...)` or `Refresh token generated (no valid
   existing token found)`, followed by the file path.
4. Generate a new access token, save it to the access token path, and print its
   path and expiry. A previously issued access token never survives a restart.
5. Write the same access token to `session.token`, ignoring failures, so that
   clients that still read the legacy file continue to work.
6. Open the listener and serve.

The daemon reads token files only at startup. After that, authentication uses
the in-memory values; editing or deleting the files on the host does not change
which tokens the running daemon accepts.

## Client request flow

`op-forward proxy` ([cmd/proxy.go](../cmd/proxy.go)) works as follows:

1. Read `access.token` (or the path in `OP_FORWARD_TOKEN_FILE`). If it is absent,
   read `session.token` instead.
2. If neither file yields a value, check that `refresh.token` has a value. If it
   does not, print the "no authentication token found" error and exit 127.
3. Probe the endpoint; exit 127 if nothing answers.
4. If an access token was read and its stored expiry is in the future, send the
   request with it.
5. If there was no usable access token, or the daemon answered 401, read
   `refresh.token` (falling back to the value in `session.token`) and call
   `POST /token/refresh`.
6. On success, write the new access token and new refresh token to their files
   and repeat the original request once with the new access token.
7. If the refresh fails, print the "token refresh failed" error and exit 127.

An access token known to be expired is not sent; the proxy refreshes first
instead of spending a round trip on a 401. The proxy does not update
`session.token`; only the daemon does.

## Refresh endpoint behavior

`POST /token/refresh` validates the bearer value against the daemon's current
refresh token (constant-time comparison plus an expiry check). On success, while
holding the daemon's token lock, it:

1. generates a new access token (1 hour) and a new refresh token (30 days);
2. writes `access.token`, `refresh.token`, and `session.token` on the host (a
   write failure is logged as a warning and does not fail the request);
3. replaces both in-memory tokens;
4. returns all four values (see [protocol.md](protocol.md#refresh-endpoint)).

The old refresh token and the old access token stop working at that moment.
The request and response shapes and status codes are in
[protocol.md](protocol.md#refresh-endpoint).

Because the daemon keeps a single token pair, rotation is global. Every client
that should keep working must end up holding the pair the daemon issued most
recently. Two clients that hold the same stale refresh token race: the first to
refresh succeeds and rotates the pair; the second presents a value the daemon no
longer holds and receives 401. The same applies to clients on different
machines that were each given a copy of the token files: after one of them
refreshes, the others hold tokens the daemon no longer accepts until new files
are deployed to them. Clients that share one token directory (for example the
same user account on one VM) see each other's refreshed files.

## Expiry

Token expiry is fixed when a token is issued:

- An access token expires one hour after it was generated. Nothing extends it;
  once it has expired the client refreshes.
- A refresh token expires 30 days after it was generated. Its expiry moves
  forward when `/token/refresh` issues a replacement, and at no other time. A
  daemon restart reuses the stored refresh token with its stored expiry.

`ShouldRenew` and `Renew` exist in the `auth` package, but neither the daemon
nor the proxy calls them. Use extends the refresh token's lifetime only
indirectly: any command issued after the access token has expired causes a
refresh, which issues a new 30-day refresh token. A client that issues no
commands for more than 30 days after its last refresh holds an expired refresh
token.

When the daemon's refresh token has expired and the daemon is not restarted,
refresh requests fail with `refresh_token_expired`. After a restart, the expired
token is replaced by a newly generated one, which clients do not have.

## Legacy token migration

Before the two-tier model, op-forward used a single token in `session.token`.
`MigrateLegacyToken` runs at daemon startup. If `refresh.token` does not exist
and `session.token` exists, parses, and has not expired, the legacy value and
expiry are saved as `refresh.token`. If `refresh.token` already exists, nothing
is migrated.

On the client side, the proxy accepts the legacy file in two roles:

- as an access token when `access.token` is missing (the daemon's access token
  differs from the legacy value, so the first request returns 401 and triggers a
  refresh);
- as a refresh token when `refresh.token` is missing, which succeeds when the
  daemon promoted that same value during migration.

The effect is that a VM holding only the old `session.token` recovers
automatically after the host is upgraded and restarted, and writes the new
`access.token` and `refresh.token` on its first request. New deployments should
use `refresh.token`; the legacy file exists for compatibility.

## Failure modes

### token refresh failed

The proxy prints `op-forward: token refresh failed — <reason>` and exits 127,
after which the shim falls back to the real `op`. Possible reasons and what
they mean:

| Reason shown | Cause |
|---|---|
| The `refresh_token_expired` message (starts `op-forward: authentication expired`) | The daemon returned 401 because the presented refresh value does not match its current refresh token, or its refresh token has expired. Typical causes: the daemon generated a new refresh token (its file was missing or expired at startup), the client holds a copy from another host, or another client refreshed first |
| `refresh request failed: ...` | The request could not be completed, for example the tunnel dropped between the probe and the refresh |
| `refresh returned HTTP N: ...` | The daemon returned another status, for example 403 from the peer check or 405 |
| `parsing refresh response: ...` | The reply was not valid JSON |

The message text stored in the daemon's 401 response refers to a refresh token
unused for more than 30 days and a restarted daemon; the condition that
produces the response is broader, as the first row states.

Remedy: copy the host's current `refresh.token` to the remote token directory,
for example:

```bash
scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token
```

No access token needs to be copied. On the next command the proxy sees no usable
access token, refreshes, and writes both files. Do not copy the file to several
machines expecting all of them to keep working; see [Refresh endpoint
behavior](#refresh-endpoint-behavior).

### no authentication token found

The proxy found no `access.token`, no `session.token`, and no `refresh.token`
value. Deploy `refresh.token` as above. The message says this is not a
1Password problem; it concerns the op-forward credential files.

### Daemon returns 401 for the access token

This is the normal signal to refresh and is handled silently. If the retry with
the new access token also returns a non-200 status, the proxy prints `daemon
returned HTTP 401` and exits 127.

## Revoking access

The daemon accepts only the tokens it holds in memory. To invalidate everything
a client may have copied:

1. Stop the daemon.
2. Delete `refresh.token` and `session.token` in the host's token directory.
   Leaving `session.token` in place would let the migration described above
   promote its value to the new refresh token.
3. Start the daemon. It generates a new refresh token and a new access token.
4. Deploy the new `refresh.token` to the clients that should keep access.

Restarting the daemon without deleting `refresh.token` invalidates every access
token but keeps the refresh token, so existing clients recover on their own.

## See also

- [protocol.md](protocol.md#refresh-endpoint) for the refresh request and response
- [security.md](security.md#3-bearer-tokens) for how tokens fit the threat model
- [deployment.md](deployment.md#token-deployment) for deploying the files
- [configuration.md](configuration.md) for the path variables
