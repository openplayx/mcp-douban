#!/usr/bin/env bash
# Build static release binaries (no cgo). Usage: scripts/build.sh [os/arch ...]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
targets=("$@")
[ ${#targets[@]} -gt 0 ] || targets=(linux/amd64 linux/arm64 darwin/arm64)
mkdir -p dist
for target in "${targets[@]}"; do
  os="${target%/*}" arch="${target#*/}"
  out="dist/mcp-douban-${os}-${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=${version}" -o "$out" ./src/cmd/mcp-douban
  echo "$out"
done
(cd dist && shasum -a 256 mcp-douban-* > SHA256SUMS)
echo "$version" > dist/VERSION
