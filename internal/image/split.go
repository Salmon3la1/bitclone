package image

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
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

// ErrDriveLost is returned when the drive holding the image stopped
// answering (unplugged, or a USB disconnect) and did not come back.
var ErrDriveLost = errors.New("the drive holding the image stopped responding (was it disconnected?). The image itself is probably fine: reconnect the drive and try again")

// How long SplitReader keeps trying to reopen an image file after a read
// error, e.g. while a USB drive resets and Windows mounts it again.
var (
	RetryFor   = 60 * time.Second
	RetryEvery = 3 * time.Second
)

// SplitReader reads the volumes of an image in sequence. If a read fails
// (not end of file), it reopens the file at the same position and tries
// again for up to RetryFor, so a drive that drops out for a moment does not
// end a restore or conversion. OnRetry, if set, is told about each attempt.
type SplitReader struct {
	base    string
	idx     int
	cur     io.ReadSeekCloser
	pos     int64 // position in the current volume
	open    func(string) (io.ReadSeekCloser, error)
	OnRetry func(err error)
	closed  atomic.Bool
	mu      sync.Mutex // guards cur between reopen and Close
}

func openFile(p string) (io.ReadSeekCloser, error) { return os.Open(p) }

// OpenSplit opens an image (first volume path).
func OpenSplit(base string) (*SplitReader, error) {
	return openSplit(base, openFile)
}

func openSplit(base string, open func(string) (io.ReadSeekCloser, error)) (*SplitReader, error) {
	f, err := open(base)
	if err != nil {
		return nil, err
	}
	return &SplitReader{base: base, cur: f, open: open}, nil
}

func (s *SplitReader) Read(p []byte) (int, error) {
	for {
		if s.cur == nil {
			return 0, io.EOF
		}
		n, err := s.cur.Read(p)
		s.pos += int64(n)
		if n > 0 {
			return n, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			if rerr := s.reopen(err); rerr != nil {
				return 0, rerr
			}
			continue
		}
		s.cur.Close()
		s.idx++
		f, err := s.open(PartName(s.base, s.idx))
		if err != nil {
			s.cur = nil
			return 0, io.EOF
		}
		s.cur, s.pos = f, 0
	}
}

// reopen closes the failed file and opens it again at the same position.
func (s *SplitReader) reopen(cause error) error {
	s.cur.Close()
	if s.closed.Load() || errors.Is(cause, os.ErrClosed) {
		s.cur = nil
		return cause
	}
	name := PartName(s.base, s.idx)
	deadline := time.Now().Add(RetryFor)
	last := cause
	for {
		if s.OnRetry != nil {
			s.OnRetry(last)
		}
		if f, err := s.open(name); err == nil {
			if _, err := f.Seek(s.pos, io.SeekStart); err == nil {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.closed.Load() {
					f.Close()
					s.cur = nil
					return os.ErrClosed
				}
				s.cur = f
				return nil
			}
			f.Close()
		} else {
			last = err
		}
		if s.closed.Load() {
			s.cur = nil
			return os.ErrClosed
		}
		if !time.Now().Before(deadline) {
			s.cur = nil
			return fmt.Errorf("%w (%v)", ErrDriveLost, cause)
		}
		time.Sleep(RetryEvery)
	}
}

// Close closes the current volume.
func (s *SplitReader) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed.Store(true)
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
