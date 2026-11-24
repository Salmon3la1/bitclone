package fs

import (
	"errors"
	"fmt"
	"io"
	"unicode/utf16"

	"imagen/internal/extent"
)

// ntfs reads the $Bitmap metadata file (MFT record 6) to find used clusters.
func ntfs(r io.ReaderAt, size int64, bs []byte, withUsed bool) (*Result, error) {
	bps := int64(le16(bs[0x0B:]))
	spcRaw := bs[0x0D]
	var spc int64
	if spcRaw > 0x80 {
		spc = 1 << (256 - int(spcRaw))
	} else {
		spc = int64(spcRaw)
	}
	if bps < 256 || bps > 4096 || bps&(bps-1) != 0 || spc == 0 {
		return nil, errors.New("ntfs: bad geometry")
	}
	cs := bps * spc
	totalSectors := int64(le64(bs[0x28:]))
	mftLCN := int64(le64(bs[0x30:]))
	cpr := int8(bs[0x40])
	var recSize int64
	if cpr > 0 {
		recSize = int64(cpr) * cs
	} else {
		recSize = 1 << uint(-cpr)
	}
	if recSize < 512 || recSize > 65536 {
		return nil, errors.New("ntfs: bad MFT record size")
	}
	volBytes := totalSectors * bps
	res := &Result{FS: "ntfs", FSSize: volBytes, UUID: fmt.Sprintf("%016X", le64(bs[0x48:]))}

	mftOff := mftLCN * cs
	if rec, err := readRecord(r, mftOff+3*recSize, recSize); err == nil {
		if a := findAttr(rec, 0x60); a != nil && a[8] == 0 { // $VOLUME_NAME, resident
			n := int(le32(a[0x10:]))
			o := int(le16(a[0x14:]))
			if o+n <= len(a) {
				res.Label = utf16le(a[o : o+n])
			}
		}
	}
	if !withUsed {
		return res, nil
	}

	rec, err := readRecord(r, mftOff+6*recSize, recSize)
	if err != nil {
		return nil, fmt.Errorf("ntfs: $Bitmap record: %w", err)
	}
	a := findAttr(rec, 0x80) // unnamed $DATA
	if a == nil {
		return nil, errors.New("ntfs: $Bitmap has no $DATA attribute (attribute list not supported)")
	}
	nClusters := totalSectors / spc
	var bitmap []byte
	if a[8] == 0 {
		n := int(le32(a[0x10:]))
		o := int(le16(a[0x14:]))
		if o+n > len(a) {
			return nil, errors.New("ntfs: bad resident $Bitmap")
		}
		bitmap = a[o : o+n]
	} else {
		runOff := int(le16(a[0x20:]))
		realSize := int64(le64(a[0x30:]))
		if runOff >= len(a) || realSize < (nClusters+7)/8 || realSize > 1<<32 {
			return nil, errors.New("ntfs: bad $Bitmap size")
		}
		runs, err := decodeRuns(a[runOff:])
		if err != nil {
			return nil, err
		}
		bitmap = make([]byte, 0, realSize)
		for _, rn := range runs {
			want := rn.length * cs
			if left := realSize - int64(len(bitmap)); want > left {
				want = left
			}
			if want <= 0 {
				break
			}
			if rn.sparse {
				bitmap = append(bitmap, make([]byte, want)...)
				continue
			}
			b, err := readAt(r, rn.lcn*cs, int(want))
			if err != nil {
				return nil, fmt.Errorf("ntfs: reading $Bitmap: %w", err)
			}
			bitmap = append(bitmap, b...)
		}
		if int64(len(bitmap)) < (nClusters+7)/8 {
			return nil, errors.New("ntfs: $Bitmap shorter than volume")
		}
	}
	used := extent.FromBitmap(bitmap, nClusters, cs, 0)
	// Always keep the boot area and the backup boot sector, which lives in
	// the sector just past the volume (normally the partition's last sector)
	// and is not covered by $Bitmap.
	used = append(used, extent.Extent{Off: 0, Len: min64(size, 64<<10)})
	used = append(used, extent.Extent{Off: (nClusters * cs), Len: volBytes - nClusters*cs})
	used = append(used, tailExtent(volBytes, size)...)
	res.Used = extent.Normalize(extent.Clamp(used, 0, size), 0)
	return res, nil
}

func readRecord(r io.ReaderAt, off, n int64) ([]byte, error) {
	rec, err := readAt(r, off, int(n))
	if err != nil {
		return nil, err
	}
	if string(rec[0:4]) != "FILE" {
		return nil, errors.New("ntfs: bad MFT record magic")
	}
	usaOff := int(le16(rec[4:]))
	usaCnt := int(le16(rec[6:]))
	if usaCnt < 2 || usaOff+usaCnt*2 > len(rec) {
		return nil, errors.New("ntfs: bad update sequence array")
	}
	stride := len(rec) / (usaCnt - 1)
	u0, u1 := rec[usaOff], rec[usaOff+1]
	for i := 1; i < usaCnt; i++ {
		p := i*stride - 2
		if p+2 > len(rec) {
			break
		}
		if rec[p] != u0 || rec[p+1] != u1 {
			return nil, errors.New("ntfs: MFT record fixup mismatch")
		}
		rec[p], rec[p+1] = rec[usaOff+2*i], rec[usaOff+2*i+1]
	}
	return rec, nil
}

// findAttr returns the first unnamed attribute of the given type.
func findAttr(rec []byte, typ uint32) []byte {
	off := int(le16(rec[0x14:]))
	for off+16 <= len(rec) {
		t := le32(rec[off:])
		if t == 0xFFFFFFFF {
			break
		}
		l := int(le32(rec[off+4:]))
		if l < 16 || off+l > len(rec) {
			break
		}
		if t == typ && rec[off+9] == 0 {
			return rec[off : off+l]
		}
		off += l
	}
	return nil
}

type run struct {
	lcn, length int64
	sparse      bool
}

func decodeRuns(b []byte) ([]run, error) {
	var out []run
	var lcn int64
	i := 0
	for i < len(b) && b[i] != 0 {
		h := b[i]
		ln, on := int(h&0x0F), int(h>>4)
		i++
		if ln == 0 || ln > 8 || on > 8 || i+ln+on > len(b) {
			return nil, errors.New("ntfs: corrupt run list")
		}
		var length int64
		for k := 0; k < ln; k++ {
			length |= int64(b[i+k]) << (8 * k)
		}
		var off int64
		for k := 0; k < on; k++ {
			off |= int64(b[i+ln+k]) << (8 * k)
		}
		if on > 0 && on < 8 {
			shift := uint(64 - 8*on)
			off = off << shift >> shift // sign-extend
		}
		i += ln + on
		if on == 0 {
			out = append(out, run{length: length, sparse: true})
			continue
		}
		lcn += off
		if lcn < 0 || length <= 0 {
			return nil, errors.New("ntfs: corrupt run list")
		}
		out = append(out, run{lcn: lcn, length: length})
	}
	return out, nil
}

func utf16le(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le16(b[2*i:])
	}
	return string(utf16.Decode(u))
}
