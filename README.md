# IMAGen

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
because it lives in sectors IMAGen always copies verbatim:

* MBR disk signature (offset 0x1B8), which Windows uses to map drive letters and BCD boot entries
* GPT disk GUID and every partition's unique GUID
* NTFS volume serial, FAT volume ID, ext UUID, labels

The drive's **hardware serial number and model** (what SMART reports) are
burned into the drive's firmware. No imaging tool can change them.

> ⚠️ Because the copy is identical, Windows sees a **signature collision** if
> the original and the clone are connected to the same Windows PC at the same
> time. It will take one of them offline. That is expected. Use one at a time.

## How "used data only" works

IMAGen reads each filesystem's own allocation map:

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

1. Download **`imagen-usb.exe`** from the project's GitHub Releases page and
   run it. Click **Yes** on the Windows administrator prompt, which is needed
   to write a USB stick.
2. **Boot image**: click **Download latest**, or **Choose ISO file…** if you
   already have `imagen-…-amd64.iso`. An ISO placed next to the program or in
   Downloads is picked up automatically.
3. **USB stick**: plug in a stick (8 GB or larger) and select it. Only USB and
   SD drives are listed. Internal, boot and system disks are never shown.
4. **Write**: click **Write USB stick**, then **Erase and write**. The app writes the
   image, reads it all back to verify it, and tells you when the stick is ready. If Windows then
   offers to format the stick, click **Cancel**.

Before erasing, the app checks again that the selected drive is the same one
(model and size), still on USB, and not a system disk. It refuses if the ISO or
the program itself is stored on that stick.

**Only one USB port?** When the PC starts from the stick, IMAGen copies itself
into memory (about 1 GB). Once the top bar says **Running from memory · USB
stick can be removed**, pull the stick out and plug in the drive with your
backups. On a PC with too little memory (under about 3 GB) it runs from the
stick as before, and the note does not appear.

**Convert a backup to .img**: the same program can turn an IMAGen backup
(`.bci`) into a plain disk image (`.img`) that data-recovery tools such as
Digger, and virtual machines, can open. Choose the backup, check where the
`.img` goes, and click **Convert to .img**. The backup is not changed. On an
NTFS drive the areas the backup skipped take no space. Only an *Every sector*
backup holds deleted files; in a *Used data only* backup free space reads as
zeros.

The stick then holds only the boot system. Store backup images on a separate
external drive. (Rufus or balenaEtcher in "DD image" mode also work.)

## Using the boot USB

1. Plug the stick into the PC whose disk you want to image.
2. Boot the PC from the stick (boot menu: usually F12, F11, F8 or Esc). It works with both UEFI and legacy BIOS. You may
   need to disable Secure Boot.
3. The IMAGen interface opens full screen:
   * **Back up**: pick the disk, choose *Used data only* or *Every sector*,
     then choose the drive to save on. Each drive shows its free space and
     whether the image fits. The image goes into an **IMAGen** folder at the
     top of that drive (created when the backup starts); **Change folder**
     picks another one. Then click **Start backup**.
   * **Restore**: IMAGen lists the images it finds on your drives (in their
     IMAGen folders and at the top of each drive); pick one, or **Browse for
     another image**. Pick the disk to overwrite, check the image → disk
     summary, then click **Erase and restore**. Checking the image first is
     optional (off by default).
   * **Verify**: check an image without writing anything.
   * **Accounts**: lists the user accounts of every Windows installation on the
     PC: local, Microsoft, work or school (Azure AD) and domain accounts, with
     administrator rights, enabled/disabled, last sign-in and profile folder.
     It mounts each Windows partition read-only, reads the registry (SAM,
     SOFTWARE, SYSTEM) and unmounts it again. Nothing is changed and no
     passwords are read. BitLocker-encrypted partitions are listed as unreadable.
4. A text console is on **Ctrl+Alt+F2** (user `user`, password `live`) for the
   command line.
5. The interface is zoomed to suit the screen, as Windows' display scaling does:
   a laptop panel by its pixel density (a 14" 2560×1600 panel gets 200%), other
   screens by resolution. To choose the zoom yourself, press **e** in the boot
   menu, add `imagen.scale=1.5` (1 to 3) to the `linux` line and press
   **Ctrl+X** or **F10**. If the screen stays blank, choose
   *Live system (amd64 fail-safe mode)* in the boot menu.

## Command line

```
imagen list
imagen analyze /dev/sda                          # what would be copied
imagen backup  -source /dev/sda -out /mnt/x/pc.bci -mode used -verify
imagen backup  -source /dev/sda -out /mnt/x/pc.bci -mode full -split 4000M
imagen restore -image /mnt/x/pc.bci -target /dev/sdb
imagen verify  /mnt/x/pc.bci
imagen info    /mnt/x/pc.bci
imagen export  /mnt/x/pc.bci /mnt/x/pc.img        # plain disk image for other tools
imagen accounts                                  # Windows user accounts on all disks
imagen gui                                       # web UI on 127.0.0.1:8080
```

On Windows or macOS the same binary works with disk *image files* (for
development and testing). Raw disk access is Linux-only, which is why the boot
USB runs Linux.

## Building

**Binary** (any OS with Go 1.22+):

```
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o imagen ./cmd/imagen
```

**Windows USB creator**:

```
go build -ldflags "-H windowsgui -X main.releaseRepo=OWNER/REPO" -o imagen-usb.exe ./cmd/imagen-usb
```

`releaseRepo` sets where **Download latest** fetches
`releases/latest/download/imagen-amd64.iso` from. Without it, the app only
uses local ISO files.

**Bootable ISO** needs Linux (Debian `live-build`). Choose one:

* **GitHub Actions** (easiest from Windows): push this repo to GitHub, then
  push a tag such as `v0.1.0`. The `build` workflow runs all tests, builds the
  ISO and `imagen-usb.exe`, and publishes both as a GitHub Release (about
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
* `sudo scripts/fs-integration-test.sh ./imagen` (Linux) runs the
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
cmd/imagen          CLI entry point (runs on the boot USB)
cmd/imagen-usb      Windows USB creator (elevation, USB-only disk list, write + verify)
internal/part       MBR/GPT parsing, GPT backup relocation
internal/fs         NTFS / ext / FAT used-block detection, fs identification
internal/image      .bci container, split files
internal/engine     analysis plan, backup / restore / verify pipelines
internal/disk       Linux disk enumeration, safety checks, mount
internal/winreg     read-only Windows registry hive reader
internal/winacct    Windows account listing (SAM, ProfileList)
internal/gui        web UI (embedded) shown in the kiosk
live/               Debian live-build config for the boot USB
scripts/            ISO build and real-filesystem test scripts
tools/icongen       draws the icon: icon.ico, and the .syso that embeds it
                    (with a manifest) in imagen-usb.exe. Rerun after changing it:
                    go run ./tools/icongen -out cmd/imagen-usb/winres -syso cmd/imagen-usb/rsrc_windows_amd64.syso
```
