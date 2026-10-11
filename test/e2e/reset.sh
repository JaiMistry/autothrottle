#!/usr/bin/env bash
# Tears the e2e stack down and provisions it again from nothing.
#
# MEDIA=jellyfin|emby|plex picks the media server (default jellyfin),
# AT_IMAGE=<image> uses an image you already built instead of building
# AutoThrottle from this checkout, and E2E_NEWEST=1 uses each server's
# newest release instead of the pinned one. Plex needs PLEX_E2E_TOKEN and
# PLEX_E2E_CLIENT_ID set (run.sh loads them).
set -euo pipefail
cd "$(dirname "$0")"

# E2E_NEWEST=1 runs every server at its newest release, pulled fresh.
compose=(docker compose -f compose.yml)
up=(up -d)
if [ "${E2E_NEWEST:-}" = 1 ]; then
  compose+=(-f compose.newest.yml)
  up+=(--pull always)
fi

export COMPOSE_PROFILES=jellyfin,emby,plex
"${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
# Jellyfin and Plex write their files as root, which a runner's own user
# can't remove — so the state is cleared from inside a container.
if [ -d .state ]; then
  docker run --rm --memory=64m -v "$PWD/.state:/state" alpine:3 \
    sh -c 'rm -rf /state/autothrottle /state/jellyfin /state/emby /state/plex /state/qbittorrent /state/transmission /state/env.json /state/plex-machine-id'
fi

export COMPOSE_PROFILES="${MEDIA:-jellyfin}"
python3 provision.py seed
if [ "${MEDIA:-}" = plex ]; then
  # Never echoed: it claims a server for the test account.
  PLEX_CLAIM="$(python3 provision.py claim)"
  export PLEX_CLAIM
fi
if [ -n "${AT_IMAGE:-}" ]; then
  "${compose[@]}" "${up[@]}" --no-build
else
  "${compose[@]}" "${up[@]}" --build
fi
python3 provision.py configure || {
  echo; echo "--- ${MEDIA:-jellyfin} log ---"
  docker logs "e2e-${MEDIA:-jellyfin}" 2>&1 | tail -80
  exit 1
}
