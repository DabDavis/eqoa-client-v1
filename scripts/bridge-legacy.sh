#!/bin/bash
# Move the testers still on a texturepacks.art build over to GitHub releases.
# Usage: source scripts/.deploy-secrets && scripts/bridge-legacy.sh [v-tag]
#
# Clients built before the switch to GitHub (BuildVersion like
# 20260802-d919518) poll https://texturepacks.art/eqoa/manifest.json, with
# Basic Auth, and nothing else. This re-hosts one GitHub stable release there,
# byte for byte, under a manifest in the format they read, so each of them
# updates once into a build that follows GitHub's stable channel from then on.
#
# Run it after the first stable release. It is harmless to run again for a
# newer one, and needless: nothing GitHub built reads texturepacks.art.
# Needs the VPN (10.8.0.1) and `gh`. Defaults to the latest stable release.

set -euo pipefail
: "${EQOA_UPDATE_USER:?Set EQOA_UPDATE_USER (source scripts/.deploy-secrets)}"
: "${EQOA_UPDATE_PASS:?Set EQOA_UPDATE_PASS (source scripts/.deploy-secrets)}"

REPO=DabDavis/eqoa-client-v1
TAG=${1:-$(gh release view --repo "$REPO" --json tagName -q .tagName)}
REMOTE=10.8.0.1
UPDATE_DIR=/home/sdg/www/tpacks.art/eqoa/eqoa
DOWNLOAD_DIR=/home/sdg/www/tpacks.art/eqoa/eqoa-client

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "=== Bridging $TAG to texturepacks.art ==="
gh release download "$TAG" --repo "$REPO" --dir "$WORK" \
    --pattern eqoa-client --pattern eqoa-client.exe --pattern manifest.json

# Only a stable build: a dev build would move every legacy tester onto the dev
# channel for good.
jq -e '.channel == "stable"' "$WORK/manifest.json" >/dev/null \
    || { echo "$TAG is not a stable release" >&2; exit 1; }
(
    cd "$WORK"
    echo "$(jq -r .linux_sha256 manifest.json)  eqoa-client" | sha256sum -c
    echo "$(jq -r .windows_sha256 manifest.json)  eqoa-client.exe" | sha256sum -c
)

jq '.linux_url = "https://texturepacks.art/eqoa/eqoa-client"
  | .windows_url = "https://texturepacks.art/eqoa/eqoa-client.exe"' \
    "$WORK/manifest.json" >"$WORK/legacy-manifest.json"

# The binaries first and the manifest last, as the workflow does.
ssh "$REMOTE" "mkdir -p $UPDATE_DIR $DOWNLOAD_DIR"
scp "$WORK/eqoa-client" "$WORK/eqoa-client.exe" "$REMOTE:$UPDATE_DIR/"
ssh "$REMOTE" "cp $UPDATE_DIR/eqoa-client $UPDATE_DIR/eqoa-client.exe $DOWNLOAD_DIR/"
scp "$WORK/legacy-manifest.json" "$REMOTE:$UPDATE_DIR/manifest.json"

echo "texturepacks.art now serves:"
curl -s -u "${EQOA_UPDATE_USER}:${EQOA_UPDATE_PASS}" https://texturepacks.art/eqoa/manifest.json | jq -r '.version + "  " + .linux_sha256'
