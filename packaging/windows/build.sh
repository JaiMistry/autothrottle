#!/usr/bin/env sh
# Builds the Windows binaries the installer packs, for x64 and Arm, with
# the icon and version stamped in:
#
#   VERSION=v1.4.2 COMMIT=<sha> BUILD_DATE=<date> packaging/windows/build.sh <out dir>
#
# leaves <out dir>/amd64 and <out dir>/arm64, each with autothrottle.exe
# and autothrottle-tray.exe. The binary embeds the CSS, so build that
# first, the way the image's build does (the Dockerfile's tailwind stage).
set -eu

[ $# -eq 1 ] || { echo "usage: $0 <out dir>" >&2; exit 2; }
mkdir -p "$1"
out=$(cd "$1" && pwd)
cd "$(dirname "$0")/../.."

# Without it the build still succeeds, and the app has no styles.
css=internal/web/static/css/tailwind.css
[ -s "$css" ] || { echo "$0: $css is missing: build the CSS first" >&2; exit 1; }

VERSION=${VERSION:-dev}
COMMIT=${COMMIT:-}
BUILD_DATE=${BUILD_DATE:-}

# Windows wants four numbers: v1.4.2 is 1.4.2.0, anything else 0.0.0.0.
v=${VERSION#v}
v=${v%%-*}
case "$v" in
  [0-9]*.[0-9]*.[0-9]*) number="$v.0" ;;
  *) number=0.0.0.0 ;;
esac

for cmd in autothrottle autothrottle-tray; do
  manifest=cli
  description=AutoThrottle
  if [ "$cmd" = autothrottle-tray ]; then
    manifest=gui
    description="AutoThrottle tray icon"
  fi
  go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64,arm64 --out "cmd/$cmd/rsrc" --manifest "$manifest" \
    --icon cmd/autothrottle-tray/autothrottle.ico \
    --product-name AutoThrottle --product-version "$number" --file-version "$number" \
    --file-description "$description" --original-filename "$cmd.exe" \
    --copyright "JaiMistry, GPL-3.0"
done

stamp="-s -w \
  -X github.com/JaiMistry/autothrottle/internal/version.Version=$VERSION \
  -X github.com/JaiMistry/autothrottle/internal/version.Commit=$COMMIT \
  -X github.com/JaiMistry/autothrottle/internal/version.BuildDate=$BUILD_DATE"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags="$stamp" \
    -o "$out/$arch/autothrottle.exe" ./cmd/autothrottle
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags="$stamp -H=windowsgui" \
    -o "$out/$arch/autothrottle-tray.exe" ./cmd/autothrottle-tray
done
