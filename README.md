# bitclone

Sector-exact disk imaging that boots from a USB stick. Back up a whole disk to
an image file, then restore it to the same or another disk. The restored disk
behaves like the original, including its **disk signature**.

## What it does (MVP)

| Feature | Status |
|---|---|
| Image a whole disk to a `.bci` file | ✅ |
| **Used data only** mode: skips free space on NTFS, ext2/3/4, FAT12/16/32 | ✅ |
| **Every sector** mode: bit-for-bit copy of the entire disk | ✅ |
| Restore to a same-size or larger disk | ✅ |
| Keep disk identity: MBR disk signature, GPT disk GUID, partition GUIDs, filesystem serials/UUIDs | ✅ (always) |
| Move the GPT backup table to the end of a larger target without changing GUIDs | ✅ |
| Compression (deflate, multi-core), split into 4 GB files for FAT32 drives | ✅ |
| Integrity: CRC32C per chunk + SHA-256 over the whole image; verify command | ✅ |
| Safety: never writes to the boot USB, a mounted disk, or the disk holding the image | ✅ |
| Full-screen GUI on the boot USB + command line | ✅ |
| Restore to a *smaller* disk, partition resizing, bad-sector skipping, encryption | ❌ later |

### About "disk signature"

Everything that identifies a disk **on the disk itself** is copied exactly,
because it lives in sectors bitclone always copies verbatim:

* MBR disk signature (offset 0x1B8), which Windows uses to map drive letters and BCD boot entries
* GPT disk GUID and every partition's unique GUID
* NTFS volume serial, FAT volume ID, ext UUID, labels

The drive's **hardware serial number and model** (what SMART reports) are
burned into the drive's firmware. No imaging tool can change them.

> ⚠️ Because the copy is identical, Windows sees a **signature collision** if
> the original and the clone are connected to the same Windows PC at the same
> time. It will take one of them offline. That is expected. Use one at a time.

## How "used data only" works

bitclone reads each filesystem's own allocation map:

* **NTFS**: the `$Bitmap` metadata file (MFT record 6)
* **ext2/3/4**: block-group bitmaps (including `flex_bg` and uninitialised groups)
* **FAT12/16/32**: the File Allocation Table

It also always copies the partition tables, boot code, the gaps between
partitions (where boot loaders such as GRUB live), NTFS's backup boot sector
and the end of the disk. Partitions it cannot read (BitLocker, LUKS, LVM, XFS,
Btrfs, exFAT, unknown) are copied **sector by sector**, so nothing is ever
lost. Swap partitions keep only their header (UUID/label).

Before imaging Windows, shut it down fully (hold **Shift** while clicking Shut
down, or turn off Fast Startup). Otherwise NTFS is left in a hibernated state.

## Making the boot USB (Windows)

1. Download **`bitclone-usb.exe`** from the project's GitHub Releases page and
   run it. Click **Yes** on the Windows administrator prompt, which is needed
   to write a USB stick.
2. **Boot image**: click **Download latest**, or **Choose ISO file…** if you
   already have `bitclone-…-amd64.iso`. An ISO placed next to the program or in
   Downloads is picked up automatically.
3. **USB stick**: plug in a stick (8 GB or larger) and select it. Only USB and
   SD drives are listed. Internal, boot and system disks are never shown.
4. **Write**: type `ERASE` to confirm. The app writes the image, reads it all
   back to verify it, and tells you when the stick is ready. If Windows then
   offers to format the stick, click **Cancel**.

Before erasing, the app checks again that the selected drive is the same one
(model and size), still on USB, and not a system disk. It refuses if the ISO or
the program itself is stored on that stick.

The stick then holds only the boot system. Store backup images on a separate
external drive. (Rufus or balenaEtcher in "DD image" mode also work.)

## Using the boot USB

1. Plug the stick into the PC whose disk you want to image.
2. Boot the PC from the stick (boot menu: usually F12, F11, F8 or Esc). It works with both UEFI and legacy BIOS. You may
   need to disable Secure Boot.
3. The bitclone interface opens full screen:
   * **Back up**: pick the disk, choose *Used data only* or *Every sector*,
     mount a destination drive, pick a folder, then Start.
   * **Restore**: open the drive holding the image, pick the `.bci`, pick the
     target disk, then type its name to confirm.
   * **Verify**: check an image without writing anything.
4. A text console is on **Ctrl+Alt+F2** (user `user`, password `live`) for the
   command line.

## Command line

```
bitclone list
bitclone analyze /dev/sda                          # what would be copied
bitclone backup  -source /dev/sda -out /mnt/x/pc.bci -mode used -verify
bitclone backup  -source /dev/sda -out /mnt/x/pc.bci -mode full -split 4000M
bitclone restore -image /mnt/x/pc.bci -target /dev/sdb
bitclone verify  /mnt/x/pc.bci
bitclone info    /mnt/x/pc.bci
bitclone gui                                       # web UI on 127.0.0.1:8080
```

On Windows or macOS the same binary works with disk *image files* (for
development and testing). Raw disk access is Linux-only, which is why the boot
USB runs Linux.

## Building

**Binary** (any OS with Go 1.22+):

```
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bitclone ./cmd/bitclone
```

**Windows USB creator**:

```
go build -ldflags "-H windowsgui -X main.releaseRepo=OWNER/REPO" -o bitclone-usb.exe ./cmd/bitclone-usb
```

`releaseRepo` sets where **Download latest** fetches
`releases/latest/download/bitclone-amd64.iso` from. Without it, the app only
uses local ISO files.

**Bootable ISO** needs Linux (Debian `live-build`). Choose one:

* **GitHub Actions** (easiest from Windows): push this repo to GitHub, then
  push a tag such as `v0.1.0`. The `build` workflow runs all tests, builds the
  ISO and `bitclone-usb.exe`, and publishes both as a GitHub Release (about
  15 minutes). Every normal push also uploads them as build artifacts.
* **Docker** (Linux, or Docker Desktop with WSL2): `sh scripts/build-iso-docker.sh`
* **Debian/Ubuntu machine**: `sudo sh scripts/build-iso.sh`

The ISO lands in `dist/`.

## Testing

* `go test ./...` runs unit tests for the partition, NTFS, ext4 and FAT parsers
  on synthetic filesystems. It also runs end-to-end backup→restore tests:
  used-mode (free space skipped, identity kept, GPT moved on a larger
  target), full-mode (SHA-256 identical), split images, and corruption and
  truncation detection.
* `sudo scripts/fs-integration-test.sh ./bitclone` (Linux) runs the
  real-filesystem round trip. It builds ext4, NTFS and FAT32 with real `mkfs`,
  writes files, images them in used mode, and restores to a larger disk. Then
  it checks that `fsck` is clean, file checksums match, and the disk GUID,
  partition GUID and filesystem UUID are unchanged.

Test on real hardware before trusting it with irreplaceable data: back up,
restore to a spare disk, boot the spare disk.

## Image format (`.bci`)

```
"BCLIMG01" | version | JSON metadata (source disk, partition table, plan)
CHNK records: disk offset | raw length | stored length | CRC32C | codec | payload
ENDS record: chunk count | total bytes | SHA-256 of everything before it
```

Chunks are 4 MiB and stored in disk order. Codecs are raw, deflate, or
all-zero (zero chunks take no space). Large images continue in
`name.bci.001`, `name.bci.002`, …

## Layout

```
cmd/bitclone        CLI entry point (runs on the boot USB)
cmd/bitclone-usb    Windows USB creator (elevation, USB-only disk list, write + verify)
internal/part       MBR/GPT parsing, GPT backup relocation
internal/fs         NTFS / ext / FAT used-block detection, fs identification
internal/image      .bci container, split files
internal/engine     analysis plan, backup / restore / verify pipelines
internal/disk       Linux disk enumeration, safety checks, mount
internal/gui        web UI (embedded) shown in the kiosk
live/               Debian live-build config for the boot USB
scripts/            ISO build and real-filesystem test scripts
```
