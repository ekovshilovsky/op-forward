#!/bin/bash
# Populate a package repository tree from the packages attached to this
# project's GitHub releases.
#
# Usage: scripts/sync-release-packages.sh <repo-dir> [repo-slug]
#
# The gh-pages branch is a cache, not the record: it is derived entirely from
# artifacts that already exist on the releases. Rebuilding the tree from those
# releases is therefore what makes the published repositories recoverable. A
# gh-pages branch that is deleted, truncated, or never created is repaired by
# running this and regenerating the indexes, with no need to cut a version
# whose only purpose is to trigger a publish.
#
# Existing files are left alone, so this is safe to run against a populated
# tree and cheap when nothing is missing.
#
# Environment:
#   GH_TOKEN  — token for the GitHub CLI (required)

set -euo pipefail

REPO_DIR="${1:?Usage: sync-release-packages.sh <repo-dir> [repo-slug]}"
REPO_SLUG="${2:-ekovshilovsky/op-forward}"

mkdir -p "${REPO_DIR}/pool" \
         "${REPO_DIR}/rpm/x86_64" "${REPO_DIR}/rpm/aarch64" \
         "${REPO_DIR}/apk/x86_64" "${REPO_DIR}/apk/aarch64"

# Where each package kind belongs in the published tree. A pattern that no
# release matches is simply absent from that release.
place_asset() {
    case "$1" in
        *.deb)          echo "pool" ;;
        *.x86_64.rpm)   echo "rpm/x86_64" ;;
        *.aarch64.rpm)  echo "rpm/aarch64" ;;
        *_x86_64.apk)   echo "apk/x86_64" ;;
        *_aarch64.apk)  echo "apk/aarch64" ;;
        *)              echo "" ;;
    esac
}

STAGE="$(mktemp -d)"
trap 'rm -rf "${STAGE}"' EXIT

downloaded=0
present=0
for TAG in $(gh release list --repo "${REPO_SLUG}" --limit 200 --json tagName --jq '.[].tagName'); do
    for ASSET in $(gh release view "${TAG}" --repo "${REPO_SLUG}" --json assets --jq '.assets[].name'); do
        DEST_DIR="$(place_asset "${ASSET}")"
        [ -n "${DEST_DIR}" ] || continue
        if [ -f "${REPO_DIR}/${DEST_DIR}/${ASSET}" ]; then
            present=$((present + 1))
            continue
        fi
        # gh writes into the current directory, so each download lands in a
        # scratch area first and is moved into place only once it is complete.
        rm -rf "${STAGE:?}/dl"
        mkdir -p "${STAGE}/dl"
        if ! (cd "${STAGE}/dl" && gh release download "${TAG}" --repo "${REPO_SLUG}" --pattern "${ASSET}" >/dev/null 2>&1); then
            echo "  warning: could not download ${ASSET} from ${TAG}"
            continue
        fi
        mv "${STAGE}/dl/${ASSET}" "${REPO_DIR}/${DEST_DIR}/${ASSET}"
        echo "  recovered ${DEST_DIR}/${ASSET} from ${TAG}"
        downloaded=$((downloaded + 1))
    done
done

echo "Package sync complete: ${downloaded} recovered, ${present} already present"
