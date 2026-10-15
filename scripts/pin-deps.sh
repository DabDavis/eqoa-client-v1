#!/bin/bash
# Pin the release workflow to the sibling checkouts as they are now.
# Usage: scripts/pin-deps.sh
#
# Writes each sibling's HEAD into scripts/deps.env. Refuses a sibling with
# uncommitted changes to tracked files, or whose HEAD is not on GitHub yet:
# CI can only build what it can fetch, and a pin to a commit that exists only
# on this machine fails the next release instead of this command.

set -euo pipefail
cd "$(dirname "$0")/.."

pin() { # <dir> <github owner/repo>
    local dir=$1 repo=$2 head
    git -C "$dir" diff --quiet HEAD -- || { echo "$dir has uncommitted changes; commit them first" >&2; exit 1; }
    head=$(git -C "$dir" rev-parse HEAD)
    if ! git -C "$dir" ls-remote "git@github.com:$repo.git" | grep -q "^$head"; then
        # ls-remote lists branch tips only; an ancestor of one is fetchable too.
        git -C "$dir" fetch -q "git@github.com:$repo.git"
        git -C "$dir" branch -r --contains "$head" | grep -q . \
            || git -C "$dir" merge-base --is-ancestor "$head" FETCH_HEAD \
            || { echo "$dir HEAD $head is not on github.com/$repo; push it first" >&2; exit 1; }
    fi
    echo "$head"
}

ESF=$(pin ../go-eqoa-esf-tools-v1 DabDavis/eqoa-esf-tools)
RAY=$(pin ../raylib-go DabDavis/raylib-go)

sed -i -E "s/^ESF_TOOLS_REF=.*/ESF_TOOLS_REF=$ESF/; s/^RAYLIB_GO_REF=.*/RAYLIB_GO_REF=$RAY/" scripts/deps.env
git diff --stat -- scripts/deps.env
echo "esf-tools $ESF"
echo "raylib-go $RAY"
