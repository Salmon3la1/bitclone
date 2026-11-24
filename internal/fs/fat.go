package fs

import (
	"errors"
	"fmt"
	"io"

	"bitclone/internal/extent"
)

type fatGeom struct {
	bps, spc, reserved, nFATs, rootEntries, totSec, fatSz int64
	rootDirSec, firstData, clusters                       int64
	kind                                                  string
}

func fatGeometry(bs []byte) (*fatGeom, error) {
	g := &fatGeom{
		bps:         int64(le16(bs[11:])),
		spc:         int64(bs[13]),
		reserved:    int64(le16(bs[14:])),
		nFATs:       int64(bs[16]),
		rootEntries: int64(le16(bs[17:])),
		totSec:      int64(le16(bs[19:])),
		fatSz:       int64(le16(bs[22:])),
	}
	if g.totSec == 0 {
		g.totSec = int64(le32(bs[32:]))
	}
	if g.fatSz == 0 {
		g.fatSz = int64(le32(bs[36:]))
	}
	switch g.bps {
	case 512, 1024, 2048, 4096:
	default:
		return nil, errors.New("fat: bad bytes/sector")
	}
	if g.spc == 0 || g.spc&(g.spc-1) != 0 || g.reserved == 0 || g.nFATs == 0 || g.nFATs > 4 || g.fatSz == 0 || g.totSec == 0 {
		return nil, errors.New("fat: bad BPB")
	}
	g.rootDirSec = (g.rootEntries*32 + g.bps - 1) / g.bps
	g.firstData = g.reserved + g.nFATs*g.fatSz + g.rootDirSec
	if g.firstData >= g.totSec {
		return nil, errors.New("fat: bad layout")
	}
	g.clusters = (g.totSec - g.firstData) / g.spc
	switch {
	case g.clusters < 4085:
		g.kind = "fat12"
	case g.clusters < 65525:
		g.kind = "fat16"
	default:
		g.kind = "fat32"
	}
	// The FAT must be large enough to describe every cluster.
	need := map[string]int64{"fat12": (g.clusters + 2) * 3 / 2, "fat16": (g.clusters + 2) * 2, "fat32": (g.clusters + 2) * 4}[g.kind]
	if g.fatSz*g.bps < need {
		return nil, errors.New("fat: FAT too small for cluster count")
	}
	return g, nil
}

func looksFAT(bs []byte) bool {
	if bs[510] != 0x55 || bs[511] != 0xAA {
		return false
	}
	if bs[0] != 0xEB && bs[0] != 0xE9 {
		return false
	}
	_, err := fatGeometry(bs)
	return err == nil
}

func fat(r io.ReaderAt, size int64, bs []byte, withUsed bool) (*Result, error) {
	g, err := fatGeometry(bs)
	if err != nil {
		return nil, err
	}
	res := &Result{FS: g.kind, FSSize: g.totSec * g.bps}
	if g.kind == "fat32" {
		res.UUID = serial32(le32(bs[67:]))
		res.Label = cstr(bs[71:82])
	} else {
		res.UUID = serial32(le32(bs[39:]))
		res.Label = cstr(bs[43:54])
	}
	if !withUsed {
		return res, nil
	}
	tbl, err := readAt(r, g.reserved*g.bps, int(g.fatSz*g.bps))
	if err != nil {
		return nil, fmt.Errorf("fat: reading FAT: %w", err)
	}
	entry := func(c int64) uint32 {
		switch g.kind {
		case "fat12":
			o := c * 3 / 2
			v := uint32(tbl[o]) | uint32(tbl[o+1])<<8
			if c&1 == 1 {
				return v >> 4
			}
			return v & 0xFFF
		case "fat16":
			return uint32(le16(tbl[c*2:]))
		default:
			return le32(tbl[c*4:]) & 0x0FFFFFFF
		}
	}
	cbytes := g.spc * g.bps
	dataOff := g.firstData * g.bps
	used := []extent.Extent{{Off: 0, Len: dataOff}} // boot, FATs, root dir
	runStart := int64(-1)
	for c := int64(2); c < g.clusters+2; c++ {
		if entry(c) != 0 {
			if runStart < 0 {
				runStart = c
			}
			continue
		}
		if runStart >= 0 {
			used = append(used, extent.Extent{Off: dataOff + (runStart-2)*cbytes, Len: (c - runStart) * cbytes})
			runStart = -1
		}
	}
	if runStart >= 0 {
		used = append(used, extent.Extent{Off: dataOff + (runStart-2)*cbytes, Len: (g.clusters + 2 - runStart) * cbytes})
	}
	// Sectors after the last full cluster, and the partition tail.
	used = append(used, extent.Extent{Off: dataOff + g.clusters*cbytes, Len: g.totSec*g.bps - (dataOff + g.clusters*cbytes)})
	used = append(used, tailExtent(g.totSec*g.bps, size)...)
	res.Used = extent.Normalize(extent.Clamp(used, 0, size), 0)
	return res, nil
}
