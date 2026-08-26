#!/usr/bin/env bash
# Local build for the dev VM.
#
# Stamps the image with the commit it was built from, and marks it
# "-dirty" when the working tree has changes that are not in that commit.
# Without the marker a build made just before committing reports the
# previous commit as though that were what is running, which is
# indistinguishable from the deploy having silently not happened.
set -euo pipefail
cd "$(dirname "$0")/.."

commit=$(git rev-parse --short HEAD)
git diff --quiet && git diff --cached --quiet || commit="${commit}-dirty"

# The version comes from the tags, never from a literal written down
# somewhere. On a tagged commit that is exactly the release number; off
# one it is "v1.0.0-3-gabc1234", which says plainly that this is three
# commits past v1.0.0 rather than claiming to be the release itself.
# An untagged repo falls back to "dev" — deliberately without
# --always, which would hand back a bare commit sha that looks enough
# like a version to be mistaken for one.
version=$(git describe --tags --dirty 2>/dev/null || echo dev)

# The commit's own date, not the wall clock. Rebuilding the same commit
# has to give the same answer, or the field says more about the machine
# that compiled it than about the code — and two identical binaries
# would disagree about their age. It also keeps the build reproducible,
# which -trimpath in the Dockerfile is already reaching for.
COMMIT="$commit" \
VERSION="${VERSION:-$version}" \
BUILD_DATE="$(git log -1 --format=%cI)" \
  docker compose build --memory 1500m "$@"
COMMIT="$commit" docker compose up -d

# Every rebuild leaves the previous image untagged, and they accumulate
# fast on a box that also runs 34 production containers. Dangling only:
# `prune -a` would also take the toolchain images this repo builds and
# tests with (golang, node, puppeteer), costing gigabytes of re-download
# on the next run for no benefit.
reclaimed=$(docker image prune -f 2>/dev/null | tail -1)
echo "built and running: $commit (version ${VERSION:-$version})"
[ -n "$reclaimed" ] && echo "$reclaimed"
