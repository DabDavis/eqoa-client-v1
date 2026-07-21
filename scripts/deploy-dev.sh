#!/bin/bash
# Build and deploy dev client (with GM tools, pprof, debug info).
# Usage: ./scripts/deploy-dev.sh
#
# Dev builds include: GM menu (F11), grid runner, pprof (localhost:6060).
# Binaries are NOT stripped — includes debug symbols for profiling.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
DIST_DIR="$PROJECT_DIR/dist"

cd "$PROJECT_DIR"

# Update-server HTTP Basic Auth credentials — injected at build time, never
# hardcoded in source. Export these before running (e.g. from a git-ignored
# secrets file: `source scripts/.deploy-secrets`).
: "${EQOA_UPDATE_USER:?Set EQOA_UPDATE_USER (update-server basic-auth user)}"
: "${EQOA_UPDATE_PASS:?Set EQOA_UPDATE_PASS (update-server basic-auth pass)}"

BUILD_HASH="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
BUILD_VERSION="$(date +%Y%m%d)-${BUILD_HASH}-dev"
CHANGELOG="$(git log -5 --format='- %s' 2>/dev/null | sed 's/"/\\"/g' | awk '{printf "%s\\n", $0}' | sed 's/\\n$//' || echo '')"
LDFLAGS="-X main.BuildVersion=${BUILD_VERSION} -X main.updateUser=${EQOA_UPDATE_USER} -X main.updatePass=${EQOA_UPDATE_PASS}"

echo "=== Deploy DEV v${BUILD_VERSION} ==="

# Build both platforms with dev tag (GM tools, pprof, unstripped)
echo "Building Linux (dev)..."
mkdir -p "$DIST_DIR"
CGO_ENABLED=1 \
    CC="zig cc -target x86_64-linux-gnu.2.31 -isystem /usr/include -isystem /usr/include/x86_64-linux-gnu -L/usr/lib/x86_64-linux-gnu" \
    CXX="zig c++ -target x86_64-linux-gnu.2.31 -isystem /usr/include -isystem /usr/include/x86_64-linux-gnu -L/usr/lib/x86_64-linux-gnu" \
    go build -tags dev -ldflags "$LDFLAGS" -o "$DIST_DIR/eqoa-client" ./cmd/client/

echo "Building Windows (dev)..."
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
    go build -tags dev -ldflags "$LDFLAGS" -o "$DIST_DIR/eqoa-client.exe" ./cmd/client/

# Generate manifest
LINUX_HASH=$(sha256sum "$DIST_DIR/eqoa-client" | cut -d' ' -f1)
WIN_HASH=$(sha256sum "$DIST_DIR/eqoa-client.exe" | cut -d' ' -f1)

# Collect extra files for the manifest "files" array (overlay ESFs only).
ASSETS_DIR="$PROJECT_DIR/../extracted-assets"
OVERLAY_DIR="$ASSETS_DIR/overlays"
FILES_JSON=""
EXTRA_UPLOADS=""

if [ -d "$OVERLAY_DIR" ]; then
    for esf in "$OVERLAY_DIR"/*.esf; do
        [ -f "$esf" ] || continue
        fname=$(basename "$esf")
        fhash=$(sha256sum "$esf" | cut -d' ' -f1)
        entry="{\"path\":\"overlays/$fname\",\"url\":\"https://texturepacks.art/eqoa/overlays/$fname\",\"sha256\":\"$fhash\"}"
        if [ -z "$FILES_JSON" ]; then
            FILES_JSON="$entry"
        else
            FILES_JSON="$FILES_JSON,$entry"
        fi
        EXTRA_UPLOADS="$EXTRA_UPLOADS $esf"
        echo "  overlay: $fname ($fhash)"
    done
fi

if [ -n "$FILES_JSON" ]; then
    FILES_FIELD="\"files\": [$FILES_JSON],"
else
    FILES_FIELD=""
fi

cat > "$DIST_DIR/manifest.json" <<EOF
{
  "version": "${BUILD_VERSION}",
  ${FILES_FIELD}
  "linux_url": "https://texturepacks.art/eqoa/eqoa-client",
  "linux_sha256": "${LINUX_HASH}",
  "windows_url": "https://texturepacks.art/eqoa/eqoa-client.exe",
  "windows_sha256": "${WIN_HASH}",
  "changelog": "${CHANGELOG}"
}
EOF

echo "Manifest:"
cat "$DIST_DIR/manifest.json"
echo ""

# Push to server (auto-update + download links)
echo "Pushing to texturepacks.art..."
REMOTE="10.8.0.1"
UPDATE_DIR="/home/sdg/www/tpacks.art/eqoa/eqoa"
DOWNLOAD_DIR="/home/sdg/www/tpacks.art/eqoa/eqoa-client"
ssh "$REMOTE" "mkdir -p $UPDATE_DIR/overlays $DOWNLOAD_DIR"
scp "$DIST_DIR/manifest.json" "$DIST_DIR/eqoa-client" "$DIST_DIR/eqoa-client.exe" \
    "$REMOTE:$UPDATE_DIR/"
ssh "$REMOTE" "cp $UPDATE_DIR/eqoa-client $UPDATE_DIR/eqoa-client.exe $DOWNLOAD_DIR/"
if [ -n "$EXTRA_UPLOADS" ]; then
    for f in $EXTRA_UPLOADS; do
        fname=$(basename "$f")
        if [[ "$fname" == zone_* ]]; then
            scp "$f" "$REMOTE:$UPDATE_DIR/overlays/"
        else
            scp "$f" "$REMOTE:$UPDATE_DIR/"
        fi
    done
fi

# Verify
echo ""
echo "=== Deployed DEV v${BUILD_VERSION} ==="
curl -s -u "${EQOA_UPDATE_USER}:${EQOA_UPDATE_PASS}" https://texturepacks.art/eqoa/manifest.json | head -1
