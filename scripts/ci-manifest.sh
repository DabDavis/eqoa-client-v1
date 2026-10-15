#!/bin/bash
# Write manifest.json (what the updater reads) and notes.md (the release page)
# beside the binaries ci-build.sh made.
# Usage: scripts/ci-manifest.sh <version> <stable|dev> <release tag> <outdir>

set -euo pipefail
VERSION=$1 CHANNEL=$2 TAG=$3 OUT=$4
cd "$(dirname "$0")/.."

REPO_URL="https://github.com/DabDavis/eqoa-client-v1"
# The binaries are named by their own tag, never "latest": a manifest has to
# describe the files it was written beside, whichever release is newest now.
BASE="$REPO_URL/releases/download/$TAG"

# What changed, one line per commit, which is how the update popup shows it.
# A stable release lists everything since the previous stable tag (newest 12);
# the dev build, and the first stable release, the last five commits.
if [ "$CHANNEL" = stable ] && PREV=$(git describe --tags --abbrev=0 --match 'v*' HEAD^ 2>/dev/null); then
    CHANGELOG=$(git log --no-merges -n 12 --format='- %s' "$PREV..HEAD")
else
    CHANGELOG=$(git log --no-merges -n 5 --format='- %s')
fi

sha() { sha256sum "$1" | cut -d' ' -f1; }

jq -n \
    --arg version "$VERSION" \
    --arg channel "$CHANNEL" \
    --arg commit "$(git rev-parse --short HEAD)" \
    --arg linux_url "$BASE/eqoa-client" \
    --arg linux_sha256 "$(sha "$OUT/eqoa-client")" \
    --arg windows_url "$BASE/eqoa-client.exe" \
    --arg windows_sha256 "$(sha "$OUT/eqoa-client.exe")" \
    --arg changelog "$CHANGELOG" \
    '{version: $version, channel: $channel, commit: $commit,
      linux_url: $linux_url, linux_sha256: $linux_sha256,
      windows_url: $windows_url, windows_sha256: $windows_sha256,
      changelog: $changelog}' >"$OUT/manifest.json"

{
    if [ "$CHANNEL" = dev ]; then
        echo "Automatic build of the latest \`master\` commit ($VERSION). It may be ahead of the stable release and less tested; a copy downloaded from here keeps following dev builds."
    else
        echo "EQOA client $VERSION. A copy downloaded from here follows stable releases."
    fi
    echo
    echo "$CHANGELOG"
    echo
    echo "Download \`eqoa-client\` (Linux) or \`eqoa-client.exe\` (Windows). It is a single binary that updates itself on launch, and it needs your own EverQuest Online Adventures: Frontiers disc image (SLUS-20744): every game asset is read from the ISO at runtime."
} >"$OUT/notes.md"

cat "$OUT/manifest.json"
