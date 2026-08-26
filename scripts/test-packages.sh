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

# Each container starts with a shim written by a release that installed the
# binary to /usr/local/bin, as every release before 0.7.2 did. The package
# must leave that shim working, since the person upgrading may not read the
# package manager's output and other users on the machine never see it.
OLD_SHIM='#!/bin/bash
# op-forward shim — forwards op commands to host daemon via SSH tunnel.
# Installed by op-forward. Do not edit directly.

REAL_OP=""
OP_FORWARD_BIN="/usr/local/bin/op-forward"

if [ -x "$OP_FORWARD_BIN" ]; then
  "$OP_FORWARD_BIN" proxy -- "$@"
  exit $?
fi
echo "op-forward: proxy unavailable and no fallback op binary" >&2
exit 1'

run() {
    local distro="$1" image="$2" install="$3"
    echo "== $distro ($ARCH) =="
    docker run --rm --platform "linux/$ARCH" -v "$PKG_DIR:/pkg:ro" -e OLD_SHIM="$OLD_SHIM" "$image" sh -ec "
        mkdir -p /home/alice/.local/bin
        printf '%s\n' \"\$OLD_SHIM\" > /home/alice/.local/bin/op
        chmod 755 /home/alice/.local/bin/op
        chown -R 1001:1001 /home/alice
        $install
        test -x /usr/bin/op-forward
        OUT=\$(op-forward version)
        echo \"\$OUT\"
        case \"\$OUT\" in *\"$VERSION\"*) ;; *) echo 'version mismatch' >&2; exit 1 ;; esac
        # The pre-0.7.2 shim must now reach the packaged binary. With no
        # daemon and no tokens the proxy exits 127; anything else means the
        # shim never found op-forward.
        RC=0; /home/alice/.local/bin/op --version >/dev/null 2>&1 || RC=\$?
        [ \"\$RC\" = 127 ] || { echo \"old shim exit=\$RC, want 127 (repaired shim reaching the packaged binary)\" >&2; exit 1; }
        OWNER=\$(stat -c %u /home/alice/.local/bin/op)
        [ \"\$OWNER\" = 1001 ] || { echo \"shim owner changed to \$OWNER\" >&2; exit 1; }
        echo 'pre-0.7.2 shim repaired, ownership preserved'"
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
