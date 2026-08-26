#!/bin/bash
# Build an Alpine package repository from .apk files already copied into the tree.
# Usage: scripts/build-apk-repo.sh <repo-dir> <rsa-private-key> <key-name>
#
# Expects .apk files at <repo-dir>/apk/{x86_64,aarch64}/*.apk
#
# Produces:
#   <repo-dir>/apk/<arch>/APKINDEX.tar.gz   (signed)
#   <repo-dir>/apk/<key-name>.rsa.pub       (users install this to /etc/apk/keys)
#
# Clients add the base URL (<repo-dir>/apk) to /etc/apk/repositories; apk
# appends its own architecture to find <base>/<arch>/APKINDEX.tar.gz.
#
# Indexing and signing run inside an Alpine container because apk-tools and
# abuild are not packaged for the Ubuntu runner. apk derives the signature
# name from the private key's file name, so the key is mounted as
# <key-name>.rsa; <key-name> must match apk.signature.key_name in nfpm.yaml.

set -euo pipefail

REPO_DIR="$(cd "${1:?Usage: build-apk-repo.sh <repo-dir> <rsa-private-key> <key-name>}" && pwd)"
KEY_FILE="$(cd "$(dirname "${2:?}")" && pwd)/$(basename "$2")"
KEY_NAME="${3:?}"

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
mkdir -p "${REPO_DIR}/apk"

docker run --rm \
    -v "${REPO_DIR}/apk:/repo" \
    -v "${KEY_FILE}:/keys/${KEY_NAME}.rsa:ro" \
    -e KEY_NAME="$KEY_NAME" \
    alpine:latest sh -ec '
        apk add --no-cache -q abuild openssl >/dev/null
        openssl rsa -in "/keys/$KEY_NAME.rsa" -pubout -out "/repo/$KEY_NAME.rsa.pub" 2>/dev/null
        # apk index verifies each package signature against the installed
        # keys, which doubles as a check that every package was signed with
        # this key before it is published.
        cp "/repo/$KEY_NAME.rsa.pub" /etc/apk/keys/
        for ARCH in x86_64 aarch64; do
            [ -d "/repo/$ARCH" ] || continue
            cd "/repo/$ARCH"
            rm -f APKINDEX.tar.gz
            # apk downloads <pkgname>-<pkgver>.apk as recorded in the index,
            # so files must carry the Alpine name rather than the nfpm name.
            for PKG in ./*.apk; do
                NAME=$(tar -xzOf "$PKG" .PKGINFO 2>/dev/null | sed -n "s/^pkgname = //p")
                VER=$(tar -xzOf "$PKG" .PKGINFO 2>/dev/null | sed -n "s/^pkgver = //p")
                [ -n "$NAME" ] && [ -n "$VER" ] && [ "$PKG" != "./$NAME-$VER.apk" ] && mv "$PKG" "$NAME-$VER.apk"
            done
            apk index -q -o APKINDEX.tar.gz --rewrite-arch "$ARCH" ./*.apk
            abuild-sign -k "/keys/$KEY_NAME.rsa" APKINDEX.tar.gz
            echo "Generated: apk/$ARCH/APKINDEX.tar.gz"
        done
        chown -R "$(stat -c %u /repo):$(stat -c %g /repo)" /repo'

echo "Alpine repository generated at ${REPO_DIR}/apk"
