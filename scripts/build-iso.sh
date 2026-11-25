#!/bin/sh
# Build the bootable IMAGen ISO. Run as root on Debian/Ubuntu, or let
# scripts/build-iso-docker.sh run it inside a Debian container.
#
# Output: dist/imagen-<version>-amd64.iso (write it to a USB stick with
# Rufus in "DD image" mode, balenaEtcher, or `dd`).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

if ! command -v lb >/dev/null; then
  apt-get update
  apt-get install -y --no-install-recommends live-build debootstrap squashfs-tools xorriso \
    grub-efi-amd64-bin grub-pc-bin mtools dosfstools syslinux syslinux-common isolinux ca-certificates golang-go
fi

VERSION=$(sed -n 's/^var ToolVersion = "\(.*\)"/\1/p' internal/engine/engine.go)
echo "Building IMAGen $VERSION"

# Static Linux binary into the live filesystem.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
  -o live/config/includes.chroot/usr/local/bin/imagen ./cmd/imagen

cd live
chmod +x auto/* config/hooks/normal/*.hook.chroot
lb clean --purge || true
lb config
lb build
mkdir -p "$ROOT/dist"
mv -f live-image-amd64.hybrid.iso "$ROOT/dist/imagen-$VERSION-amd64.iso"
echo "ISO ready: dist/imagen-$VERSION-amd64.iso"
