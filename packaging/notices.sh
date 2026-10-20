#!/usr/bin/env bash
# Prints THIRD-PARTY-NOTICES.txt: the licence of every Go module linked into
# orosdrive on the platforms we ship, and of the Go standard library. The
# release files carry it because the MIT/BSD licences ask for their notice
# and Apache-2.0 for its text in binary copies.
# Usage: packaging/notices.sh > THIRD-PARTY-NOTICES.txt
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
main="$(go list -m)"
mods="$(for os in windows linux; do for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go list -deps \
    -f '{{with .Module}}{{.Path}} {{.Version}} {{.Dir}}{{end}}' ./cmd/orosdrive
done; done | grep -v "^$main " | sort -u)"

cat <<TXT
orOS Drive: third-party notices
===============================

orOS Drive (MIT) is built with Go and the Go modules below. Each keeps
its own licence, reproduced here in full.

TXT
print() { # name version dir-or-file...
  local name="$1" ver="$2"; shift 2
  printf '%s\n' "------------------------------------------------------------------------" "$name $ver" ""
  for f in "$@"; do cat "$f"; echo; done
}
print "Go standard library and runtime" "$(go env GOVERSION)" "$(go env GOROOT)/LICENSE"
while read -r path ver dir; do
  [ -n "$path" ] || continue
  files=()
  for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE*; do
    [ -f "$f" ] && files+=("$f")
  done
  if [ "${#files[@]}" -eq 0 ]; then echo "notices.sh: no licence file in $path $ver ($dir)" >&2; exit 1; fi
  print "$path" "$ver" "${files[@]}"
done <<< "$mods"
