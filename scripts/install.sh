#!/bin/sh
# Installs a released treaty for this machine, macOS or Linux on Intel or
# ARM, after checking its archive against the release's checksums.txt.
#
# Usage: scripts/install.sh [version] [directory]
#
# The version defaults to the latest release, and the directory to
# ~/.local/bin. In CI, without a checkout of treaty:
#   curl -fsSL https://raw.githubusercontent.com/smarty/treaty/master/scripts/install.sh | sh -s v0.3.0
set -eu

version="${1:-latest}"
directory="${2:-$HOME/.local/bin}"
releases="https://github.com/smarty/treaty/releases"

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) echo "treaty releases are for macOS and Linux, not $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "treaty releases are for Intel and ARM, not $(uname -m)" >&2; exit 1 ;;
esac

if [ "$version" = latest ]; then
	# The latest release's page redirects to its tag.
	version="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest")"
	version="${version##*/}"
fi

if ! printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	echo "not a treaty version: $version" >&2
	exit 2
fi

name="treaty_${version}_${os}_${arch}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/$name.tar.gz" "$releases/download/$version/$name.tar.gz"
curl -fsSL -o "$work/checksums.txt" "$releases/download/$version/checksums.txt"

expected="$(grep " $name.tar.gz\$" "$work/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null; then
	actual="$(sha256sum "$work/$name.tar.gz" | cut -d' ' -f1)"
else
	actual="$(shasum -a 256 "$work/$name.tar.gz" | cut -d' ' -f1)"
fi

if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
	echo "$name.tar.gz does not match the release's checksums.txt" >&2
	exit 1
fi

tar -C "$work" -xzf "$work/$name.tar.gz"
mkdir -p "$directory"
mv "$work/$name/treaty" "$directory/treaty"
echo "installed treaty $version in $directory"
