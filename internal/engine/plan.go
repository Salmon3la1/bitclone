// Package engine implements backup, restore and verify.
package engine

import (
	"fmt"
	"io"

	"bitclone/internal/extent"
	"bitclone/internal/fs"
	"bitclone/internal/image"
	"bitclone/internal/part"
)

const (
	ModeUsed = "used" // copy only allocated blocks of known filesystems
	ModeFull = "full" // copy every sector

	// Unpartitioned gaps up to this size are copied in full in "used" mode
	// (they can hold boot loaders, e.g. GRUB after the MBR).
	smallGap = 64 << 20
	// For larger gaps only the start is kept, plus the last edgeKeep bytes
	// of the disk (GPT backup, RAID/vendor metadata at the end).
	edgeKeep = 16 << 20
	// Neighbouring regions closer than this are merged into one read.
	mergeGap = 256 << 10
)

// Plan says exactly which byte ranges of a disk will be imaged.
type Plan struct {
	Mode       string           `json:"mode"`
	DiskSize   int64            `json:"diskSize"`
	SectorSize int              `json:"sectorSize"`
	Table      *part.Table      `json:"table,omitempty"`
	Parts      []image.PartInfo `json:"parts"`
	Extents    []extent.Extent  `json:"-"`
	DataBytes  int64            `json:"dataBytes"`
	Warnings   []string         `json:"warnings,omitempty"`
}

// MakePlan analyses a disk.
func MakePlan(dev io.ReaderAt, size int64, sectorSize int, mode string) (*Plan, error) {
	if mode != ModeUsed && mode != ModeFull {
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	p := &Plan{Mode: mode, DiskSize: size, SectorSize: sectorSize}
	t, err := part.Read(dev, size, sectorSize)
	if err != nil && err != part.ErrNoTable {
		p.Warnings = append(p.Warnings, "partition table unreadable: "+err.Error())
	}
	if t != nil {
		p.Table = t
		if t.SectorSize != sectorSize && sectorSize != 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("partition table uses %d-byte sectors but device reports %d", t.SectorSize, sectorSize))
		}
	}

	var parts []part.Partition
	if t != nil {
		parts = t.DataPartitions()
	} else {
		// No table: maybe a filesystem directly on the disk ("superfloppy").
		parts = []part.Partition{{Index: 0, Start: 0, Size: size, TypeName: "whole disk"}}
	}

	var used []extent.Extent
	for _, pt := range parts {
		pi := image.PartInfo{Partition: pt, Strategy: "full"}
		if pt.Start >= size {
			pi.Note = "lies beyond end of disk"
			p.Warnings = append(p.Warnings, fmt.Sprintf("partition %d lies beyond the end of the disk", pt.Index))
			p.Parts = append(p.Parts, pi)
			continue
		}
		psize := pt.Size
		if pt.Start+psize > size {
			psize = size - pt.Start
			p.Warnings = append(p.Warnings, fmt.Sprintf("partition %d extends past end of disk; truncated", pt.Index))
		}
		sec := io.NewSectionReader(dev, pt.Start, psize)
		var res *fs.Result
		if mode == ModeUsed {
			res = fs.Detect(sec, psize)
		} else {
			res = fs.Identify(sec, psize)
		}
		if res != nil {
			pi.FS, pi.Label, pi.UUID = res.FS, res.Label, res.UUID
			if res.Err != "" {
				pi.Note = res.Err + " — copying every sector"
				p.Warnings = append(p.Warnings, fmt.Sprintf("partition %d (%s): %s; it will be copied in full", pt.Index, res.FS, res.Err))
			}
		}
		if mode == ModeUsed && res != nil && res.Used != nil {
			pi.Strategy = "used"
			ext := extent.Shift(res.Used, pt.Start)
			ext = extent.Clamp(ext, pt.Start, pt.Start+psize)
			pi.UsedBytes = extent.Total(extent.Normalize(ext, 0))
			used = append(used, ext...)
		} else {
			pi.UsedBytes = psize
			used = append(used, extent.Extent{Off: pt.Start, Len: psize})
			if mode == ModeUsed && pi.Note == "" {
				if pi.FS != "" {
					pi.Note = pi.FS + " has no used-block reader — copying every sector"
				} else {
					pi.Note = "unknown content — copying every sector"
				}
			}
		}
		p.Parts = append(p.Parts, pi)
	}

	if mode == ModeFull {
		p.Extents = []extent.Extent{{Off: 0, Len: size}}
	} else {
		var ranges []extent.Extent
		for _, pt := range parts {
			ranges = append(ranges, extent.Extent{Off: pt.Start, Len: pt.Size})
		}
		for _, g := range extent.Complement(ranges, 0, size) {
			if g.Len <= smallGap {
				used = append(used, g)
			} else {
				used = append(used, extent.Extent{Off: g.Off, Len: edgeKeep})
			}
		}
		used = append(used, extent.Extent{Off: size - edgeKeep, Len: edgeKeep})
		p.Extents = extent.Normalize(extent.Clamp(used, 0, size), mergeGap)
	}
	p.DataBytes = extent.Total(p.Extents)
	return p, nil
}
