#!/bin/bash
# Ensure the repository's GitHub Pages site is enabled and served from the
# gh-pages branch.
#
# Usage: scripts/ensure-pages.sh <owner/repo>
#
# Pushing gh-pages does not publish anything by itself. When the Pages site is
# disabled -- never enabled, or switched off later -- the push still succeeds
# and every package URL answers 404, with nothing in the release log to say so.
# Making publication part of the release removes the silent dependency on a
# setting somebody has to remember.
#
# Environment:
#   GH_TOKEN — token for the GitHub CLI (required)

set -euo pipefail

REPO_SLUG="${1:?Usage: ensure-pages.sh <owner/repo>}"
SOURCE='{"source":{"branch":"gh-pages","path":"/"}}'

if gh api "repos/${REPO_SLUG}/pages" >/dev/null 2>&1; then
    printf '%s' "${SOURCE}" | gh api -X PUT "repos/${REPO_SLUG}/pages" --input - >/dev/null
    echo "Pages site already enabled; source pinned to gh-pages"
else
    printf '%s' "${SOURCE}" | gh api -X POST "repos/${REPO_SLUG}/pages" --input - >/dev/null
    echo "Pages site enabled, serving gh-pages"
fi
