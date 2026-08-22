#!/usr/bin/env sh
# Run the Go toolchain in a memory-capped container.
#
#   scripts/go.sh build ./...
#   scripts/go.sh test ./...
#   scripts/go.sh vet ./...
#
# There is no Go toolchain on the development host, so everything runs
# in an ephemeral container. Two things make that safe rather than
# merely convenient:
#
#   * A hard --memory cap. Docker advertises the host's entire RAM to
#     every container, so an uncapped toolchain will happily grow until
#     the host's OOM killer picks a victim — and the victim is chosen
#     by score, not by blame, so it is usually something else.
#   * Bounded compile parallelism. The toolchain defaults to one
#     compile process per core, so peak memory tracks the machine's
#     core count rather than its free RAM.
#
# It also refuses to start when the host is already short, because a
# build that fails is cheaper than a host that falls over.
set -eu

IMAGE="${GO_IMAGE:-golang:1.26.6-alpine}"
LIMIT="${GO_MEMORY_LIMIT:-1500m}"
JOBS="${GO_JOBS:-2}"
NEED_MB="${GO_MIN_AVAILABLE_MB:-1800}"

available_mb() {
  awk '/MemAvailable/ {printf "%d", $2/1024}' /proc/meminfo 2>/dev/null || echo 99999
}

AVAILABLE="$(available_mb)"
if [ "$AVAILABLE" -lt "$NEED_MB" ]; then
  echo "refusing to start: ${AVAILABLE} MB available, need ${NEED_MB} MB." >&2
  echo "Something else on this host is using the memory. Wait, or lower" >&2
  echo "GO_MIN_AVAILABLE_MB if you know the run is small." >&2
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
echo "go $* — ${LIMIT} cap, -p=${JOBS}, ${AVAILABLE} MB available"

exec docker run --rm \
  --memory="$LIMIT" --memory-swap="$LIMIT" \
  --cpus="${GO_CPUS:-2}" \
  -e "GOFLAGS=-p=${JOBS}" \
  -e "GOMAXPROCS=${JOBS}" \
  -e "GOMEMLIMIT=${GO_SOFT_LIMIT:-1200MiB}" \
  -v "$ROOT":/src -w /src \
  "$IMAGE" go "$@"
