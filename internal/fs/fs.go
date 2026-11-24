// Package fs identifies filesystems inside a partition and, for NTFS, ext2/3/4
// and FAT12/16/32, reads their allocation bitmaps to find the blocks in use.
//
// Every detector is conservative: anything it cannot fully account for (boot
// areas, backup boot sectors, the tail of a partition past the end of the
// filesystem) is reported as used, so a "used blocks only" image always
// restores to a working filesystem.
package fs

import (
	"bytes"
	"encoding/binary"
	"io"

	"imagen/internal/extent"
)

// Result describes a filesystem found in a partition.
type Result struct {
	FS     string `json:"fs"`              // "ntfs", "ext4", "fat32", "swap", "bitlocker", ...
	Label  string `json:"label,omitempty"` // volume label
	UUID   string `json:"uuid,omitempty"`  // volume serial / UUID
	FSSize int64  `json:"fsSize,omitempty"`
	// Used lists the byte ranges (relative to partition start) that must be
	// copied. nil means "unknown: copy the whole partition".
	Used []extent.Extent `json:"-"`
	// Err is set when the filesystem was recognized but its bitmap could not
	// be read; the partition is then copied in full.
	Err string `json:"error,omitempty"`
}

// UsedBytes returns the number of bytes in Used, or -1 when unknown.
func (r *Result) UsedBytes() int64 {
	if r == nil || r.Used == nil {
		return -1
	}
	return extent.Total(r.Used)
}

func readAt(r io.ReaderAt, off int64, n int) ([]byte, error) {
	b := make([]byte, n)
	m, err := r.ReadAt(b, off)
	if m == n {
		return b, nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

var le16 = func(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
var le32 = func(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
var le64 = func(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// Identify recognizes the filesystem without reading allocation bitmaps. It is
// cheap and meant for listing disks. Returns nil if nothing is recognized.
func Identify(r io.ReaderAt, size int64) *Result {
	return probe(r, size, false)
}

// Detect recognizes the filesystem and computes its used extents. Returns nil
// if nothing is recognized (caller should copy the partition in full).
func Detect(r io.ReaderAt, size int64) *Result {
	return probe(r, size, true)
}

func probe(r io.ReaderAt, size int64, withUsed bool) *Result {
	head, err := readAt(r, 0, 4096)
	if err != nil {
		return nil
	}
	fail := func(res *Result, err error) *Result {
		res.Used = nil
		res.Err = err.Error()
		return res
	}
	switch {
	case string(head[3:11]) == "NTFS    ":
		res, err := ntfs(r, size, head, withUsed)
		if err != nil {
			return fail(&Result{FS: "ntfs"}, err)
		}
		return res
	case string(head[3:11]) == "-FVE-FS-":
		return &Result{FS: "bitlocker"}
	case string(head[3:11]) == "EXFAT   ":
		return &Result{FS: "exfat", UUID: serial32(le32(head[100:]))}
	case bytes.Equal(head[0:6], []byte("LUKS\xba\xbe")):
		return &Result{FS: "luks"}
	case string(head[0:4]) == "XFSB":
		return &Result{FS: "xfs"}
	case string(head[4086:4096]) == "SWAPSPACE2":
		// Swap holds no data that survives a reboot; keep only its header
		// (label/UUID) so fstab references still resolve.
		res := &Result{FS: "swap", UUID: uuidString(head[1036:1052]), Label: cstr(head[1052:1068])}
		if withUsed {
			res.Used = []extent.Extent{{Off: 0, Len: min64(size, 1<<20)}}
		}
		return res
	case string(head[512:520]) == "LABELONE" || string(head[0:8]) == "LABELONE":
		return &Result{FS: "lvm2"}
	}
	if len(head) >= 1024+1024 && le16(head[1024+56:]) == 0xEF53 {
		res, err := ext(r, size, head[1024:2048], withUsed)
		if err != nil {
			return fail(&Result{FS: "ext"}, err)
		}
		return res
	}
	if looksFAT(head) {
		res, err := fat(r, size, head, withUsed)
		if err != nil {
			return fail(&Result{FS: "fat"}, err)
		}
		return res
	}
	if b, err := readAt(r, 0x10040, 8); err == nil && string(b) == "_BHRfS_M" {
		return &Result{FS: "btrfs"}
	}
	return nil
}

// tailExtent returns [fsEnd, partSize) if non-empty: the part of the
// partition past the end of the filesystem, copied verbatim.
func tailExtent(fsEnd, partSize int64) []extent.Extent {
	if partSize > fsEnd {
		return []extent.Extent{{Off: fsEnd, Len: partSize - fsEnd}}
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(bytes.TrimRight(b, " "))
}

func serial32(v uint32) string {
	return hex4(uint16(v>>16)) + "-" + hex4(uint16(v))
}

func hex4(v uint16) string {
	const h = "0123456789ABCDEF"
	return string([]byte{h[v>>12&15], h[v>>8&15], h[v>>4&15], h[v&15]})
}

func uuidString(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, h[c>>4], h[c&15])
	}
	return string(out)
}
