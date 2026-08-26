#!/bin/bash
# Install each built package in its native distribution container and run
# the binary. This is the acceptance test for the packaging pipeline.
# Usage: scripts/test-packages.sh <version> <package-dir> [arch]
#
# arch defaults to the host architecture. Arch Linux publishes amd64 images
# only, so its package is verified only when arch is amd64.
#
# Requires Docker. Packages are installed with signature checks disabled
# here; signature verification is exercised by the repository scripts. This
# test covers file layout, the postinstall script, and the binary itself.

set -euo pipefail

VERSION="${1:?Usage: test-packages.sh <version> <package-dir> [arch]}"
PKG_DIR="$(cd "${2:?}" && pwd)"
ARCH="${3:-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')}"

case "$ARCH" in
    amd64) RPM_ARCH=x86_64; ARCH_ARCH=x86_64 ;;
    arm64) RPM_ARCH=aarch64; ARCH_ARCH=aarch64 ;;
    *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

run() {
    local distro="$1" image="$2" install="$3"
    echo "== $distro ($ARCH) =="
    docker run --rm --platform "linux/$ARCH" -v "$PKG_DIR:/pkg:ro" "$image" sh -ec "
        $install
        test -x /usr/bin/op-forward
        OUT=\$(op-forward version)
        echo \"\$OUT\"
        case \"\$OUT\" in *\"$VERSION\"*) ;; *) echo 'version mismatch' >&2; exit 1 ;; esac"
}

run debian debian:bookworm-slim \
    "dpkg -i /pkg/op-forward_${VERSION}_${ARCH}.deb"
run fedora fedora:latest \
    "rpm -i --nosignature /pkg/op-forward-${VERSION}-1.${RPM_ARCH}.rpm"
run alpine alpine:latest \
    "apk add --allow-untrusted /pkg/op-forward_${VERSION}_${RPM_ARCH}.apk"
if [ "$ARCH" = amd64 ]; then
    run archlinux archlinux:latest \
        "pacman -U --noconfirm /pkg/op-forward-${VERSION}-1-${ARCH_ARCH}.pkg.tar.zst >/dev/null"
else
    echo "== archlinux: skipped (no official $ARCH image) =="
fi

echo "All package installs verified for $ARCH."
