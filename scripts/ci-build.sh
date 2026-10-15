#!/bin/bash
# Build the two release binaries.
# Usage: scripts/ci-build.sh <version> <stable|dev> <outdir>
#
# Run by .github/workflows/release.yml, and runs the same way here given zig
# and x86_64-w64-mingw32-gcc on PATH and the sibling checkouts go.mod points at.
# The version and channel are what the updater compares and follows
# (cmd/client/updater.go); a build without them never updates itself.

set -euo pipefail
VERSION=$1 CHANNEL=$2 OUT=$3
case "$CHANNEL" in stable | dev) ;; *) echo "channel must be stable or dev, not $CHANNEL" >&2; exit 1 ;; esac
cd "$(dirname "$0")/.."
mkdir -p "$OUT"

# -trimpath keeps the build machine's paths out of a public binary.
LDFLAGS="-s -w -X main.BuildVersion=$VERSION -X main.UpdateChannel=$CHANNEL"

# Linux: zig cc targeting glibc 2.31 for broad distro compatibility
# (Debian 11+, Ubuntu 20.04+, RHEL 9+, Fedora 30+, SUSE Leap 15.4+, Arch).
# The host's X11/GL/Wayland headers are what raylib's GLFW compiles against.
ZIG_TARGET="-target x86_64-linux-gnu.2.31 -isystem /usr/include -isystem /usr/include/x86_64-linux-gnu -L/usr/lib/x86_64-linux-gnu"
echo "Building Linux $VERSION ($CHANNEL)..."
CGO_ENABLED=1 CC="zig cc $ZIG_TARGET" CXX="zig c++ $ZIG_TARGET" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/eqoa-client" ./cmd/client/

echo "Building Windows $VERSION ($CHANNEL)..."
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/eqoa-client.exe" ./cmd/client/

ls -l "$OUT"
