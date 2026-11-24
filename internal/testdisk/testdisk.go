// Package testdisk builds small synthetic disks and filesystems for tests.
package testdisk

import (
	"encoding/binary"
	"hash/crc32"
	"math/rand"

	"bitclone/internal/extent"
)

// Part is a GPT partition to create (byte offsets must be 512-aligned).
type Part struct {
	Start, Size int64
	Type        [16]byte
	GUID        [16]byte
	Name        string
}

var BasicData = [16]byte{0xA2, 0xA0, 0xD0, 0xEB, 0xE5, 0xB9, 0x33, 0x44, 0x87, 0xC0, 0x68, 0xB6, 0xB7, 0x26, 0x99, 0xC7}

// GPT writes a protective MBR (with disk signature mbrSig), primary and backup
// GPT into disk.
func GPT(disk []byte, diskGUID [16]byte, mbrSig uint32, parts []Part) {
	const ss = 512
	size := int64(len(disk))
	last := uint64(size/ss) - 1
	mbr := disk[0:512]
	binary.LittleEndian.PutUint32(mbr[440:], mbrSig)
	e := mbr[446:]
	e[4] = 0xEE
	binary.LittleEndian.PutUint32(e[8:], 1)
	binary.LittleEndian.PutUint32(e[12:], uint32(last))
	mbr[510], mbr[511] = 0x55, 0xAA

	ents := make([]byte, 128*128)
	for i, p := range parts {
		en := ents[i*128:]
		copy(en[0:], p.Type[:])
		copy(en[16:], p.GUID[:])
		binary.LittleEndian.PutUint64(en[32:], uint64(p.Start/ss))
		binary.LittleEndian.PutUint64(en[40:], uint64((p.Start+p.Size)/ss-1))
		for j, c := range p.Name {
			binary.LittleEndian.PutUint16(en[56+2*j:], uint16(c))
		}
	}
	entCRC := crc32.ChecksumIEEE(ents)
	hdr := func(my, alt, entLBA uint64) []byte {
		h := make([]byte, ss)
		copy(h, "EFI PART")
		binary.LittleEndian.PutUint32(h[8:], 0x00010000)
		binary.LittleEndian.PutUint32(h[12:], 92)
		binary.LittleEndian.PutUint64(h[24:], my)
		binary.LittleEndian.PutUint64(h[32:], alt)
		binary.LittleEndian.PutUint64(h[40:], 34)
		binary.LittleEndian.PutUint64(h[48:], last-33)
		copy(h[56:], diskGUID[:])
		binary.LittleEndian.PutUint64(h[72:], entLBA)
		binary.LittleEndian.PutUint32(h[80:], 128)
		binary.LittleEndian.PutUint32(h[84:], 128)
		binary.LittleEndian.PutUint32(h[88:], entCRC)
		binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
		return h
	}
	copy(disk[ss:], hdr(1, last, 2))
	copy(disk[2*ss:], ents)
	copy(disk[(last-32)*ss:], ents)
	copy(disk[last*ss:], hdr(last, 1, last-32))
}

// Fill writes pseudo-random bytes.
func Fill(b []byte, seed int64) {
	r := rand.New(rand.NewSource(seed))
	r.Read(b)
}

// FAT16 formats p (whole partition) as FAT16 with 2 KiB clusters, marks the
// given cluster numbers in use, and fills the whole data area with random
// bytes (so tests can tell copied clusters from skipped ones). It returns the
// used extents expected from the detector.
func FAT16(p []byte, used []int, seed int64) []extent.Extent {
	Fill(p, seed)
	const bps, spc, reserved, nFATs, rootEntries = 512, 4, 4, 2, 512
	totSec := int64(len(p) / bps)
	fatSz := int64(64)
	bs := p[0:512]
	for i := range bs {
		bs[i] = 0
	}
	bs[0], bs[1], bs[2] = 0xEB, 0x3C, 0x90
	copy(bs[3:], "MSWIN4.1")
	binary.LittleEndian.PutUint16(bs[11:], bps)
	bs[13] = spc
	binary.LittleEndian.PutUint16(bs[14:], reserved)
	bs[16] = nFATs
	binary.LittleEndian.PutUint16(bs[17:], rootEntries)
	if totSec < 65536 {
		binary.LittleEndian.PutUint16(bs[19:], uint16(totSec))
	} else {
		binary.LittleEndian.PutUint32(bs[32:], uint32(totSec))
	}
	bs[21] = 0xF8
	binary.LittleEndian.PutUint16(bs[22:], uint16(fatSz))
	binary.LittleEndian.PutUint32(bs[39:], 0x1234ABCD)
	copy(bs[43:], "TESTVOL    ")
	copy(bs[54:], "FAT16   ")
	bs[510], bs[511] = 0x55, 0xAA
	for f := int64(0); f < nFATs; f++ {
		fat := p[(reserved+f*fatSz)*bps : (reserved+(f+1)*fatSz)*bps]
		for i := range fat {
			fat[i] = 0
		}
		binary.LittleEndian.PutUint16(fat[0:], 0xFFF8)
		binary.LittleEndian.PutUint16(fat[2:], 0xFFFF)
		for _, c := range used {
			binary.LittleEndian.PutUint16(fat[c*2:], 0xFFFF)
		}
	}
	rootSec := int64(rootEntries * 32 / bps)
	firstData := reserved + nFATs*fatSz + rootSec
	clusters := (totSec - firstData) / spc
	exp := []extent.Extent{{Off: 0, Len: firstData * bps}}
	for _, c := range used {
		exp = append(exp, extent.Extent{Off: (firstData + int64(c-2)*spc) * bps, Len: spc * bps})
	}
	exp = append(exp, extent.Extent{Off: (firstData + clusters*spc) * bps, Len: int64(len(p)) - (firstData+clusters*spc)*bps})
	return extent.Normalize(exp, 0)
}

// NTFS builds a minimal NTFS volume (boot sector, MFT record 6 with a
// non-resident $Bitmap) with 4 KiB clusters. len(p) must be a multiple of
// 4096. usedClusters are marked allocated. Returns the expected extents.
func NTFS(p []byte, usedClusters []int64, seed int64) []extent.Extent {
	Fill(p, seed)
	const bps, spc, cs = 512, 8, 4096
	const mftLCN, bitmapLCN = 4, 100
	totalSectors := int64(len(p)/bps) - 1
	nClusters := totalSectors / spc
	bs := p[0:512]
	for i := range bs {
		bs[i] = 0
	}
	bs[0], bs[1], bs[2] = 0xEB, 0x52, 0x90
	copy(bs[3:], "NTFS    ")
	binary.LittleEndian.PutUint16(bs[0x0B:], bps)
	bs[0x0D] = spc
	binary.LittleEndian.PutUint64(bs[0x28:], uint64(totalSectors))
	binary.LittleEndian.PutUint64(bs[0x30:], mftLCN)
	binary.LittleEndian.PutUint64(bs[0x38:], 2)
	bs[0x40] = 0xF6 // 2^10 = 1024-byte records
	binary.LittleEndian.PutUint64(bs[0x48:], 0x1122334455667788)
	bs[510], bs[511] = 0x55, 0xAA
	copy(p[totalSectors*bps:], bs) // backup boot sector

	all := append([]int64{0, 1, 2, 3, mftLCN, mftLCN + 1, bitmapLCN}, usedClusters...)
	bm := p[bitmapLCN*cs : bitmapLCN*cs+cs]
	for i := range bm {
		bm[i] = 0
	}
	for _, c := range all {
		bm[c/8] |= 1 << (c % 8)
	}
	// Bits past the end of the volume are set, as NTFS does.
	for c := nClusters; c < int64(len(bm))*8; c++ {
		bm[c/8] |= 1 << (c % 8)
	}

	rec := p[mftLCN*cs+6*1024 : mftLCN*cs+7*1024]
	for i := range rec {
		rec[i] = 0
	}
	copy(rec, "FILE")
	binary.LittleEndian.PutUint16(rec[4:], 0x30) // USA offset
	binary.LittleEndian.PutUint16(rec[6:], 3)    // USA count
	binary.LittleEndian.PutUint16(rec[0x14:], 0x38)
	a := rec[0x38:]
	binary.LittleEndian.PutUint32(a[0:], 0x80)
	binary.LittleEndian.PutUint32(a[4:], 0x48)
	a[8] = 1
	binary.LittleEndian.PutUint16(a[0x20:], 0x40)
	binary.LittleEndian.PutUint64(a[0x28:], cs)
	binary.LittleEndian.PutUint64(a[0x30:], uint64((nClusters+7)/8))
	binary.LittleEndian.PutUint64(a[0x38:], uint64((nClusters+7)/8))
	copy(a[0x40:], []byte{0x11, 0x01, bitmapLCN, 0x00})
	binary.LittleEndian.PutUint32(rec[0x38+0x48:], 0xFFFFFFFF)
	// Update sequence: usn 0x0001 at the end of each 512-byte stride,
	// original bytes saved in the array.
	usa := rec[0x30:]
	binary.LittleEndian.PutUint16(usa[0:], 1)
	for i := 1; i <= 2; i++ {
		pos := i*512 - 2
		copy(usa[2*i:2*i+2], rec[pos:pos+2])
		binary.LittleEndian.PutUint16(rec[pos:], 1)
	}

	var exp []extent.Extent
	for _, c := range all {
		exp = append(exp, extent.Extent{Off: c * cs, Len: cs})
	}
	exp = append(exp, extent.Extent{Off: 0, Len: 64 << 10})
	exp = append(exp, extent.Extent{Off: nClusters * cs, Len: int64(len(p)) - nClusters*cs})
	return extent.Normalize(exp, 0)
}

// Ext4 builds a two-group ext4-like filesystem with 4 KiB blocks, flex_bg
// style metadata in group 0 and group 1 flagged BLOCK_UNINIT. usedBlocks
// (beyond metadata) must lie in group 0. len(p) must be 65536*4096.
func Ext4(p []byte, usedBlocks []int64, seed int64) []extent.Extent {
	Fill(p, seed)
	const bs = 4096
	const bpg, ipg, inodeSize = 32768, 8192, 256
	blocks := int64(len(p) / bs)
	sb := p[1024:2048]
	for i := range sb {
		sb[i] = 0
	}
	binary.LittleEndian.PutUint32(sb[0:], ipg*2)
	binary.LittleEndian.PutUint32(sb[4:], uint32(blocks))
	binary.LittleEndian.PutUint32(sb[20:], 0)
	binary.LittleEndian.PutUint32(sb[24:], 2)
	binary.LittleEndian.PutUint32(sb[32:], bpg)
	binary.LittleEndian.PutUint32(sb[40:], ipg)
	binary.LittleEndian.PutUint16(sb[56:], 0xEF53)
	binary.LittleEndian.PutUint32(sb[76:], 1)
	binary.LittleEndian.PutUint16(sb[88:], inodeSize)
	binary.LittleEndian.PutUint32(sb[92:], 0x4)       // has_journal
	binary.LittleEndian.PutUint32(sb[96:], 0x240)     // extents | flex_bg
	binary.LittleEndian.PutUint32(sb[100:], 0x1|0x10) // sparse_super | gdt_csum
	copy(sb[104:], []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	copy(sb[120:], "rootfs")

	itBlocks := int64(ipg * inodeSize / bs)
	gd := p[bs : 2*bs]
	for i := range gd {
		gd[i] = 0
	}
	// flex_bg layout: both block bitmaps, both inode bitmaps, both inode tables.
	meta := [][3]int64{{2, 4, 6}, {3, 5, 6 + itBlocks}}
	for g, m := range meta {
		d := gd[g*32:]
		binary.LittleEndian.PutUint32(d[0:], uint32(m[0]))
		binary.LittleEndian.PutUint32(d[4:], uint32(m[1]))
		binary.LittleEndian.PutUint32(d[8:], uint32(m[2]))
	}
	binary.LittleEndian.PutUint16(gd[32+18:], 0x2) // group 1 BLOCK_UNINIT
	metaEnd := 6 + 2*itBlocks
	bm := p[2*bs : 3*bs]
	for i := range bm {
		bm[i] = 0
	}
	mark := func(b int64) { bm[b/8] |= 1 << (b % 8) }
	for b := int64(0); b < metaEnd; b++ {
		mark(b)
	}
	for _, b := range usedBlocks {
		mark(b)
	}
	exp := []extent.Extent{{Off: 0, Len: metaEnd * bs}}
	for _, b := range usedBlocks {
		exp = append(exp, extent.Extent{Off: b * bs, Len: bs})
	}
	exp = append(exp, extent.Extent{Off: bpg * bs, Len: 2 * bs}) // group 1 superblock + GDT backup
	return extent.Normalize(exp, 0)
}
