#!/bin/sh
# Real-filesystem round trip, run as root on Linux:
#   sudo scripts/fs-integration-test.sh ./imagen
# For each of ext4, NTFS and FAT32 it builds a GPT disk image with real mkfs,
# fills it with files (and deleted garbage), takes a used-blocks-only image,
# restores it to a LARGER disk, then checks: fsck is clean, every file's
# checksum matches, and the disk GUID, partition GUID and filesystem UUID are
# identical to the source.
set -eu
BC=$(realpath "${1:-./imagen}")
W=$(mktemp -d)
trap 'umount "$W/mnt" 2>/dev/null || true; losetup -D 2>/dev/null || true; rm -rf "$W"' EXIT
mkdir -p "$W/mnt"
fail() { echo "FAIL: $*"; exit 1; }

for FS in ext4 ntfs vfat; do
  echo "=== $FS ==="
  SRC=$W/src-$FS.img; DST=$W/dst-$FS.img; IMG=$W/$FS.bci
  truncate -s 512M "$SRC"
  printf 'label: gpt\nstart=2048, size=900000, type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7\n' | sfdisk -q "$SRC"
  LOOP=$(losetup -fP --show "$SRC"); P=${LOOP}p1
  case $FS in
    ext4) mkfs.ext4 -q -L SRCVOL "$P" ;;
    ntfs) mkfs.ntfs -Q -q -L SRCVOL "$P" ;;
    vfat) mkfs.vfat -F 32 -n SRCVOL "$P" >/dev/null ;;
  esac
  mount "$P" "$W/mnt"
  head -c 150M /dev/urandom > "$W/mnt/garbage.bin"; sync; rm "$W/mnt/garbage.bin"   # deleted data = free space with junk
  mkdir -p "$W/mnt/dir/sub"
  for i in 1 2 3 4 5 6 7 8; do head -c $((i * 3))M /dev/urandom > "$W/mnt/dir/file$i.bin"; done
  echo hello > "$W/mnt/dir/sub/hello.txt"
  (cd "$W/mnt" && find . -type f -exec md5sum {} + | sort -k2) > "$W/sums"
  umount "$W/mnt"
  UUID_SRC=$(blkid -s UUID -o value "$P"); PART_SRC=$(blkid -s PARTUUID -o value "$P")
  losetup -d "$LOOP"
  DISK_SRC=$(sfdisk --disk-id "$SRC")

  "$BC" backup -source "$SRC" -out "$IMG" -mode used -verify 2>&1 | grep -E "partition|image" || true
  truncate -s 768M "$DST"
  "$BC" restore -image "$IMG" -target "$DST" -yes 2>&1 | grep -E "GPT|complete" || true

  [ "$(sfdisk --disk-id "$DST")" = "$DISK_SRC" ] || fail "$FS disk GUID changed"
  sfdisk --verify "$DST" >/dev/null 2>&1 || fail "$FS partition table does not verify"
  LOOP=$(losetup -fP --show "$DST"); P=${LOOP}p1
  [ "$(blkid -s UUID -o value "$P")" = "$UUID_SRC" ] || fail "$FS filesystem UUID changed"
  [ "$(blkid -s PARTUUID -o value "$P")" = "$PART_SRC" ] || fail "$FS partition GUID changed"
  case $FS in
    ext4) e2fsck -fn "$P" >/dev/null || fail "e2fsck reported errors" ;;
    ntfs) ntfsfix -n "$P" >/dev/null || fail "ntfsfix reported errors" ;;
    vfat) fsck.vfat -n "$P" >/dev/null || fail "fsck.vfat reported errors" ;;
  esac
  mount -o ro "$P" "$W/mnt"
  (cd "$W/mnt" && md5sum -c --quiet "$W/sums") || fail "$FS file contents differ"
  umount "$W/mnt"; losetup -d "$LOOP"
  echo "$FS OK  (image $(du -h "$IMG" | cut -f1) for a 512M disk)"
done
echo "ALL FILESYSTEM ROUND TRIPS PASSED"
