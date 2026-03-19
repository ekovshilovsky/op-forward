#!/bin/bash
# Build .deb packages for op-forward from pre-compiled binaries.
# Usage: scripts/build-deb.sh <version> <binary-dir> <output-dir>
#
# Expects binaries at:
#   <binary-dir>/op-forward_<version>_linux_amd64/op-forward
#   <binary-dir>/op-forward_<version>_linux_arm64/op-forward
#
# Produces:
#   <output-dir>/op-forward_<version>_amd64.deb
#   <output-dir>/op-forward_<version>_arm64.deb

set -euo pipefail

VERSION="${1:?Usage: build-deb.sh <version> <binary-dir> <output-dir>}"
BINARY_DIR="${2:?}"
OUTPUT_DIR="${3:?}"

mkdir -p "$OUTPUT_DIR"

for ARCH in amd64 arm64; do
    DEB_ARCH="$ARCH"
    SRC_DIR="${BINARY_DIR}/op-forward_${VERSION}_linux_${ARCH}"
    PKG_DIR=$(mktemp -d)

    # Directory structure for the .deb package
    mkdir -p "${PKG_DIR}/usr/local/bin"
    mkdir -p "${PKG_DIR}/DEBIAN"

    # Copy the binary
    cp "${SRC_DIR}/op-forward" "${PKG_DIR}/usr/local/bin/op-forward"
    chmod 755 "${PKG_DIR}/usr/local/bin/op-forward"

    # Control file — package metadata
    cat > "${PKG_DIR}/DEBIAN/control" <<CTRL
Package: op-forward
Version: ${VERSION}
Section: utils
Priority: optional
Architecture: ${DEB_ARCH}
Maintainer: Eugene Kovshilovsky <ekovshilovsky@users.noreply.github.com>
Homepage: https://github.com/ekovshilovsky/op-forward
Description: Forward 1Password CLI across SSH boundaries with biometric auth
 op-forward runs a daemon on the host and installs a transparent op shim on the
 remote side so that 1Password CLI commands inside VMs are forwarded to the host
 and authenticated via Touch ID.
CTRL

    # Post-install script — run op-forward install to set up the op shim
    cat > "${PKG_DIR}/DEBIAN/postinst" <<'POST'
#!/bin/bash
set -e
# Install the op shim for all interactive users
if [ -x /usr/local/bin/op-forward ]; then
    echo "Run 'op-forward install' to set up the op shim."
fi
POST
    chmod 755 "${PKG_DIR}/DEBIAN/postinst"

    # Build the .deb
    DEB_FILE="${OUTPUT_DIR}/op-forward_${VERSION}_${DEB_ARCH}.deb"
    dpkg-deb --build --root-owner-group "${PKG_DIR}" "${DEB_FILE}"

    rm -rf "${PKG_DIR}"
    echo "Built: ${DEB_FILE}"
done
