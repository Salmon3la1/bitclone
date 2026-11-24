package fs

import (
	"errors"
	"fmt"
	"io"

	"imagen/internal/extent"
)

const (
	extCompatResizeInode = 0x10
	extIncompat64Bit     = 0x80
	extIncompatMetaBG    = 0x10
	extROSparseSuper     = 0x1
	extROGdtCsum         = 0x10
	extROMetadataCsum    = 0x400
	extBGBlockUninit     = 0x2
)

// ext reads the block-group bitmaps of an ext2/3/4 filesystem.
func ext(r io.ReaderAt, size int64, sb []byte, withUsed bool) (*Result, error) {
	logBS := le32(sb[24:])
	if logBS > 6 {
		return nil, errors.New("ext: bad block size")
	}
	bs := int64(1024) << logBS
	blocks := int64(le32(sb[4:]))
	compat := le32(sb[92:])
	incompat := le32(sb[96:])
	roCompat := le32(sb[100:])
	if incompat&extIncompat64Bit != 0 {
		blocks |= int64(le32(sb[0x150:])) << 32
	}
	firstData := int64(le32(sb[20:]))
	bpg := int64(le32(sb[32:]))
	ipg := int64(le32(sb[40:]))
	inodeSize := int64(128)
	if le32(sb[76:]) >= 1 {
		inodeSize = int64(le16(sb[88:]))
	}
	name := "ext2"
	switch {
	case incompat&0x2C0 != 0 || roCompat&0x478 != 0: // extents/64bit/flex_bg, huge_file/dir_nlink/extra_isize/metadata_csum
		name = "ext4"
	case compat&0x4 != 0: // has_journal
		name = "ext3"
	}
	res := &Result{FS: name, FSSize: blocks * bs, UUID: uuidString(sb[104:120]), Label: cstr(sb[120:136])}
	if !withUsed {
		return res, nil
	}
	if bpg == 0 || bpg > 8*bs || blocks <= firstData || inodeSize == 0 {
		return nil, errors.New("ext: bad geometry")
	}
	if incompat&extIncompatMetaBG != 0 {
		return nil, errors.New("ext: meta_bg layout not supported")
	}
	descSize := int64(32)
	if incompat&extIncompat64Bit != 0 {
		if ds := int64(le16(sb[254:])); ds >= 64 {
			descSize = ds
		}
	}
	groups := (blocks - firstData + bpg - 1) / bpg
	gdtBytes := groups * descSize
	gdtBlocks := (gdtBytes + bs - 1) / bs
	reservedGdt := int64(0)
	if compat&extCompatResizeInode != 0 {
		reservedGdt = int64(le16(sb[206:]))
	}
	gdt, err := readAt(r, (firstData+1)*bs, int(gdtBytes))
	if err != nil {
		return nil, fmt.Errorf("ext: group descriptors: %w", err)
	}
	csumOK := roCompat&(extROGdtCsum|extROMetadataCsum) != 0
	sparse := roCompat&extROSparseSuper != 0
	itBlocks := (ipg*inodeSize + bs - 1) / bs

	var used []extent.Extent
	blk := func(b, n int64) { used = append(used, extent.Extent{Off: b * bs, Len: n * bs}) }
	// Everything before the first group (boot block when block size is 1 KiB).
	used = append(used, extent.Extent{Off: 0, Len: min64(size, 64<<10)})

	for g := int64(0); g < groups; g++ {
		d := gdt[g*descSize:]
		bb := int64(le32(d[0:]))
		ib := int64(le32(d[4:]))
		it := int64(le32(d[8:]))
		if descSize >= 64 {
			bb |= int64(le32(d[0x20:])) << 32
			ib |= int64(le32(d[0x24:])) << 32
			it |= int64(le32(d[0x28:])) << 32
		}
		flags := le16(d[18:])
		gStart := firstData + g*bpg
		gLen := bpg
		if gStart+gLen > blocks {
			gLen = blocks - gStart
		}
		// Group metadata is used regardless of where it lives (flex_bg may
		// place it in another group, possibly an uninitialized one).
		blk(bb, 1)
		blk(ib, 1)
		blk(it, itBlocks)
		if hasSuper(g, sparse) {
			blk(gStart, 1+gdtBlocks+reservedGdt)
		}
		if csumOK && flags&extBGBlockUninit != 0 {
			continue // no data blocks allocated in this group
		}
		if bb <= 0 || bb >= blocks {
			return nil, fmt.Errorf("ext: group %d block bitmap out of range", g)
		}
		bm, err := readAt(r, bb*bs, int(bs))
		if err != nil {
			return nil, fmt.Errorf("ext: group %d bitmap: %w", g, err)
		}
		used = append(used, extent.FromBitmap(bm, gLen, bs, gStart*bs)...)
	}
	used = append(used, tailExtent(blocks*bs, size)...)
	res.Used = extent.Normalize(extent.Clamp(used, 0, size), 0)
	return res, nil
}

func hasSuper(g int64, sparse bool) bool {
	if !sparse || g <= 1 {
		return true
	}
	for _, base := range []int64{3, 5, 7} {
		n := base
		for n < g {
			n *= base
		}
		if n == g {
			return true
		}
	}
	return false
}
