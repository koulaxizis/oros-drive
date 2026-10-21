#!/usr/bin/env bash
# Writes the Scoop and winget manifests for a release from the templates in
# packaging/scoop and packaging/winget, with the hashes from the release's
# SHA256SUMS.txt: packaging/channel-manifests.sh <version> <SHA256SUMS.txt> <out dir>
set -euo pipefail
VERSION="$1" SUMS="$2" OUT="$3"
ROOT="$(cd "$(dirname "$0")" && pwd)"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "bad version: $VERSION"; exit 1; }
sum() {
  local s
  s="$(awk -v f="$1" '{ n = $2; sub(/^\*?(\.\/)?/, "", n); if (n == f) print $1 }' "$SUMS")"
  [[ "$s" =~ ^[0-9a-f]{64}$ ]] || { echo "no sha256 for $1" >&2; exit 1; }
  echo "$s"
}
SETUP="$(sum "orOS-Drive-Setup-$VERSION.exe")"
AMD64="$(sum "orosdrive-$VERSION-windows-amd64.zip")"
ARM64="$(sum "orosdrive-$VERSION-windows-arm64.zip")"
fill() {
  sed -e "s/@VERSION@/$VERSION/g" -e "s/@SHA_SETUP_UPPER@/${SETUP^^}/g" \
    -e "s/@SHA_AMD64@/$AMD64/g" -e "s/@SHA_ARM64@/$ARM64/g" -e "s/@DATE@/$(date -u +%F)/g" "$1"
}
mkdir -p "$OUT/bucket" "$OUT/winget"
fill "$ROOT/scoop/oros-drive.json.in" > "$OUT/bucket/oros-drive.json"
for f in "$ROOT"/winget/*.yaml.in; do
  fill "$f" > "$OUT/winget/$(basename "${f%.in}")"
done
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OUT/bucket/oros-drive.json"
if grep -l '@[A-Z0-9_]*@' "$OUT"/winget/*.yaml "$OUT/bucket/oros-drive.json"; then echo "unfilled placeholder"; exit 1; fi
ls -R "$OUT"
