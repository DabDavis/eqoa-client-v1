#!/bin/bash
# Build dev client locally (GM tools, pprof, debug symbols).
# Usage: ./scripts/build-dev.sh

set -e
cd "$(dirname "$0")/.."
go build -tags dev -o client-dev ./cmd/client/
echo "Built client-dev ($(du -h client-dev | cut -f1))"
