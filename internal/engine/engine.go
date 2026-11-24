package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"imagen/internal/disk"
	"imagen/internal/image"
	"imagen/internal/part"
)

// ToolVersion is stamped into every image.
var ToolVersion = "0.1.0"

const chunkSize = 4 << 20

// BackupOptions configures a backup.
type BackupOptions struct {
	Source    string // disk device or image file
	Dest      string // .bci path
	Mode      string // ModeUsed or ModeFull
	Level     int    // deflate level 0 (store) .. 9; default 1
	SplitSize int64  // bytes per image file; 0 = no split
	Overwrite bool
	Verify    bool // verify the image after writing
	Model     string
	Serial    string
}

// RestoreOptions configures a restore.
type RestoreOptions struct {
	Image       string
	Target      string
	FixGPT      bool // move GPT backup header to end of a larger target
	ZeroFill    bool // write zeros to regions not stored in the image
	VerifyFirst bool // verify the whole image before touching the target
	Force       bool // allow sector-size mismatch
}

func workers() int {
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	return n
}

// Analyze opens a disk and returns its plan.
func Analyze(path, mode string) (*Plan, error) {
	dev, err := disk.Open(path)
	if err != nil {
		return nil, err
	}
	defer dev.Close()
	return MakePlan(dev, dev.Size, disk.SectorSize(path), mode)
}

type encJob struct {
	off int64
	raw []byte
	err error
	res chan encResult
}

type encResult struct {
	codec  byte
	stored []byte
	crc    uint32
}

// Backup images a disk.
func Backup(ctx context.Context, o BackupOptions, p *Progress) (err error) {
	if o.Mode == "" {
		o.Mode = ModeUsed
	}
	if disk.SameDisk(o.Source, filepath.Dir(o.Dest)) {
		return errors.New("the destination folder is on the disk being backed up; choose another drive")
	}
	src, err := disk.Open(o.Source)
	if err != nil {
		return err
	}
	defer src.Close()
	ss := disk.SectorSize(o.Source)

	p.Start("Analysing disk", 0)
	plan, err := MakePlan(src, src.Size, ss, o.Mode)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		p.Logf("warning: %s", w)
	}
	for _, pt := range plan.Parts {
		p.Logf("partition %d: %s %s %s — %s, %s to copy", pt.Index, pt.TypeName, pt.FS, pt.Label, pt.Strategy, HumanBytes(pt.UsedBytes))
	}
	if plan.Table != nil {
		p.Logf("partition table: %s, disk ID %s", plan.Table.Scheme, plan.Table.DiskID)
	}

	meta := &image.Meta{
		Tool: "ImaGen", ToolVersion: ToolVersion, Created: time.Now().UTC(),
		Mode: o.Mode, ChunkSize: chunkSize,
		Source:    image.SourceInfo{Path: o.Source, Model: o.Model, Serial: o.Serial, Size: src.Size, SectorSize: ss},
		Table:     plan.Table,
		Parts:     plan.Parts,
		DataBytes: plan.DataBytes,
		Regions:   len(plan.Extents),
		Warnings:  plan.Warnings,
	}
	if len(plan.Extents) <= 64 {
		meta.Extents = plan.Extents
	}

	if err := os.MkdirAll(filepath.Dir(o.Dest), 0o755); err != nil {
		return err
	}
	sw, err := image.CreateSplit(o.Dest, o.SplitSize, o.Overwrite)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		sw.Close()
		if !ok {
			for _, f := range image.Parts(o.Dest) {
				os.Remove(f)
			}
		}
	}()
	iw, err := image.NewWriter(sw, meta)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nw := workers()
	work := make(chan *encJob, nw)
	order := make(chan *encJob, nw*2)

	// Reader: sequential reads in disk order (best for spinning disks).
	go func() {
		defer close(work)
		defer close(order)
		for _, e := range plan.Extents {
			for off := e.Off; off < e.End(); off += chunkSize {
				n := int64(chunkSize)
				if off+n > e.End() {
					n = e.End() - off
				}
				j := &encJob{off: off, raw: make([]byte, n), res: make(chan encResult, 1)}
				if m, rerr := src.ReadAt(j.raw, off); m < len(j.raw) {
					j.err = fmt.Errorf("read error on source at byte %d: %v", off+int64(m), rerr)
				}
				select {
				case order <- j:
				case <-ctx.Done():
					return
				}
				if j.err != nil {
					return
				}
				select {
				case work <- j:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	for i := 0; i < nw; i++ {
		go func() {
			for j := range work {
				c, s, crc := image.Encode(j.raw, o.Level)
				j.res <- encResult{c, s, crc}
			}
		}()
	}

	p.Start(fmt.Sprintf("Backing up %s (%s mode) to %s", o.Source, o.Mode, o.Dest), plan.DataBytes)
	for j := range order {
		if j.err != nil {
			return j.err
		}
		var r encResult
		select {
		case r = <-j.res:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := iw.WriteChunk(j.off, len(j.raw), r.codec, r.stored, r.crc); err != nil {
			return fmt.Errorf("writing image: %w", err)
		}
		p.Add(int64(len(j.raw)))
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := iw.Close(); err != nil {
		return fmt.Errorf("writing image: %w", err)
	}
	if err := sw.Close(); err != nil {
		return fmt.Errorf("writing image: %w", err)
	}
	ok = true
	total := image.TotalSize(o.Dest)
	p.Logf("image written: %s in %d file(s), %s read from disk, %s stored",
		o.Dest, len(image.Parts(o.Dest)), HumanBytes(plan.DataBytes), HumanBytes(total))
	if o.Verify {
		if _, err := Verify(ctx, o.Dest, p); err != nil {
			return err
		}
	}
	return nil
}

type decJob struct {
	c   *image.Chunk
	res chan decResult
}

type decResult struct {
	raw []byte
	err error
}

// streamChunks reads an image and decodes chunks in parallel, calling fn in
// disk order. It returns after the end record and image checksum are verified.
func streamChunks(ctx context.Context, path string, onMeta func(*image.Meta) error, fn func(off int64, raw []byte) error) (*image.Meta, error) {
	sr, err := image.OpenSplit(path)
	if err != nil {
		return nil, err
	}
	defer sr.Close()
	ir, err := image.NewReader(sr)
	if err != nil {
		return nil, err
	}
	if onMeta != nil {
		if err := onMeta(ir.Meta); err != nil {
			return ir.Meta, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nw := workers()
	work := make(chan *decJob, nw)
	order := make(chan *decJob, nw*2)
	readErr := make(chan error, 1)
	go func() {
		defer close(work)
		defer close(order)
		for {
			c, err := ir.Next()
			if err == io.EOF {
				readErr <- nil
				return
			}
			if err != nil {
				readErr <- err
				return
			}
			j := &decJob{c: c, res: make(chan decResult, 1)}
			select {
			case order <- j:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
			select {
			case work <- j:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
		}
	}()
	for i := 0; i < nw; i++ {
		go func() {
			for j := range work {
				raw, err := image.Decode(j.c)
				j.res <- decResult{raw, err}
			}
		}()
	}
	for j := range order {
		var r decResult
		select {
		case r = <-j.res:
		case <-ctx.Done():
			return ir.Meta, ctx.Err()
		}
		if r.err != nil {
			return ir.Meta, r.err
		}
		if err := fn(j.c.Off, r.raw); err != nil {
			return ir.Meta, err
		}
		if ctx.Err() != nil {
			return ir.Meta, ctx.Err()
		}
	}
	return ir.Meta, <-readErr
}

// ReadMeta returns an image's metadata without reading its data.
func ReadMeta(path string) (*image.Meta, error) {
	sr, err := image.OpenSplit(path)
	if err != nil {
		return nil, err
	}
	defer sr.Close()
	ir, err := image.NewReader(sr)
	if err != nil {
		return nil, err
	}
	return ir.Meta, nil
}

// Verify checks every chunk checksum and the whole-image SHA-256.
func Verify(ctx context.Context, path string, p *Progress) (*image.Meta, error) {
	meta, err := streamChunks(ctx, path, func(m *image.Meta) error {
		p.Start("Verifying "+path, m.DataBytes)
		return nil
	}, func(off int64, raw []byte) error {
		p.Add(int64(len(raw)))
		return nil
	})
	if err != nil {
		return meta, fmt.Errorf("verification failed: %w", err)
	}
	p.Logf("image OK: all checksums match")
	return meta, nil
}

// Restore writes an image to a target disk.
func Restore(ctx context.Context, o RestoreOptions, p *Progress) error {
	meta, err := ReadMeta(o.Image)
	if err != nil {
		return err
	}
	if disk.SameDisk(o.Image, o.Target) {
		return errors.New("the image file is stored on the target disk")
	}
	if err := disk.CheckWritable(o.Target); err != nil {
		return err
	}
	tgt, err := disk.OpenWrite(o.Target)
	if err != nil {
		return err
	}
	defer tgt.Close()
	if tgt.Size < meta.Source.Size {
		return fmt.Errorf("target is smaller than the source disk (%s < %s); restoring to a smaller disk is not supported yet",
			HumanBytes(tgt.Size), HumanBytes(meta.Source.Size))
	}
	tss := disk.SectorSize(o.Target)
	if disk.IsBlockDevice(o.Target) && tss != meta.Source.SectorSize && !o.Force {
		return fmt.Errorf("target uses %d-byte sectors but the source used %d; the partition table would not match", tss, meta.Source.SectorSize)
	}
	if o.VerifyFirst {
		if _, err := Verify(ctx, o.Image, p); err != nil {
			return err
		}
	}

	zero := make([]byte, chunkSize)
	zeroRange := func(from, to int64) error {
		for from < to {
			n := int64(len(zero))
			if to-from < n {
				n = to - from
			}
			if _, err := tgt.WriteAt(zero[:n], from); err != nil {
				return err
			}
			p.Add(n)
			from += n
		}
		return nil
	}
	total := meta.DataBytes
	if o.ZeroFill {
		total = meta.Source.Size
	}
	p.Start(fmt.Sprintf("Restoring %s to %s", o.Image, o.Target), total)
	var pos int64
	_, err = streamChunks(ctx, o.Image, nil, func(off int64, raw []byte) error {
		if o.ZeroFill && off > pos {
			if err := zeroRange(pos, off); err != nil {
				return fmt.Errorf("writing target: %w", err)
			}
		}
		if _, err := tgt.WriteAt(raw, off); err != nil {
			return fmt.Errorf("writing target at byte %d: %w", off, err)
		}
		pos = off + int64(len(raw))
		p.Add(int64(len(raw)))
		return nil
	})
	if err != nil {
		tgt.Sync()
		return fmt.Errorf("restore stopped, target disk is incomplete: %w", err)
	}
	if o.ZeroFill && pos < meta.Source.Size {
		if err := zeroRange(pos, meta.Source.Size); err != nil {
			return err
		}
	}
	if meta.Table != nil && meta.Table.Scheme == "gpt" && tgt.Size > meta.Source.Size {
		if o.FixGPT {
			if err := part.RelocateGPTBackup(tgt, tgt.Size, meta.Table.SectorSize); err != nil {
				p.Logf("warning: could not move GPT backup to end of disk: %v", err)
			} else {
				p.Logf("GPT backup header moved to the end of the larger target (disk GUID unchanged)")
			}
		} else {
			p.Logf("note: target is larger; GPT backup header left at original position")
		}
	}
	if err := tgt.Sync(); err != nil {
		return fmt.Errorf("flushing target: %w", err)
	}
	disk.Reread(o.Target)
	if meta.Table != nil {
		p.Logf("restore complete; disk ID %s preserved", meta.Table.DiskID)
	} else {
		p.Logf("restore complete")
	}
	return nil
}
