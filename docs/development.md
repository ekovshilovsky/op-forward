# Development

This page covers building and testing op-forward, the packaging pipeline, the
package repositories, and the release and recovery workflows. The current release
line is v1.x (latest tag v1.0.0).

## Requirements

| Tool | Needed for | Source of the requirement |
|---|---|---|
| Go 1.27 | Building and testing | `go 1.27.0` in [go.mod](../go.mod); CI reads the version from it |
| make | Convenience targets | [Makefile](../Makefile) |
| nfpm 2.47.0 | Building Linux packages | Pinned and checksummed in [release.yml](../.github/workflows/release.yml) |
| Docker | Package install tests, the Alpine repository, AUR verification | [scripts/test-packages.sh](../scripts/test-packages.sh), [scripts/build-apk-repo.sh](../scripts/build-apk-repo.sh), [scripts/build-aur.sh](../scripts/build-aur.sh) |
| `dpkg-scanpackages`, `createrepo_c`, `gpg`, `gh` | Building the APT and RPM repositories and syncing releases | [scripts/](../scripts) |

The module has two direct dependencies, `github.com/creack/pty` and
`golang.org/x/sys`. The daemon and proxy support macOS and Linux: the executor
has platform files for each ([pty_darwin.go](../internal/executor/pty_darwin.go),
[pty_linux.go](../internal/executor/pty_linux.go)).

## Build and test

```bash
make build                 # ./op-forward for this platform
make build VERSION=1.0.0   # stamp a version
make build-all VERSION=1.0.0
make test
make clean
```

- The Makefile's `VERSION` defaults to `0.3.0`, which does not track releases.
  Pass `VERSION` explicitly. A plain `go build .` produces a binary that reports
  `dev`.
- The version is stamped with
  `-ldflags "-X github.com/ekovshilovsky/op-forward/cmd.Version=$VERSION"`.
- `build-all` disables cgo and builds `darwin` and `linux` for `arm64` and
  `amd64` into `dist/op-forward_<version>_<os>_<arch>/op-forward`, plus a
  `.tar.gz` of each. With cgo off, the Linux binaries are static and run on
  musl-based distributions.

Checks to run before submitting a change:

```bash
gofmt -l .        # prints files that need formatting; should print nothing
go vet ./...
go test ./...
```

The CI workflow ([ci.yml](../.github/workflows/ci.yml)) runs `go vet ./...` and
`go test ./...` on pushes and pull requests to `main`. It does not run `gofmt`.

Test files sit beside the code they cover:

| Area | Tests |
|---|---|
| Endpoint parsing, loopback rule, Unix socket rules, peer uid | [internal/endpoint/](../internal/endpoint) |
| Tokens, file format, migration | [internal/auth/token_test.go](../internal/auth/token_test.go) |
| Daemon routes, auth, refresh, peer middleware, Unix serving | [internal/daemon/](../internal/daemon) |
| Validation, blocklist, PTY behavior | [internal/executor/op_test.go](../internal/executor/op_test.go) |
| Version comparison | [internal/version/compare_test.go](../internal/version/compare_test.go) |
| Flag precedence, shim fallback, plist rendering, update wait | [cmd/](../cmd) |

## Packaging

### nfpm definition

[nfpm.yaml](../nfpm.yaml) defines one package for every Linux format. It reads
`ARCH` and `VERSION` from the environment. It installs the binary to
`/usr/bin/op-forward` (mode 0755) and the license to each format's conventional
location, and runs [scripts/pkg/postinstall.sh](../scripts/pkg/postinstall.sh).
RPM packages are signed with the key in `NFPM_RPM_KEY_FILE`, and Alpine packages
with the RSA key in `NFPM_APK_KEY_FILE` (key name `op-forward`); an empty value
produces an unsigned package. The packages declare no dependencies.

Packages use `/usr/bin` because distribution policy reserves `/usr/local` for the
administrator. Releases up to 0.7.1 installed to `/usr/local/bin`, and the shim
written by those releases records that path. The post-install script rewrites the
recorded path in shims it finds under `/home/*/.local/bin/op` and
`/root/.local/bin/op`, identified by the header line `# op-forward shim`,
keeping ownership and mode.

### Building packages

```bash
make build-all VERSION=1.0.0
scripts/build-packages.sh 1.0.0 dist pkg-out
```

[scripts/build-packages.sh](../scripts/build-packages.sh) takes the version, the
directory holding `op-forward_<version>_linux_<arch>/op-forward` binaries, and an
output directory. For `amd64` and `arm64` it stages the binary at
`build/op-forward` (the fixed path `nfpm.yaml` references) and runs `nfpm package`
for each of `deb`, `rpm`, `apk`, and `archlinux`. Output names:

| Format | File name |
|---|---|
| Debian | `op-forward_<version>_<arch>.deb` (amd64, arm64) |
| RPM | `op-forward-<version>-1.<rpmarch>.rpm` (x86_64, aarch64) |
| Alpine | `op-forward_<version>_<rpmarch>.apk` |
| Arch | `op-forward-<version>-1-<archarch>.pkg.tar.zst` |

### Testing packages

```bash
scripts/test-packages.sh 1.0.0 pkg-out [arch]
```

[scripts/test-packages.sh](../scripts/test-packages.sh) is the acceptance test. For
Debian (`debian:bookworm-slim`), Fedora (`fedora:latest`), Alpine
(`alpine:latest`), and, on amd64 only, Arch Linux, it starts a container, installs
the package, checks that `/usr/bin/op-forward` is executable and that
`op-forward version` contains the version, and then runs the pre-0.7.2 shim check:
the container begins with a shim that records `/usr/local/bin/op-forward`, and
after installation that shim must reach the packaged binary (the proxy exits 127
because no daemon or tokens exist) and its owner must be unchanged. Signatures
are bypassed in this test; it covers file layout, the post-install script, and the
binary.

## Package repositories

The repositories are static files on the `gh-pages` branch, served by GitHub
Pages at `https://ekovshilovsky.github.io/op-forward`.

### Published layout

| Path | Repository | Contents |
|---|---|---|
| `/` | APT | `dists/stable/main/binary-{amd64,arm64}/Packages{,.gz}`, `dists/stable/Release`, `InRelease`, `Release.gpg`, `pool/*.deb`, `key.gpg` |
| `/rpm` | dnf, yum, zypper | `rpm/<arch>/*.rpm`, `rpm/<arch>/repodata/` (plus `repomd.xml.asc` when signed), `rpm/op-forward.repo` |
| `/apk` | apk | `apk/<arch>/*.apk`, `apk/<arch>/APKINDEX.tar.gz`, `apk/op-forward.rsa.pub` |
| `/index.html` | Landing page | Install commands for every repository |

### Building the repositories

[scripts/build-repositories.sh](../scripts/build-repositories.sh)
`<repo-dir> <base-url> [repo-slug]` is the single entry point. It runs, in order:

1. [sync-release-packages.sh](../scripts/sync-release-packages.sh): populates the
   tree from the packages attached to the project's GitHub releases (see
   [Recovery](#recovery)).
2. [build-apt-repo.sh](../scripts/build-apt-repo.sh): runs
   `dpkg-scanpackages --multiversion` for each architecture over `pool/`,
   compresses the index, writes `Release` with MD5 and SHA-256 sums, and, when
   `GPG_KEY_ID` is set, produces `Release.gpg`, `InRelease`, and `key.gpg`. The
   `--multiversion` flag matters: without it `dpkg-scanpackages` lists only the
   newest version of each package, which makes older versions in the pool
   unreachable. Without a key the metadata is unsigned.
3. [build-rpm-repo.sh](../scripts/build-rpm-repo.sh): runs `createrepo_c --update`
   for `x86_64` and `aarch64`, signs `repomd.xml` when a key is set, and writes
   `op-forward.repo` with `gpgcheck` and `repo_gpgcheck` enabled only if signed.
4. [build-apk-repo.sh](../scripts/build-apk-repo.sh): only when
   `NFPM_APK_KEY_FILE` is set. Inside an Alpine container it exports the public
   key, renames each package to `<pkgname>-<pkgver>.apk`, builds the index, and
   signs it.
5. Writes `index.html`.

Environment: `GH_TOKEN` (required), `GPG_KEY_ID`, `NFPM_APK_KEY_FILE`.

### Recovery

The `gh-pages` branch is derived data. Everything on it comes from artifacts
attached to the GitHub releases, so a branch that was deleted, truncated, or never
created can be rebuilt.

- `sync-release-packages.sh <repo-dir> [slug]` lists up to 200 releases with
  `gh release list`, and downloads each `.deb`, `.rpm`, and `.apk` asset that is
  not already present into `pool/`, `rpm/<arch>/`, or `apk/<arch>/`. Downloads go
  to a scratch area first and move into place only when complete; a failed
  download is reported and skipped. Existing files are left alone.
- [scripts/ensure-pages.sh](../scripts/ensure-pages.sh) `<owner/repo>` checks the
  repository's Pages configuration through the GitHub API. If the site already
  serves `gh-pages` from `/`, it does nothing. If the site is missing or serves
  something else, it tries to create or repoint it, and if the token lacks
  administration rights it fails with instructions (a one-time `gh api -X POST`
  command, or Settings, Pages, Source). The step exists because a pushed branch
  that Pages is not serving answers 404 with nothing in the logs.

The [republish workflow](#republish-workflow) runs the whole recovery on demand.

## AUR package

[scripts/build-aur.sh](../scripts/build-aur.sh) `<version> <out-dir> [--verify]`
writes a `PKGBUILD` for `op-forward-bin` that installs the static release tarballs
(x86_64 and aarch64) and the license, with SHA-256 sums computed from the
published release assets, then generates `.SRCINFO` with `makepkg` in an Arch
container. `--verify` additionally builds and installs the package in an
`archlinux` container on `linux/amd64`. The release must already be published
when this runs, because the checksums are read from it.

## Release workflow

[.github/workflows/release.yml](../.github/workflows/release.yml) runs on a
pushed tag matching `v*` and on manual dispatch (the dry run). Jobs:

1. `test`: `go test ./...`.
2. `release` (after `test`):
   1. Resolve the version: the tag name without `v`, or the dispatch input.
   2. Build binaries for linux and darwin on amd64 and arm64 with cgo disabled,
      `-trimpath`, and the version stamp; archive each as `op-forward_<version>_<os>_<arch>.tar.gz`.
      Windows (amd64 and arm64) is built the same way and archived as
      `op-forward_<version>_windows_<arch>.zip` containing `op-forward.exe`; Windows
      archives are attached to the release only, not to the Homebrew formula or
      the Linux package repositories.
   3. Fail unless every Linux binary is reported by `file` as statically linked.
   4. Create the GitHub release with the tarballs and generated notes.
   5. Update the Homebrew formula in the project's tap repository with the new
      version and the four checksums.
   6. Import signing keys: one PGP key for APT metadata, RPM packages, and RPM
      metadata, and an RSA key for Alpine (without it, Alpine packages are
      unsigned and the Alpine repository is not published).
   7. Install nfpm from its release tarball with a pinned checksum and run
      `scripts/build-packages.sh`.
   8. Run `scripts/test-packages.sh` for `amd64`.
   9. Attach the packages to the release.
   10. Generate the AUR definition and verify it; push it to the AUR when an AUR
       SSH key secret is configured, otherwise keep the generated files.
   11. Publish the package repositories: clone `gh-pages` (or initialize an empty
       branch if it does not exist), copy the new packages in, run
       `build-repositories.sh`, commit, and push.
   12. Run `ensure-pages.sh`.

The `gh-pages` existence check uses a command substitution on `git ls-remote`
rather than `ls-remote | grep -q`. The pipe form can die of SIGPIPE under
`pipefail` and be read as "branch missing", which would reinitialize the branch
and discard the pool; the comment in the workflow explains the case.

Secrets used: `GITHUB_TOKEN`, `HOMEBREW_TAP_TOKEN`, `APT_GPG_PRIVATE_KEY`,
`APT_GPG_KEY_ID`, `APK_RSA_PRIVATE_KEY`, and `AUR_SSH_PRIVATE_KEY`.

### Dry run

Dispatching the workflow manually asks for a version (default `0.0.0`); no tag is
created. A dry run uses the real signing secrets and performs steps 1 through 3
and 6 through 8 and the repository generation of step 11, but skips: creating
the release, the Homebrew update, attaching packages, the AUR step, the push to
`gh-pages`, and `ensure-pages.sh`. Packages and the generated repository tree
(excluding `.git`) are uploaded as the artifact `dry-run-<version>` with seven-day
retention. Because the repository step syncs from published releases, the dry run
exercises that path against the real release list.

## Republish workflow

[.github/workflows/republish-repositories.yml](../.github/workflows/republish-repositories.yml)
rebuilds the published repositories from the packages already attached to the
releases, without cutting a version. It runs on manual dispatch and requires the
input `confirm` to be exactly `republish`. Steps:

1. Check the confirmation.
2. Import signing keys. It stops if the PGP key is missing, because unsigned
   metadata would make existing installations reject the repository. The Alpine
   key is optional.
3. Clone `gh-pages` (or initialize it), then run `build-repositories.sh`, which
   syncs from the releases and regenerates every index.
4. Verify the APT metadata: `Release`, `InRelease`, and `key.gpg` must be
   non-empty, and the `amd64` and `arm64` `Packages` files must each list at least
   one package.
5. Commit and push `gh-pages`.
6. Run `ensure-pages.sh`, which is also how a disabled Pages site is detected.

Use it after a deleted or damaged `gh-pages` branch, a Pages site that was turned
off, or an index that omits published versions.

## Versioning and tagging

Releases are tags of the form `vMAJOR.MINOR.PATCH` (tags `v0.7.0` through `v0.7.3`
exist at this writing). Pushing the tag starts the release workflow, and the tag
without its `v` is stamped into the binaries and used in package versions. The
daemon's version comparison understands only three numeric components and treats
anything else, such as a pre-release suffix, as `0.0.0`, so use plain numeric tags
(see [protocol.md](protocol.md#version-negotiation)). Raising the minimum
accepted client version is a source change to `MinClientVersion` in
[internal/daemon/server.go](../internal/daemon/server.go).

## See also

- [architecture.md](architecture.md) for the code layout
- [deployment.md](deployment.md) for how users install the packages
- [cli.md](cli.md#update) for the self-update command
- [configuration.md](configuration.md#build-time-values) for build-time values
