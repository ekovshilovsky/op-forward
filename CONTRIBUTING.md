# Contributing to op-forward

Thank you for your interest in op-forward. This guide describes how changes are
proposed, tested, and reviewed. By participating you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Discuss first

Please open an [issue](https://github.com/ekovshilovsky/op-forward/issues)
before starting significant work, so scope and design can be agreed before you
invest time. Small fixes and documentation corrections can go straight to a
pull request. Security vulnerabilities must not be filed as public issues; see
[SECURITY.md](SECURITY.md).

## Building

```bash
make build                     # build for the current platform
go build -o op-forward .       # equivalent, without version stamping
make build-all VERSION=0.0.0   # cross-compile darwin/linux, arm64/amd64
```

## Required checks

Before opening a pull request, all of the following must be clean:

```bash
gofmt -l .          # must print nothing
go vet ./...
go test ./...
```

## Test-first development

This project practices test-first development. A change lands together with a
test that failed before the change and passes after it. For a bug fix, begin
with the failing reproduction; for a feature, begin with the test that
specifies the behavior. Pull requests that change behavior without a
corresponding test will be asked to add one.

## Packaging changes

Changes to `nfpm.yaml`, `scripts/`, or anything that affects the Linux packages
and repositories must pass the package build and install tests. These require
Docker:

```bash
make build-all VERSION=0.0.0
scripts/build-packages.sh 0.0.0 dist pkg-out
scripts/test-packages.sh 0.0.0 pkg-out
```

## Exercising the release pipeline

The release workflow has a dry-run mode that builds everything without
publishing or creating a tag. Maintainers can run it with:

```bash
gh workflow run release.yml -f version=0.0.0
```

Maintainers may run this before merging packaging or release changes.

## Pull requests

* Keep commits focused: one logical change per commit, with a message that
  explains why the change is needed.
* Credit the origin of ideas. If your change builds on another person's issue,
  fork, or code, name them and link the source in the pull request body and in
  the relevant commit message. This repository has a history of doing so and
  intends to continue.
* Update the README when behavior, configuration, or commands change.

## Style

* Use American English in code, comments, documentation, and commit messages.
* Do not use emojis in code or documentation.
* Write comments that explain intent and constraints, not what the code
  already says.
