package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"imagen/internal/image"
	"imagen/internal/sparse"
)

// Export rebuilds a plain disk image (.img: every sector in order, as dd
// would make it) from a .bci image, for tools that read raw images such as
// data-recovery programs or virtual machines. Areas the image did not
// capture (free space in "used data only" mode) and all-zero chunks are
// left unwritten in a sparse file, so they read as zeros and, on NTFS or
// Linux filesystems, take no disk space.
//
// It writes to out+".part" and renames it to out once every checksum has
// matched, so a stopped or failed export never leaves a file that looks
// complete.
func Export(ctx context.Context, imagePath, out string, p *Progress) (*image.Meta, error) {
	meta, err := ReadMeta(imagePath)
	if err != nil {
		return nil, err
	}
	tmp := out + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return meta, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	sparseOK := sparse.Set(f) == nil
	if err := f.Truncate(meta.Source.Size); err != nil {
		if meta.Source.Size > 4<<30-1 {
			return meta, fmt.Errorf("could not create a %s file there (FAT32 drives hold files up to 4 GB; choose an NTFS or exFAT drive): %w",
				HumanBytes(meta.Source.Size), err)
		}
		return meta, err
	}
	if !sparseOK {
		p.Logf("note: this drive does not support sparse files, so skipped areas are written out as zeros by Windows")
	}
	if meta.Mode == "used" {
		p.Logf("note: this is a \"used data only\" image; free space was not captured and reads as zeros")
	}
	p.Start(fmt.Sprintf("Converting %s", imagePath), meta.DataBytes)
	zero := make([]byte, image.MaxChunk)
	_, err = streamChunks(ctx, p, imagePath, nil, func(off int64, raw []byte) error {
		if !bytes.Equal(raw, zero[:len(raw)]) {
			if _, err := f.WriteAt(raw, off); err != nil {
				return fmt.Errorf("writing %s at byte %d: %w", out, off, err)
			}
		}
		p.Add(int64(len(raw)))
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return meta, err
		}
		return meta, fmt.Errorf("conversion stopped: %w", err)
	}
	if err := f.Sync(); err != nil {
		return meta, err
	}
	if err := f.Close(); err != nil {
		return meta, err
	}
	if err := os.Rename(tmp, out); err != nil {
		return meta, err
	}
	ok = true
	p.Logf("saved %s (%s, every checksum matched)", out, HumanBytes(meta.Source.Size))
	return meta, nil
}
