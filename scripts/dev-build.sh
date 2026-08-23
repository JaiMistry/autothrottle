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

COMMIT="$commit" \
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  docker compose build --memory 1500m "$@"
COMMIT="$commit" docker compose up -d
echo "built and running: $commit"
