#!/usr/bin/env bash
# Cross-compile static, stripped binaries into dist/.
set -euo pipefail
VERSION="${1:-dev}"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  out="dist/aatmt-${VERSION}-${os}-${arch}"
  [ "$os" = windows ] && out="$out.exe"
  echo "building $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$out" ./cmd/aatmt
done
ls -lh dist
