#!/bin/bash
# Build every published package repository (APT, RPM, Alpine) and the landing
# page that documents them.
#
# Usage: scripts/build-repositories.sh <repo-dir> <base-url> [repo-slug]
#
# The tree is first reconciled against the packages attached to this project's
# GitHub releases, so the result depends on the releases rather than on
# whatever the gh-pages branch happened to still contain. Building a release
# and repairing a lost branch are then the same operation, which is why both
# the release workflow and the republish workflow call this.
#
# Environment:
#   GH_TOKEN            — token for the GitHub CLI (required)
#   GPG_KEY_ID          — signing key for the APT and RPM metadata (optional)
#   NFPM_APK_KEY_FILE   — RSA key for the Alpine index; absent skips Alpine

set -euo pipefail

REPO_DIR="${1:?Usage: build-repositories.sh <repo-dir> <base-url> [repo-slug]}"
BASE_URL="${2:?Usage: build-repositories.sh <repo-dir> <base-url> [repo-slug]}"
REPO_SLUG="${3:-ekovshilovsky/op-forward}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${HERE}/sync-release-packages.sh" "${REPO_DIR}" "${REPO_SLUG}"

"${HERE}/build-apt-repo.sh" "${REPO_DIR}"
"${HERE}/build-rpm-repo.sh" "${REPO_DIR}" "${BASE_URL}"

if [ -n "${NFPM_APK_KEY_FILE:-}" ]; then
    "${HERE}/build-apk-repo.sh" "${REPO_DIR}" "${NFPM_APK_KEY_FILE}" op-forward
else
    echo "APK_RSA_PRIVATE_KEY not set; skipping the Alpine repository"
fi

cat > "${REPO_DIR}/index.html" <<INDEXEOF
<!DOCTYPE html>
<html><head><title>op-forward package repositories</title></head>
<body>
<h1>op-forward package repositories</h1>
<p>Forward 1Password CLI across SSH boundaries with biometric auth.
   Source and releases: <a href="https://github.com/${REPO_SLUG}">github.com/${REPO_SLUG}</a></p>
<h2>Debian / Ubuntu (APT)</h2>
<pre>
curl -fsSL ${BASE_URL}/key.gpg | sudo gpg --dearmor -o /usr/share/keyrings/op-forward.gpg
echo "deb [arch=\$(dpkg --print-architecture) signed-by=/usr/share/keyrings/op-forward.gpg] ${BASE_URL} stable main" | sudo tee /etc/apt/sources.list.d/op-forward.list
sudo apt-get update &amp;&amp; sudo apt-get install op-forward
</pre>
<h2>Fedora / RHEL / openSUSE (dnf, yum, zypper)</h2>
<pre>
sudo curl -fsSL ${BASE_URL}/rpm/op-forward.repo -o /etc/yum.repos.d/op-forward.repo
sudo dnf install op-forward
</pre>
<h2>Alpine (apk)</h2>
<pre>
sudo curl -fsSL ${BASE_URL}/apk/op-forward.rsa.pub -o /etc/apk/keys/op-forward.rsa.pub
echo "${BASE_URL}/apk" | sudo tee -a /etc/apk/repositories
sudo apk add op-forward
</pre>
<h2>Arch Linux, other distributions, macOS</h2>
<p>Arch packages (<code>.pkg.tar.zst</code>), static tarballs, and macOS builds are attached to each
   <a href="https://github.com/${REPO_SLUG}/releases">GitHub release</a>;
   Homebrew: <code>brew install ekovshilovsky/tap/op-forward</code>.</p>
</body></html>
INDEXEOF

echo "Package repositories built at ${REPO_DIR}"
