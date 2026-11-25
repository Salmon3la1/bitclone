package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestExportFullModeBitExact(t *testing.T) {
	f := makeDisk(t)
	_, img := roundTrip(t, f, ModeFull, int64(len(f.disk)), 7*MiB, false)
	out := filepath.Join(t.TempDir(), "disk.img")
	if _, err := Export(context.Background(), img, out, &Progress{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(b) != sha256.Sum256(f.disk) {
		t.Fatal("exported .img is not bit-identical to the source disk")
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Fatal("temporary .part file left behind")
	}
}

func TestExportUsedModeZerosFreeSpace(t *testing.T) {
	f := makeDisk(t)
	_, img := roundTrip(t, f, ModeUsed, int64(len(f.disk)), 0, false)
	out := filepath.Join(t.TempDir(), "disk.img")
	if _, err := Export(context.Background(), img, out, &Progress{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if int64(len(b)) != int64(len(f.disk)) {
		t.Fatalf("size %d, want %d", len(b), len(f.disk))
	}
	for _, e := range f.must {
		if !bytes.Equal(b[e.Off:e.End()], f.disk[e.Off:e.End()]) {
			t.Fatalf("used region %v differs", e)
		}
	}
}

func TestExportCorruptLeavesNoFile(t *testing.T) {
	f := makeDisk(t)
	_, img := roundTrip(t, f, ModeFull, int64(len(f.disk)), 0, false)
	b, _ := os.ReadFile(img)
	b[len(b)/2] ^= 0x40
	writeFile(t, img, b)
	out := filepath.Join(t.TempDir(), "disk.img")
	if _, err := Export(context.Background(), img, out, &Progress{}); err == nil {
		t.Fatal("corrupt image exported without error")
	}
	for _, p := range []string{out, out + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", p)
		}
	}
}
