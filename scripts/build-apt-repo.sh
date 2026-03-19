#!/bin/bash
# Build APT repository metadata from .deb packages already in the pool.
# Usage: scripts/build-apt-repo.sh <repo-dir>
#
# Expects .deb files at <repo-dir>/pool/*.deb
#
# Produces:
#   <repo-dir>/dists/stable/main/binary-{amd64,arm64}/Packages{,.gz}
#   <repo-dir>/dists/stable/Release
#   <repo-dir>/dists/stable/InRelease       (if GPG_KEY_ID is set)
#   <repo-dir>/dists/stable/Release.gpg     (if GPG_KEY_ID is set)
#   <repo-dir>/key.gpg                      (if GPG_KEY_ID is set)
#
# Environment:
#   GPG_KEY_ID  — GPG key fingerprint for signing (optional but recommended)

set -euo pipefail

REPO_DIR="${1:?Usage: build-apt-repo.sh <repo-dir>}"

mkdir -p "${REPO_DIR}/dists/stable/main/binary-amd64"
mkdir -p "${REPO_DIR}/dists/stable/main/binary-arm64"

# Generate Packages index for each architecture
for ARCH in amd64 arm64; do
    PACKAGES_DIR="${REPO_DIR}/dists/stable/main/binary-${ARCH}"
    cd "${REPO_DIR}"
    dpkg-scanpackages --arch "${ARCH}" pool/ > "${PACKAGES_DIR}/Packages"
    gzip -9 -k -f "${PACKAGES_DIR}/Packages"
    cd - > /dev/null
    echo "Generated: dists/stable/main/binary-${ARCH}/Packages"
done

# Generate Release file
cat > "${REPO_DIR}/dists/stable/Release" <<RELEASE
Origin: op-forward
Label: op-forward
Suite: stable
Codename: stable
Architectures: amd64 arm64
Components: main
Description: op-forward APT repository — forward 1Password CLI across SSH boundaries
RELEASE

# Append checksums for all index files
cd "${REPO_DIR}/dists/stable"
{
    echo "MD5Sum:"
    for f in main/binary-*/Packages main/binary-*/Packages.gz; do
        [ -f "$f" ] && printf " %s %s %s\n" "$(md5sum "$f" | cut -d' ' -f1)" "$(wc -c < "$f" | tr -d ' ')" "$f"
    done
    echo "SHA256:"
    for f in main/binary-*/Packages main/binary-*/Packages.gz; do
        [ -f "$f" ] && printf " %s %s %s\n" "$(sha256sum "$f" | cut -d' ' -f1)" "$(wc -c < "$f" | tr -d ' ')" "$f"
    done
} >> Release
cd - > /dev/null

# Sign the Release file if a GPG key is available
if [ -n "${GPG_KEY_ID:-}" ]; then
    gpg --batch --yes --default-key "${GPG_KEY_ID}" --armor --detach-sign \
        --output "${REPO_DIR}/dists/stable/Release.gpg" "${REPO_DIR}/dists/stable/Release"
    gpg --batch --yes --default-key "${GPG_KEY_ID}" --armor --clearsign \
        --output "${REPO_DIR}/dists/stable/InRelease" "${REPO_DIR}/dists/stable/Release"
    gpg --armor --export "${GPG_KEY_ID}" > "${REPO_DIR}/key.gpg"
    echo "Signed Release and exported public key"
else
    echo "Warning: GPG_KEY_ID not set — Release file is unsigned"
    echo "Users will need [trusted=yes] in their sources.list"
fi

echo "APT repository metadata generated at ${REPO_DIR}"
