#!/bin/sh
# Builds treaty for macOS and Linux, on Intel and ARM, into dist/: one
# archive per platform, holding the binary stamped with the version, the
# license and the README, and checksums.txt with each archive's SHA-256.
#
# Usage: scripts/release.sh v0.2.0
set -eu

version="${1:-}"
if ! printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	echo "usage: scripts/release.sh <version, such as v0.2.0>" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
rm -rf dist
mkdir dist

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
	os="${target%/*}"
	arch="${target#*/}"
	name="treaty_${version}_${os}_${arch}"
	mkdir "dist/$name"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.stamp=$version" -o "dist/$name/treaty" ./cmd/treaty
	cp LICENSE README.md "dist/$name/"
	tar -C dist -czf "dist/$name.tar.gz" "$name"
	rm -r "dist/${name:?}"
done

cd dist
if command -v sha256sum >/dev/null; then
	sha256sum ./*.tar.gz | sed 's| \./| |' > checksums.txt
else
	shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt
fi

cat checksums.txt
