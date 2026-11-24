package image

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// PartName returns the file name of volume idx of a split image:
// "disk.bci", "disk.bci.001", "disk.bci.002", ...
func PartName(base string, idx int) string {
	if idx == 0 {
		return base
	}
	return fmt.Sprintf("%s.%03d", base, idx)
}

// Parts lists the existing files of an image.
func Parts(base string) []string {
	var out []string
	for i := 0; ; i++ {
		p := PartName(base, i)
		if _, err := os.Stat(p); err != nil {
			return out
		}
		out = append(out, p)
	}
}

// SplitWriter writes a stream across files of at most limit bytes each
// (limit <= 0 means a single file). Needed for FAT32 destinations.
type SplitWriter struct {
	base  string
	limit int64
	idx   int
	cur   *os.File
	n     int64
}

// CreateSplit creates a new image. If overwrite is false and the image
// exists, it fails. Stale extra volumes of an overwritten image are removed.
func CreateSplit(base string, limit int64, overwrite bool) (*SplitWriter, error) {
	if _, err := os.Stat(base); err == nil && !overwrite {
		return nil, fmt.Errorf("%s already exists", base)
	}
	for _, p := range Parts(base) {
		if err := os.Remove(p); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(base, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &SplitWriter{base: base, limit: limit, cur: f}, nil
}

func (s *SplitWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if s.limit > 0 && s.n >= s.limit {
			if err := s.rotate(); err != nil {
				return total, err
			}
		}
		chunk := p
		if s.limit > 0 && int64(len(chunk)) > s.limit-s.n {
			chunk = chunk[:s.limit-s.n]
		}
		n, err := s.cur.Write(chunk)
		total += n
		s.n += int64(n)
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

func (s *SplitWriter) rotate() error {
	if err := s.cur.Sync(); err != nil {
		return err
	}
	if err := s.cur.Close(); err != nil {
		return err
	}
	s.idx++
	f, err := os.OpenFile(PartName(s.base, s.idx), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	s.cur, s.n = f, 0
	return nil
}

// Volumes returns how many files have been written so far.
func (s *SplitWriter) Volumes() int { return s.idx + 1 }

// Close syncs and closes the current file.
func (s *SplitWriter) Close() error {
	if s.cur == nil {
		return nil
	}
	err := s.cur.Sync()
	if cerr := s.cur.Close(); err == nil {
		err = cerr
	}
	s.cur = nil
	return err
}

// SplitReader reads the volumes of an image in sequence.
type SplitReader struct {
	base string
	idx  int
	cur  *os.File
}

// OpenSplit opens an image (first volume path).
func OpenSplit(base string) (*SplitReader, error) {
	f, err := os.Open(base)
	if err != nil {
		return nil, err
	}
	return &SplitReader{base: base, cur: f}, nil
}

func (s *SplitReader) Read(p []byte) (int, error) {
	for {
		if s.cur == nil {
			return 0, io.EOF
		}
		n, err := s.cur.Read(p)
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			return n, err
		}
		s.cur.Close()
		s.idx++
		f, err := os.Open(PartName(s.base, s.idx))
		if err != nil {
			s.cur = nil
			return 0, io.EOF
		}
		s.cur = f
	}
}

// Close closes the current volume.
func (s *SplitReader) Close() error {
	if s.cur != nil {
		return s.cur.Close()
	}
	return nil
}

// TotalSize returns the combined size of all volumes.
func TotalSize(base string) int64 {
	var t int64
	for _, p := range Parts(base) {
		if st, err := os.Stat(p); err == nil {
			t += st.Size()
		}
	}
	return t
}
