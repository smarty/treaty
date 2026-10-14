#!/bin/sh
# Compares two builds of treaty across repositories, so a scanner change
# can't quietly shift what Treaty reads. For each repository it runs
# overview and dump with both builds and diffs them, then lists which
# repositories differ and exits 1 when any does. A difference is not wrong
# in itself: read it, and check it is the change you meant.
#
# Usage: scripts/compare.sh <old treaty> <new treaty> <repository>...
#
# Example, against the installed build:
#   go build -o /tmp/treaty-new ./cmd/treaty
#   scripts/compare.sh "$(command -v treaty)" /tmp/treaty-new ~/src/github.com/smarty/*/
set -eu

if [ $# -lt 3 ]; then
	echo "usage: scripts/compare.sh <old treaty> <new treaty> <repository>..." >&2
	exit 2
fi

old="$1"
new="$2"
shift 2
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT

differ=0
same=0
for repo in "$@"; do
	[ -d "$repo" ] || continue
	name="$(basename "$repo")"
	changed=""
	for command in overview dump; do
		"$old" -C "$repo" "$command" > "$out/old" 2>&1 || true
		"$new" -C "$repo" "$command" > "$out/new" 2>&1 || true
		if ! cmp -s "$out/old" "$out/new"; then
			changed="$changed $command"
			echo "== $name: $command differs"
			diff -u "$out/old" "$out/new" | head -40 || true
		fi
	done

	if [ -n "$changed" ]; then
		differ=$((differ + 1))
	else
		same=$((same + 1))
	fi
done

echo "$same repositories the same, $differ different"
[ "$differ" -eq 0 ]
