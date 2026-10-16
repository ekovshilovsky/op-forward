# Security Policy

## Supported versions

Security fixes are provided for the latest release line only.

| Version | Supported |
|---|---|
| 0.7.x (latest) | Yes |
| Earlier than 0.7 | No |

Please upgrade to the latest 0.7.x release before reporting an issue, and
confirm it still reproduces.

## Reporting a vulnerability

Do not open a public issue for a security problem. Report it privately through
GitHub's private vulnerability reporting:

<https://github.com/ekovshilovsky/op-forward/security/advisories/new>

Please include the affected version (`op-forward version`), the transport in use
(`tcp://` or `unix://`), steps to reproduce, and the impact you expect. Do not
include real token values or vault contents.

op-forward is maintained by a small team on a best-effort basis. We aim to
acknowledge reports within one week and will keep you informed as a fix is
investigated and released. We do not operate a bug bounty program.

## Scope and trust model

In summary:

* Touch ID on the host is the authorization boundary; every privileged
  1Password operation requires biometric approval.
* The daemon binds only loopback TCP or a Unix domain socket with mode `0600`
  inside a `0700` directory.
* Requests must carry a valid bearer token.

The full threat model, including what op-forward does not protect against, is
in the [Security Model](README.md#security-model) section of the README.
Reports that depend on an already compromised host, or on Touch ID being
configured not to prompt, are generally out of scope.
