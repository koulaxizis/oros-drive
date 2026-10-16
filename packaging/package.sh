#!/usr/bin/env bash
# Builds the release files into out/:
#   orosdrive-<v>-windows-<arch>.zip  orosdrive.exe (terminal) + "orOS Drive.exe" (no console window)
#   orosdrive-<v>-linux-<arch>.tar.gz orosdrive + .desktop entry + icon
#   oros-drive_<v>_<arch>.deb         Debian / Ubuntu / Mint package
# Usage: packaging/package.sh <version>
set -euo pipefail
v="${1:?version}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/out"
rm -rf "$out" && mkdir -p "$out"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
ld="-s -w -X main.version=$v"

for arch in amd64 arm64; do
  d="$work/win-$arch"; mkdir -p "$d"
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "$ld" -o "$d/orosdrive.exe" "$root/cmd/orosdrive"
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "$ld -H=windowsgui" -o "$d/orOS Drive.exe" "$root/cmd/orosdrive"
  cp "$root/LICENSE" "$d/LICENSE.txt"
  cat > "$d/README.txt" <<TXT
orOS Drive $v

Double-click "orOS Drive.exe" to start. The settings open in your browser:
sign in to the Dropbox orOS syncs with, type your orOS sync passphrase,
pick the folders to mirror. The app then lives in the tray (by the clock).

orosdrive.exe is the same program for the terminal (orosdrive help).
Free and open source (MIT): https://github.com/koulaxizis/oros-drive
TXT
  (cd "$d" && zip -q -9 "$out/orosdrive-$v-windows-$arch.zip" ./*)

  d="$work/linux-$arch/orosdrive-$v"; mkdir -p "$d"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$ld" -o "$d/orosdrive" "$root/cmd/orosdrive"
  cp "$root/LICENSE" "$root/packaging/oros-drive.desktop" "$root/packaging/oros-drive.png" "$d/"
  tar -C "$work/linux-$arch" -czf "$out/orosdrive-$v-linux-$arch.tar.gz" "orosdrive-$v"

  p="$work/deb-$arch"
  install -Dm755 "$d/orosdrive" "$p/usr/bin/orosdrive"
  install -Dm644 "$root/packaging/oros-drive.desktop" "$p/usr/share/applications/oros-drive.desktop"
  install -Dm644 "$root/packaging/oros-drive.png" "$p/usr/share/icons/hicolor/192x192/apps/oros-drive.png"
  install -Dm644 "$root/LICENSE" "$p/usr/share/doc/oros-drive/copyright"
  debv="${v#v}"; debv="${debv//-/'~'}"
  mkdir -p "$p/DEBIAN"
  cat > "$p/DEBIAN/control" <<CTL
Package: oros-drive
Version: $debv
Architecture: $arch
Maintainer: Christos Koulaxizis <noreply@useoros.online>
Homepage: https://github.com/koulaxizis/oros-drive
Section: net
Priority: optional
Recommends: gnome-keyring | kwalletmanager, xdg-utils
Description: Mirror folders into your encrypted orOS Vault
 orOS Drive keeps chosen folders of this computer and the encrypted
 orOS Vault in your own Dropbox the same, both ways. Dropbox only
 ever sees encrypted data. Free and open source (MIT).
CTL
  dpkg-deb --build --root-owner-group "$p" "$out/oros-drive_${debv}_$arch.deb" >/dev/null
done
(cd "$out" && sha256sum ./* > SHA256SUMS.txt)
ls -l "$out"
