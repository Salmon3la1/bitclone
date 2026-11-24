package main

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bitclone/internal/engine"
)

func fakeStick(t *testing.T, size int) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stick.img"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	f.Write(bytes.Repeat([]byte{0xEE}, size))
	return f
}

func TestWriteImage(t *testing.T) {
	src := make([]byte, 10<<20+123) // not a multiple of the sector size
	rand.New(rand.NewSource(1)).Read(src)
	const stick = 64 << 20
	f := fakeStick(t, stick)
	if err := writeImage(context.Background(), bytes.NewReader(src), int64(len(src)), f, stick, f.Sync, &engine.Progress{}); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, stick)
	f.ReadAt(got, 0)
	n := padded(int64(len(src)))
	if !bytes.Equal(got[:len(src)], src) {
		t.Fatal("image not written exactly")
	}
	if !bytes.Equal(got[len(src):n], make([]byte, n-int64(len(src)))) {
		t.Fatal("padding not zeroed")
	}
	if got[n] != 0xEE || got[stick-(1<<20)-1] != 0xEE {
		t.Fatal("wrote outside image area")
	}
	if !bytes.Equal(got[stick-(1<<20):], make([]byte, 1<<20)) {
		t.Fatal("end of stick not cleared")
	}
}

func TestWriteImageTooSmall(t *testing.T) {
	f := fakeStick(t, 1<<20)
	err := writeImage(context.Background(), bytes.NewReader(make([]byte, 2<<20)), 2<<20, f, 1<<20, nil, &engine.Progress{})
	if err == nil || !strings.Contains(err.Error(), "too small") {
		t.Fatalf("got %v", err)
	}
}

// lyingStick drops writes past a limit, like a fake-capacity stick.
type lyingStick struct {
	*os.File
	limit int64
}

func (l lyingStick) WriteAt(b []byte, off int64) (int, error) {
	if off >= l.limit {
		return len(b), nil
	}
	return l.File.WriteAt(b, off)
}

func TestWriteImageDetectsBadStick(t *testing.T) {
	src := make([]byte, 12<<20)
	rand.New(rand.NewSource(2)).Read(src)
	f := fakeStick(t, 32<<20)
	err := writeImage(context.Background(), bytes.NewReader(src), int64(len(src)), lyingStick{f, 8 << 20}, 32<<20, nil, &engine.Progress{})
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("bad stick not detected: %v", err)
	}
}
