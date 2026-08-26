#!/bin/bash
# Build a dnf/yum repository from .rpm packages already copied into the tree.
# Usage: scripts/build-rpm-repo.sh <repo-dir> <base-url>
#
# Expects .rpm files at <repo-dir>/rpm/{x86_64,aarch64}/*.rpm
#
# Produces:
#   <repo-dir>/rpm/<arch>/repodata/                 (createrepo_c)
#   <repo-dir>/rpm/<arch>/repodata/repomd.xml.asc   (if GPG_KEY_ID is set)
#   <repo-dir>/rpm/op-forward.repo                  (dnf repo definition)
#
# Environment:
#   GPG_KEY_ID  GPG key fingerprint for signing repository metadata (optional)
#
# The public key is expected at <base-url>/key.gpg, which the APT repository
# script already exports; both repositories share one signing key.

set -euo pipefail

REPO_DIR="${1:?Usage: build-rpm-repo.sh <repo-dir> <base-url>}"
BASE_URL="${2:?}"

command -v createrepo_c >/dev/null || { echo "createrepo_c is required" >&2; exit 1; }

for ARCH in x86_64 aarch64; do
    DIR="${REPO_DIR}/rpm/${ARCH}"
    mkdir -p "$DIR"
    createrepo_c --update --quiet "$DIR"
    if [ -n "${GPG_KEY_ID:-}" ]; then
        gpg --batch --yes --default-key "${GPG_KEY_ID}" --armor --detach-sign \
            --output "${DIR}/repodata/repomd.xml.asc" "${DIR}/repodata/repomd.xml"
    fi
    echo "Generated: rpm/${ARCH}/repodata"
done

GPGCHECK=1
if [ -z "${GPG_KEY_ID:-}" ]; then
    GPGCHECK=0
    echo "Warning: GPG_KEY_ID not set; repository metadata and packages are unsigned"
fi

cat > "${REPO_DIR}/rpm/op-forward.repo" <<REPO
[op-forward]
name=op-forward
baseurl=${BASE_URL}/rpm/\$basearch
enabled=1
gpgcheck=${GPGCHECK}
repo_gpgcheck=${GPGCHECK}
gpgkey=${BASE_URL}/key.gpg
REPO

echo "RPM repository generated at ${REPO_DIR}/rpm"
