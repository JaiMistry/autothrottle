#!/usr/bin/env bash
# Runs the end-to-end suite: for each media server, provisions the stack
# from nothing, drives AutoThrottle through a browser, and tears it down.
#
#   test/e2e/run.sh                     Jellyfin, then Emby, building from this checkout
#   MEDIA=emby test/e2e/run.sh          one media server only
#   AT_IMAGE=my-image test/e2e/run.sh   test an image you already built
#   KEEP=1 test/e2e/run.sh              leave the last stack running afterwards
set -euo pipefail
cd "$(dirname "$0")"

status=0
for media in ${MEDIA:-jellyfin emby}; do
  echo "=== $media ==="
  MEDIA=$media ./reset.sh
  docker run --rm --memory=1500m --network host \
    -e NODE_PATH=/home/pptruser/node_modules \
    -v "$PWD:/work" -w /work \
    ghcr.io/puppeteer/puppeteer:25.12.0 node scenario.js || {
      status=1
      echo; echo "--- AutoThrottle log ($media) ---"
      docker logs e2e-autothrottle 2>&1 | tail -40
    }
done

[ "${KEEP:-}" = 1 ] || COMPOSE_PROFILES=jellyfin,emby docker compose -f compose.yml down --remove-orphans >/dev/null 2>&1
exit "$status"
