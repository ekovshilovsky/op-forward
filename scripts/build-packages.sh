#!/bin/bash
# Build Linux packages (deb, rpm, apk, Arch) from pre-compiled binaries using nfpm.
# Usage: scripts/build-packages.sh <version> <binary-dir> <output-dir>
#
# Expects binaries at:
#   <binary-dir>/op-forward_<version>_linux_{amd64,arm64}/op-forward
#
# Produces, for each architecture:
#   <output-dir>/op-forward_<version>_<arch>.deb
#   <output-dir>/op-forward-<version>-1.<rpmarch>.rpm
#   <output-dir>/op-forward_<version>_<rpmarch>.apk
#   <output-dir>/op-forward-<version>-1-<archarch>.pkg.tar.zst
#
# Environment:
#   NFPM_RPM_KEY_FILE  armored PGP private key for signing RPMs (optional)
#   NFPM_APK_KEY_FILE  RSA private key for signing Alpine packages (optional)

set -euo pipefail

VERSION="${1:?Usage: build-packages.sh <version> <binary-dir> <output-dir>}"
BINARY_DIR="${2:?}"
OUTPUT_DIR="${3:?}"

command -v nfpm >/dev/null || { echo "nfpm is required: https://nfpm.goreleaser.com/install/" >&2; exit 1; }

mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"
export VERSION
export NFPM_RPM_KEY_FILE="${NFPM_RPM_KEY_FILE:-}"
export NFPM_APK_KEY_FILE="${NFPM_APK_KEY_FILE:-}"

# nfpm resolves content paths relative to the working directory and does
# not expand variables inside them, so run from the repository root and
# stage each architecture's binary at the fixed path nfpm.yaml references.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARY_DIR="$(cd "$BINARY_DIR" && pwd)"
cd "$ROOT"
mkdir -p build
trap 'rm -f build/op-forward' EXIT

for ARCH in amd64 arm64; do
    export ARCH
    cp "${BINARY_DIR}/op-forward_${VERSION}_linux_${ARCH}/op-forward" build/op-forward
    chmod 755 build/op-forward
    for PACKAGER in deb rpm apk archlinux; do
        nfpm package --config nfpm.yaml --packager "$PACKAGER" --target "$OUTPUT_DIR/"
    done
done

echo "Built packages in $OUTPUT_DIR:"
ls -1 "$OUTPUT_DIR"
