#!/usr/bin/env bash
# Tears the e2e stack down and provisions it again from nothing.
#
# AT_IMAGE=<image> uses an image you already built instead of building
# AutoThrottle from this checkout.
set -euo pipefail
cd "$(dirname "$0")"

docker compose -f compose.yml down --remove-orphans >/dev/null 2>&1 || true
# Jellyfin writes its files as root, which a runner's own user can't
# remove — so the state is cleared from inside a container. The test
# video is kept: it takes the longest to make and never changes.
if [ -d .state ]; then
  docker run --rm -v "$PWD/.state:/state" alpine:3 \
    sh -c 'rm -rf /state/autothrottle /state/jellyfin /state/qbittorrent /state/transmission /state/env.json'
fi

python3 provision.py seed
if [ -n "${AT_IMAGE:-}" ]; then
  docker compose -f compose.yml up -d --no-build
else
  docker compose -f compose.yml up -d --build
fi
python3 provision.py configure
