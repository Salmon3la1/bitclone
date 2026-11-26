package image

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// flaky is a file whose reads fail once after failAt bytes, as when a USB
// drive resets; reopening it works.
type flaky struct {
	*bytes.Reader
	failAt int64
	fail   *bool
}

func (f *flaky) Read(p []byte) (int, error) {
	pos, _ := f.Seek(0, io.SeekCurrent)
	if *f.fail && pos >= f.failAt {
		*f.fail = false
		return 0, errors.New("A device which does not exist was specified.")
	}
	if *f.fail && pos+int64(len(p)) > f.failAt {
		p = p[:f.failAt-pos]
	}
	return f.Reader.Read(p)
}
func (f *flaky) Close() error { return nil }

func TestSplitReaderRetriesAfterDriveDrop(t *testing.T) {
	RetryEvery = time.Millisecond
	data := bytes.Repeat([]byte("0123456789"), 100000)
	fail := true
	opens := 0
	sr, err := openSplit("x.bci", func(name string) (io.ReadSeekCloser, error) {
		if name != "x.bci" {
			return nil, errors.New("no such file")
		}
		opens++
		return &flaky{Reader: bytes.NewReader(data), failAt: 123457, fail: &fail}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	retries := 0
	sr.OnRetry = func(error) { retries++ }
	got, err := io.ReadAll(sr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) || retries != 1 || opens != 2 {
		t.Fatalf("got %d bytes, %d retries, %d opens", len(got), retries, opens)
	}
}

func TestSplitReaderGivesUpWhenDriveIsGone(t *testing.T) {
	RetryFor, RetryEvery = 20*time.Millisecond, time.Millisecond
	fail := true
	first := true
	sr, _ := openSplit("x.bci", func(string) (io.ReadSeekCloser, error) {
		if !first {
			return nil, errors.New("The system cannot find the path specified.")
		}
		first = false
		return &flaky{Reader: bytes.NewReader(make([]byte, 1000)), failAt: 10, fail: &fail}, nil
	})
	_, err := io.ReadAll(sr)
	if !errors.Is(err, ErrDriveLost) {
		t.Fatalf("want ErrDriveLost, got %v", err)
	}
	if errors.Is(readErr(err), ErrCorrupt) {
		t.Fatal("a lost drive was reported as a corrupt image")
	}
	if !errors.Is(readErr(io.ErrUnexpectedEOF), ErrCorrupt) {
		t.Fatal("truncation no longer reported as corrupt")
	}
}
