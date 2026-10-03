#!/usr/bin/env bash
# Runs the end-to-end suite: provisions the stack from nothing, drives
# AutoThrottle through a browser, and tears it down again.
#
#   test/e2e/run.sh                 build AutoThrottle from this checkout
#   AT_IMAGE=my-image test/e2e/run.sh   test an image you already built
#   KEEP=1 test/e2e/run.sh          leave the stack running afterwards
set -euo pipefail
cd "$(dirname "$0")"

./reset.sh

# The setup code is only ever in the container log — exactly where a
# person would have to go and get it.
code=""
for _ in $(seq 1 30); do
  code=$(docker logs e2e-autothrottle 2>&1 | grep -oE '[A-Z0-9]{4}-[A-Z0-9]{4}' | head -1 || true)
  [ -n "$code" ] && break
  sleep 1
done
[ -n "$code" ] || { echo "no setup code in the AutoThrottle log"; docker logs e2e-autothrottle; exit 1; }

status=0
docker run --rm --memory=1500m --network host \
  -e SETUP_CODE="$code" -e NODE_PATH=/home/pptruser/node_modules \
  -v "$PWD:/work" -w /work \
  ghcr.io/puppeteer/puppeteer:25.12.0 node scenario.js || status=$?

if [ "$status" -ne 0 ]; then
  echo; echo "--- AutoThrottle log ---"
  docker logs e2e-autothrottle 2>&1 | tail -40
fi
[ "${KEEP:-}" = 1 ] || docker compose -f compose.yml down --remove-orphans >/dev/null 2>&1
exit "$status"
