#!/usr/bin/env bash
set -euo pipefail

version="${1:?version is required}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="$root/dist"

rm -rf "$dist"
mkdir -p "$dist"

platforms=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
)

for platform in "${platforms[@]}"; do
  os="${platform%/*}"
  arch="${platform#*/}"
  name="tabucom_${version}_${os}_${arch}"
  bin="tabucom"
  archive="$name.tar.gz"

  if [[ "$os" == "windows" ]]; then
    bin="tabucom.exe"
    archive="$name.zip"
  fi

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  echo "Building $platform"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$tmp/$bin" ./cmd/tabucom

  cp "$root/LICENSE" "$tmp/LICENSE"
  cp "$root/README.md" "$tmp/README.md"

  if [[ "$os" == "windows" ]]; then
    (cd "$tmp" && zip -q "$dist/$archive" "$bin" LICENSE README.md)
  else
    (cd "$tmp" && tar -czf "$dist/$archive" "$bin" LICENSE README.md)
  fi

  rm -rf "$tmp"
  trap - EXIT
done

(
  cd "$dist"
  shasum -a 256 * > checksums.txt
)
