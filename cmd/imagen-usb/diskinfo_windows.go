//go:build windows

package main

// Native drive details, the same way Digger finds them: drive letters are
// matched to physical disks through the volume's disk extents, the make and
// model come from the storage device descriptor, and a disk or partition
// Windows gives no letter (an ISO image written to a stick, a Linux
// partition) is named from its own on-disk label.

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetLogicalDrives      = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW         = kernel32.NewProc("GetDriveTypeW")
	procGetVolumeInformationW = kernel32.NewProc("GetVolumeInformationW")
)

const (
	ioctlVolumeGetVolumeDiskExtents = 0x00560000
	ioctlStorageQueryProperty       = 0x002D1400
	driveRemovable                  = 2
	driveFixed                      = 3
)

// letterVolume is a drive letter and the disks it sits on.
type letterVolume struct {
	Letter string
	Label  string
	FS     string
	Disks  []int
}

// letterVolumes lists the drive letters on fixed and removable drives with
// their label, file system and the physical disks under them.
func letterVolumes() []letterVolume {
	mask, _, _ := procGetLogicalDrives.Call()
	var out []letterVolume
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		l := string(rune('A' + i))
		root, _ := syscall.UTF16PtrFromString(l + `:\`)
		t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(root)))
		if t != driveRemovable && t != driveFixed {
			continue
		}
		v := letterVolume{Letter: l, Disks: volumeDisks(`\\.\` + l + `:`)}
		var name, fs [261]uint16
		if r, _, _ := procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(root)),
			uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)), 0, 0, 0,
			uintptr(unsafe.Pointer(&fs[0])), uintptr(len(fs))); r != 0 {
			v.Label = syscall.UTF16ToString(name[:])
			v.FS = syscall.UTF16ToString(fs[:])
		}
		out = append(out, v)
	}
	return out
}

// volumeDisks returns the disk numbers a volume such as \\.\E: occupies.
// The volume is opened without read access, so this works without
// administrator rights and for volumes that cannot be read.
func volumeDisks(path string) []int {
	p, _ := syscall.UTF16PtrFromString(path)
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|4, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(h)
	buf := make([]byte, 8+24*16)
	var n uint32
	if syscall.DeviceIoControl(h, ioctlVolumeGetVolumeDiskExtents, nil, 0, &buf[0], uint32(len(buf)), &n, nil) != nil {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(buf))
	var disks []int
	for i := 0; i < count && i < 16; i++ {
		disks = append(disks, int(binary.LittleEndian.Uint32(buf[8+i*24:])))
	}
	return disks
}

// diskDescriptor is what the storage device descriptor says about a disk.
type diskDescriptor struct {
	Name      string // vendor and product, e.g. "SanDisk Cruzer Blade"
	Serial    string
	Removable bool
	External  bool // USB, FireWire, SD or MMC
}

func describeDisk(n int) (d diskDescriptor) {
	p, _ := syscall.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, n))
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	query := make([]byte, 12) // StorageDeviceProperty, PropertyStandardQuery
	buf := make([]byte, 1024)
	var got uint32
	if syscall.DeviceIoControl(h, ioctlStorageQueryProperty, &query[0], uint32(len(query)), &buf[0], uint32(len(buf)), &got, nil) != nil || got < 32 {
		return
	}
	text := func(at int) string {
		o := int(binary.LittleEndian.Uint32(buf[at:]))
		if o <= 0 || o >= len(buf) {
			return ""
		}
		end := o
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		return cleanText(buf[o:end])
	}
	d.Removable = buf[10] != 0
	bus := binary.LittleEndian.Uint32(buf[28:])
	d.External = d.Removable || bus == 7 || bus == 4 || bus == 0xC || bus == 0xD
	d.Name = strings.TrimSpace(text(12) + " " + text(16))
	d.Serial = text(24)
	return
}

// cleanText keeps printable ASCII and trims it; device strings are often
// padded with spaces or contain junk bytes.
func cleanText(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			s.WriteByte(c)
		}
	}
	return strings.Join(strings.Fields(s.String()), " ")
}

// rawVolume is a partition or disk found by reading the disk itself.
type rawVolume struct {
	Label string `json:"label"`
	FS    string `json:"fs"`
	Size  int64  `json:"size"`
}

// readRawVolumes reads disk n's partition table and names what is on it,
// the way Digger does for partitions without a drive letter. It needs
// administrator rights; without them it returns nothing.
func readRawVolumes(n int) []rawVolume {
	f, err := os.Open(fmt.Sprintf(`\\.\PhysicalDrive%d`, n))
	if err != nil {
		return nil
	}
	defer f.Close()
	read := func(off int64, size int) []byte {
		b := make([]byte, size)
		if _, err := f.ReadAt(b, off); err != nil {
			return nil
		}
		return b
	}
	var vols []rawVolume
	// An installer or ImaGen image written straight to the stick carries
	// an ISO-9660 header at 32 KB naming the whole disk.
	if b := read(32768, 2048); b != nil && string(b[1:6]) == "CD001" {
		if l := cleanText(b[40:72]); l != "" {
			vols = append(vols, rawVolume{Label: l, FS: "ISO 9660"})
		}
	}
	for _, p := range partitions(read) {
		fs, label := identify(read, p.off)
		if fs == "" {
			continue
		}
		vols = append(vols, rawVolume{Label: label, FS: fs, Size: p.size})
	}
	return vols
}

type partEntry struct{ off, size int64 }

// partitions lists the primary MBR partitions, or the GPT ones.
func partitions(read func(int64, int) []byte) []partEntry {
	mbr := read(0, 512)
	if mbr == nil || mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil
	}
	var parts []partEntry
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i:]
		typ := e[4]
		start := int64(binary.LittleEndian.Uint32(e[8:]))
		count := int64(binary.LittleEndian.Uint32(e[12:]))
		switch {
		case typ == 0xEE:
			return gptPartitions(read)
		case typ == 0 || typ == 0x05 || typ == 0x0F || typ == 0x85 || count == 0:
			continue // empty or extended
		}
		parts = append(parts, partEntry{start * 512, count * 512})
	}
	return parts
}

func gptPartitions(read func(int64, int) []byte) []partEntry {
	hdr := read(512, 512)
	if hdr == nil || string(hdr[:8]) != "EFI PART" {
		return nil
	}
	lba := int64(binary.LittleEndian.Uint64(hdr[72:]))
	count := int(binary.LittleEndian.Uint32(hdr[80:]))
	size := int(binary.LittleEndian.Uint32(hdr[84:]))
	if size < 128 || size > 1024 || count <= 0 || count > 256 {
		return nil
	}
	tbl := read(lba*512, (count*size+511)/512*512)
	if tbl == nil {
		return nil
	}
	var parts []partEntry
	for i := 0; i < count; i++ {
		e := tbl[i*size:]
		empty := true
		for _, c := range e[:16] {
			if c != 0 {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		first := int64(binary.LittleEndian.Uint64(e[32:]))
		last := int64(binary.LittleEndian.Uint64(e[40:]))
		parts = append(parts, partEntry{first * 512, (last - first + 1) * 512})
	}
	return parts
}

// identify names the file system at off and its label when it has one.
func identify(read func(int64, int) []byte, off int64) (fs, label string) {
	b := read(off, 4096)
	if b == nil {
		return "", ""
	}
	switch {
	case string(b[3:11]) == "NTFS    ":
		return "NTFS", ""
	case string(b[3:11]) == "EXFAT   ":
		return "exFAT", ""
	case string(b[3:11]) == "-FVE-FS-":
		return "BitLocker", ""
	case string(b[82:87]) == "FAT32":
		return "FAT32", fatLabel(read, off, b, true)
	case string(b[54:57]) == "FAT":
		return "FAT", fatLabel(read, off, b, false)
	case b[1080] == 0x53 && b[1081] == 0xEF:
		return "ext4", cleanText(trimNul(b[1144:1160]))
	case string(b[0:6]) == "LUKS\xba\xbe":
		return "LUKS (encrypted)", ""
	}
	return "", ""
}

func trimNul(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// fatLabel prefers the volume-label entry in the root directory, which is
// what Windows shows; the boot sector's copy is often left as "NO NAME".
func fatLabel(read func(int64, int) []byte, off int64, b []byte, fat32 bool) string {
	bps := int64(binary.LittleEndian.Uint16(b[11:]))
	spc := int64(b[13])
	reserved := int64(binary.LittleEndian.Uint16(b[14:]))
	fats := int64(b[16])
	if (bps == 512 || bps == 1024 || bps == 2048 || bps == 4096) && spc > 0 && spc <= 128 && fats > 0 && fats <= 2 {
		var dirOff int64
		var dirLen int
		if fat32 {
			fatSize := int64(binary.LittleEndian.Uint32(b[36:]))
			root := int64(binary.LittleEndian.Uint32(b[44:]))
			if root >= 2 {
				dirOff = (reserved + fats*fatSize + (root-2)*spc) * bps
				dirLen = int(bps * spc)
			}
		} else {
			fatSize := int64(binary.LittleEndian.Uint16(b[22:]))
			entries := int(binary.LittleEndian.Uint16(b[17:]))
			dirOff = (reserved + fats*fatSize) * bps
			dirLen = (entries*32 + int(bps) - 1) / int(bps) * int(bps)
		}
		if dirLen > 0 && dirLen <= 256*1024 {
			if dir := read(off+dirOff, dirLen); dir != nil {
				for at := 0; at+32 <= len(dir) && dir[at] != 0; at += 32 {
					attr := dir[at+11]
					if dir[at] == 0xE5 || attr&0x0F == 0x0F || attr&0x08 == 0 {
						continue // deleted, long-name part, or not a label
					}
					if l := latin1(dir[at : at+11]); l != "" {
						return l
					}
				}
			}
		}
	}
	at := 43
	if fat32 {
		at = 71
	}
	if l := latin1(b[at : at+11]); l != "NO NAME" {
		return l
	}
	return ""
}

func latin1(b []byte) string {
	r := make([]uint16, len(b))
	for i, c := range b {
		r[i] = uint16(c)
	}
	return strings.TrimSpace(string(utf16.Decode(r)))
}
