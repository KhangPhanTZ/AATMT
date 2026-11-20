#!/usr/bin/env bash
# Build release artifacts into dist/:
#   aatmt-<ver>-<os>-<arch>.tar.gz   Linux / macOS (binary + README)
#   aatmt-<ver>-windows-<arch>.zip   Windows portable (exe + icon + README)
#   AATMT-Setup-<ver>-<arch>.exe     Windows installer (needs makensis / NSIS 3)
#   SHA256SUMS
# Usage: ./build.sh v1.0.0        (TARGETS="linux/amd64 windows/amd64" ./build.sh v1.0.0 to limit)
set -euo pipefail

VERSION="${1:-dev}"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
ROOT="$(cd "$(dirname "$0")" && pwd)"
DIST="$ROOT/dist"
STAGE="$(mktemp -d)"
trap 'rm -rf -- "$STAGE"' EXIT
mkdir -p "$DIST"

# numeric x.y.z.w for Windows version resources (v1.0.0 -> 1.0.0.0)
NUM="${VERSION#v}"; NUM="${NUM%%-*}"
IFS=. read -r a b c d <<<"$NUM.0.0.0.0"
VERSION4="${a:-0}.${b:-0}.${c:-0}.${d:-0}"
case "$VERSION4" in *[!0-9.]*) VERSION4="0.0.0.0" ;; esac

LDFLAGS="-s -w -X github.com/khangphantz/aatmt/internal/version.Version=${VERSION}"
ICON="$STAGE/aatmt.ico"
(cd "$ROOT" && go run ./tools/mkicon -ico "$ICON")

for target in $TARGETS; do
  os="${target%/*}"; arch="${target#*/}"
  name="aatmt-${VERSION}-${os}-${arch}"
  dir="$STAGE/$name"; mkdir -p "$dir"
  bin="aatmt"; [ "$os" = windows ] && bin="aatmt.exe"
  echo "building $name"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$dir/$bin" ./cmd/aatmt)
  cp "$ROOT/README.md" "$dir/"
  if [ "$os" = windows ]; then
    cp "$ICON" "$dir/"
    (cd "$STAGE" && zip -qr "$DIST/$name.zip" "$name")
    if command -v makensis >/dev/null; then
      label="$arch"; [ "$arch" = amd64 ] && label="x64"
      setup="$DIST/AATMT-Setup-${VERSION}-${label}.exe"
      echo "packaging $(basename "$setup")"
      makensis -V2 -DVERSION="${VERSION#v}" -DVERSION4="$VERSION4" -DARCH="$arch" -DSRC="$dir" -DOUT="$setup" "$ROOT/packaging/windows/aatmt.nsi"
    else
      echo "makensis not found: skipping the Windows installer (install NSIS 3 to build it)"
    fi
  else
    tar -C "$STAGE" -czf "$DIST/$name.tar.gz" "$name"
  fi
done

(cd "$DIST" && sha256sum -- *"${VERSION}"* > SHA256SUMS)
ls -lh "$DIST"
