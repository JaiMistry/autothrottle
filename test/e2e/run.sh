#!/usr/bin/env bash
# Runs the end-to-end suite: for each media server, provisions the stack
# from nothing, drives AutoThrottle through a browser, and tears it down.
#
#   test/e2e/run.sh                     Jellyfin, Emby, and Plex if a token is available
#   MEDIA=emby test/e2e/run.sh          one media server only
#   AT_IMAGE=my-image test/e2e/run.sh   test an image you already built
#   KEEP=1 test/e2e/run.sh              leave the last stack running afterwards
#   E2E_NEWEST=1 test/e2e/run.sh        every server at its newest release, not the pins
#   E2E_BROWSER=host test/e2e/run.sh    use this machine's Node, puppeteer-core and
#                                       Chrome (PUPPETEER_EXECUTABLE_PATH), not the
#                                       Puppeteer image
#
# Plex runs against a real plex.tv test account. Its token and client
# identifier come from PLEX_E2E_TOKEN and PLEX_E2E_CLIENT_ID, or from a
# file of KEY=value lines named by PLEX_E2E_ENV. Keep that file outside
# the repository; nothing here ever writes the token to disk.
set -euo pipefail
cd "$(dirname "$0")"

if [ -n "${PLEX_E2E_ENV:-}" ] && [ -z "${PLEX_E2E_TOKEN:-}" ]; then
  set -a; . "$PLEX_E2E_ENV"; set +a
fi
default="jellyfin emby"
[ -n "${PLEX_E2E_TOKEN:-}" ] && default="$default plex"

# The biggest pull, not needed until the stack is up: fetch it meanwhile.
PUPPETEER=ghcr.io/puppeteer/puppeteer:25.12.0
pull=""
if [ "${E2E_BROWSER:-}" != host ] && ! docker image inspect "$PUPPETEER" >/dev/null 2>&1; then
  docker pull -q "$PUPPETEER" >/dev/null &
  pull=$!
fi

# Plex's name on the shared test account, unique to this run: cleanup
# removes only its own server while another run may be using the account.
PLEX_SERVER_NAME="e2e-plex-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
export PLEX_SERVER_NAME

status=0
for media in ${MEDIA:-$default}; do
  echo "=== $media ==="
  if [ "$media" = plex ] && [ -z "${PLEX_E2E_TOKEN:-}" ]; then
    echo "  skipped: no PLEX_E2E_TOKEN"; continue
  fi
  MEDIA=$media ./reset.sh
  [ -n "$pull" ] && { wait "$pull" || true; pull=""; }
  # The Docker socket lets the scenario restart containers mid-test.
  if [ "${E2E_BROWSER:-}" = host ]; then
    scenario=(node scenario.js)
  else
    scenario=(docker run --rm --memory=1500m --network host
      -v /var/run/docker.sock:/var/run/docker.sock --group-add "$(stat -c %g /var/run/docker.sock)"
      -e NODE_PATH=/home/pptruser/node_modules
      -e PLEX_E2E_TOKEN -e PLEX_E2E_CLIENT_ID
      -v "$PWD:/work" -w /work
      "$PUPPETEER" node scenario.js)
  fi
  "${scenario[@]}" || {
      status=1
      echo; echo "--- AutoThrottle log ($media) ---"
      docker logs e2e-autothrottle 2>&1 | tail -40
    }
  # Plex comes off the account even on failure; a kept stack's server is
  # removed by the first cleanup after it is an hour old.
  if [ "$media" = plex ] && [ "${KEEP:-}" != 1 ]; then
    MEDIA=plex python3 provision.py cleanup || true
  fi
done

[ "${KEEP:-}" = 1 ] || COMPOSE_PROFILES=jellyfin,emby,plex docker compose -f compose.yml -f compose.newest.yml down --remove-orphans >/dev/null 2>&1
exit "$status"
