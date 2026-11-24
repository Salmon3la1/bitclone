package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"bitclone/internal/engine"
)

const (
	ioChunk = 4 << 20
	align   = 4096 // multiple of every common logical sector size
)

type readerWriterAt interface {
	io.ReaderAt
	io.WriterAt
}

func padded(n int64) int64 { return (n + align - 1) / align * align }

// writeImage copies src (srcSize bytes) to the start of dst (a raw disk of
// dstSize bytes), then reads it back and compares. All I/O is aligned to 4
// KiB because raw Windows disk handles reject unaligned transfers. The first
// chunk (with the partition table) is written last, so an interrupted write
// never leaves a stick that looks bootable but is half-written. The last MiB
// of the disk is zeroed to remove a stale GPT backup header.
func writeImage(ctx context.Context, src io.ReaderAt, srcSize int64, dst readerWriterAt, dstSize int64, sync func() error, p *engine.Progress) error {
	total := padded(srcSize)
	if total > dstSize {
		return fmt.Errorf("the USB stick is too small: needs %s, has %s", engine.HumanBytes(total), engine.HumanBytes(dstSize))
	}
	readChunk := func(off int64, buf []byte) ([]byte, error) {
		n := int64(len(buf))
		if off+n > total {
			n = total - off
		}
		buf = buf[:n]
		m, err := src.ReadAt(buf, off)
		if int64(m) < n && off+int64(m) < srcSize {
			return nil, fmt.Errorf("reading image at %d: %v", off, err)
		}
		for i := m; i < len(buf); i++ {
			buf[i] = 0 // padding past the end of the image
		}
		return buf, nil
	}

	if tail := (dstSize - 1<<20) / align * align; tail >= total {
		if _, err := dst.WriteAt(make([]byte, 1<<20), tail); err != nil {
			return fmt.Errorf("clearing end of stick: %w", err)
		}
	}

	p.Start("Writing", total)
	buf := make([]byte, ioChunk)
	write := func(off int64) error {
		b, err := readChunk(off, buf)
		if err != nil {
			return err
		}
		if _, err := dst.WriteAt(b, off); err != nil {
			return fmt.Errorf("writing to USB stick at %s: %w", engine.HumanBytes(off), err)
		}
		p.Add(int64(len(b)))
		return nil
	}
	for off := int64(ioChunk); off < total; off += ioChunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := write(off); err != nil {
			return err
		}
	}
	if err := write(0); err != nil {
		return err
	}
	if sync != nil {
		if err := sync(); err != nil {
			return fmt.Errorf("flushing USB stick: %w", err)
		}
	}

	p.Start("Verifying", total)
	back := make([]byte, ioChunk)
	for off := int64(0); off < total; off += ioChunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		want, err := readChunk(off, buf)
		if err != nil {
			return err
		}
		got := back[:len(want)]
		if _, err := dst.ReadAt(got, off); err != nil {
			return fmt.Errorf("reading back USB stick at %s: %w", engine.HumanBytes(off), err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("verification failed at %s: the stick returned different data (it may be faulty or fake-capacity)", engine.HumanBytes(off))
		}
		p.Add(int64(len(want)))
	}
	p.Logf("written and verified %s", engine.HumanBytes(srcSize))
	return nil
}
