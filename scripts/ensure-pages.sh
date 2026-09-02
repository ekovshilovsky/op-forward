#!/bin/bash
# Ensure the repository's GitHub Pages site is enabled and served from the
# gh-pages branch.
#
# Usage: scripts/ensure-pages.sh <owner/repo>
#
# Pushing gh-pages does not publish anything by itself. When the Pages site is
# disabled -- never enabled, or switched off later -- the push still succeeds
# and every package URL answers 404, with nothing in the release log to say so.
# Checking here turns that silent failure into a failed run.
#
# A site that is already configured correctly is left untouched, because the
# workflow token can read the Pages configuration but not create or change it:
# creating a site needs administration rights that GITHUB_TOKEN does not carry
# even with pages: write. Writing unconditionally would therefore fail every
# healthy run.
#
# Environment:
#   GH_TOKEN — token for the GitHub CLI (required)

set -euo pipefail

REPO_SLUG="${1:?Usage: ensure-pages.sh <owner/repo>}"
SOURCE='{"source":{"branch":"gh-pages","path":"/"}}'

# Read through gh's own --jq so the result is plain text: gh pretty-prints and
# colorizes raw JSON when it believes it has a terminal, and those escape codes
# make a separate JSON parser fail on perfectly good output.
if CONFIG=$(gh api "repos/${REPO_SLUG}/pages" --jq '"\(.source.branch // "")\t\(.source.path // "")"' 2>/dev/null); then
    BRANCH=$(printf '%s' "${CONFIG}" | cut -f1)
    SOURCE_PATH=$(printf '%s' "${CONFIG}" | cut -f2)
    if [ "${BRANCH}" = "gh-pages" ] && [ "${SOURCE_PATH}" = "/" ]; then
        echo "Pages site is enabled and serving gh-pages"
        exit 0
    fi
    echo "Pages site serves ${BRANCH}:${SOURCE_PATH}; repointing it at gh-pages"
    if printf '%s' "${SOURCE}" | gh api -X PUT "repos/${REPO_SLUG}/pages" --input - >/dev/null 2>&1; then
        echo "Pages source repointed to gh-pages"
        exit 0
    fi
else
    if printf '%s' "${SOURCE}" | gh api -X POST "repos/${REPO_SLUG}/pages" --input - >/dev/null 2>&1; then
        echo "Pages site enabled, serving gh-pages"
        exit 0
    fi
fi

cat >&2 <<MESSAGE
::error::The GitHub Pages site for ${REPO_SLUG} is not serving gh-pages, and this
token cannot change that: creating or repointing a Pages site needs repository
administration rights, which GITHUB_TOKEN does not have even with pages: write.
The packages were published to the gh-pages branch, but nothing will serve them
until someone with administration rights runs, once:

  echo '${SOURCE}' | gh api -X POST repos/${REPO_SLUG}/pages --input -

or sets Settings -> Pages -> Source to the gh-pages branch.
MESSAGE
exit 1
