package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bitclone/internal/extent"
	"bitclone/internal/image"
	"bitclone/internal/part"
	"bitclone/internal/testdisk"
)

const MiB = 1 << 20

type fixture struct {
	disk []byte
	// byte ranges (absolute) that must survive a used-mode round trip
	must []extent.Extent
	// byte ranges that must NOT be copied in used mode (free space)
	free []extent.Extent
}

// makeDisk builds a 400 MiB GPT disk: FAT16 (EFI-like), NTFS, ext4, and a
// partition of unknown content, with random garbage in all free space.
func makeDisk(t *testing.T) *fixture {
	disk := make([]byte, 400*MiB)
	testdisk.Fill(disk, 42) // garbage everywhere, including gaps
	fatOff, ntfsOff, extOff, rawOff := int64(1*MiB), int64(40*MiB), int64(110*MiB), int64(380*MiB)
	parts := []testdisk.Part{
		{Start: fatOff, Size: 32 * MiB, Type: testdisk.BasicData, GUID: [16]byte{1}, Name: "EFI"},
		{Start: ntfsOff, Size: 64 * MiB, Type: testdisk.BasicData, GUID: [16]byte{2}, Name: "Windows"},
		{Start: extOff, Size: 256 * MiB, Type: testdisk.BasicData, GUID: [16]byte{3}, Name: "Linux"},
		{Start: rawOff, Size: 8 * MiB, Type: testdisk.BasicData, GUID: [16]byte{4}, Name: "Raw"},
	}
	f := &fixture{disk: disk}
	add := func(base int64, list []extent.Extent) { f.must = append(f.must, extent.Shift(list, base)...) }
	free := func(base, size int64, used []extent.Extent) {
		f.free = append(f.free, extent.Shift(extent.Complement(used, 0, size), base)...)
	}
	u := testdisk.FAT16(disk[fatOff:fatOff+32*MiB], []int{2, 3, 4, 100, 101, 5000}, 1)
	add(fatOff, u)
	free(fatOff, 32*MiB, u)
	u = testdisk.NTFS(disk[ntfsOff:ntfsOff+64*MiB], []int64{30, 31, 7000, 7001, 7002}, 2)
	add(ntfsOff, u)
	free(ntfsOff, 64*MiB, u)
	u = testdisk.Ext4(disk[extOff:extOff+256*MiB], []int64{3000, 3001, 20000}, 3)
	add(extOff, u)
	free(extOff, 256*MiB, u)
	f.must = append(f.must, extent.Extent{Off: rawOff, Len: 8 * MiB})
	testdisk.GPT(disk, [16]byte{0xD1, 0x5C}, 0x5EED1234, parts)
	f.must = append(f.must, extent.Extent{Off: 0, Len: 34 * 512}, extent.Extent{Off: int64(len(disk)) - 33*512, Len: 33 * 512})
	return f
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// freeKept reports how many bytes of the given free ranges were restored.
// The probe reads 64 KiB inside each range, avoiding merge-gap edges.
func roundTrip(t *testing.T, f *fixture, mode string, targetSize int64, split int64, zero bool) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	writeFile(t, src, f.disk)
	img := filepath.Join(dir, "out", "disk.bci")
	p := &Progress{}
	if err := Backup(context.Background(), BackupOptions{Source: src, Dest: img, Mode: mode, Level: 1, SplitSize: split, Verify: true}, p); err != nil {
		t.Fatal(err)
	}
	tgt := filepath.Join(dir, "tgt.img")
	tf, _ := os.Create(tgt)
	tf.Truncate(targetSize)
	tf.Close()
	if err := Restore(context.Background(), RestoreOptions{Image: img, Target: tgt, FixGPT: true, ZeroFill: zero, VerifyFirst: true}, p); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(tgt)
	if err != nil {
		t.Fatal(err)
	}
	return out, img
}

func TestUsedModeRoundTrip(t *testing.T) {
	f := makeDisk(t)
	out, img := roundTrip(t, f, ModeUsed, int64(len(f.disk))+64*MiB, 0, false)

	// On a larger target the GPT header pointers/CRC and the protective MBR
	// size field are rewritten; boot code, disk signature and entries are not.
	if !bytes.Equal(out[:446], f.disk[:446]) || !bytes.Equal(out[1024:34*512], f.disk[1024:34*512]) {
		t.Fatal("boot code / disk signature / partition entries changed")
	}
	for _, e := range f.must {
		if e.Off == 0 || e.End() > int64(len(f.disk))-33*512 {
			continue // checked above / old GPT backup location
		}
		if !bytes.Equal(out[e.Off:e.End()], f.disk[e.Off:e.End()]) {
			t.Fatalf("used region %v not restored exactly", e)
		}
	}
	// Large free regions must not have been copied (target was zeroed).
	skipped := 0
	for _, e := range f.free {
		if e.Len < 2*MiB {
			continue
		}
		mid := e.Off + e.Len/2
		if !bytes.Equal(out[mid:mid+64*1024], make([]byte, 64*1024)) {
			t.Fatalf("free region %v was copied", e)
		}
		skipped++
	}
	if skipped == 0 {
		t.Fatal("test fixture has no large free regions")
	}
	// Identity: same disk GUID, MBR signature and partition GUIDs, valid backup GPT at new end.
	a, err := part.Read(bytes.NewReader(f.disk), int64(len(f.disk)), 512)
	if err != nil {
		t.Fatal(err)
	}
	b, err := part.Read(bytes.NewReader(out), int64(len(out)), 512)
	if err != nil {
		t.Fatal(err)
	}
	if a.DiskID != b.DiskID || a.MBRSig != b.MBRSig || len(a.Partitions) != len(b.Partitions) {
		t.Fatalf("identity changed: %s/%s vs %s/%s", a.DiskID, a.MBRSig, b.DiskID, b.MBRSig)
	}
	for i := range a.Partitions {
		if a.Partitions[i].GUID != b.Partitions[i].GUID || a.Partitions[i].Start != b.Partitions[i].Start {
			t.Fatalf("partition %d changed", i)
		}
	}
	if b.GPT.AlternateLBA != uint64(len(out)/512-1) {
		t.Fatal("GPT backup not moved to end of larger disk")
	}
	meta, _ := ReadMeta(img)
	if meta.DataBytes >= int64(len(f.disk))/2 {
		t.Fatalf("used mode copied too much: %d", meta.DataBytes)
	}
	t.Logf("used mode: %d MiB of %d MiB copied, image %d KiB", meta.DataBytes/MiB, len(f.disk)/MiB, image.TotalSize(img)/1024)
}

func TestFullModeBitExact(t *testing.T) {
	f := makeDisk(t)
	out, _ := roundTrip(t, f, ModeFull, int64(len(f.disk)), 7*MiB, false)
	if sha256.Sum256(out) != sha256.Sum256(f.disk) {
		t.Fatal("full-mode restore is not bit-identical")
	}
}

func TestUsedModeZeroFillSameSize(t *testing.T) {
	f := makeDisk(t)
	out, _ := roundTrip(t, f, ModeUsed, int64(len(f.disk)), 0, true)
	for _, e := range f.must {
		if !bytes.Equal(out[e.Off:e.End()], f.disk[e.Off:e.End()]) {
			t.Fatalf("used region %v not restored exactly", e)
		}
	}
	// Same-size target: the GPT is byte-identical, including the backup.
	n := int64(len(f.disk))
	if !bytes.Equal(out[n-33*512:], f.disk[n-33*512:]) {
		t.Fatal("backup GPT differs on same-size target")
	}
}

func TestCorruptImageDetected(t *testing.T) {
	f := makeDisk(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	writeFile(t, src, f.disk)
	img := filepath.Join(dir, "disk.bci")
	p := &Progress{}
	if err := Backup(context.Background(), BackupOptions{Source: src, Dest: img, Mode: ModeUsed, Level: 1}, p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(img)
	b[len(b)/2] ^= 0x40
	writeFile(t, img, b)
	if _, err := Verify(context.Background(), img, p); !errors.Is(err, image.ErrCorrupt) {
		t.Fatalf("corruption not detected: %v", err)
	}
	// Truncated image.
	writeFile(t, img, b[:len(b)-100])
	if _, err := Verify(context.Background(), img, p); err == nil {
		t.Fatal("truncation not detected")
	}
}

func TestRefuseSmallerTarget(t *testing.T) {
	f := makeDisk(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	writeFile(t, src, f.disk)
	img := filepath.Join(dir, "disk.bci")
	p := &Progress{}
	if err := Backup(context.Background(), BackupOptions{Source: src, Dest: img, Mode: ModeUsed}, p); err != nil {
		t.Fatal(err)
	}
	tgt := filepath.Join(dir, "small.img")
	writeFile(t, tgt, make([]byte, 100*MiB))
	if err := Restore(context.Background(), RestoreOptions{Image: img, Target: tgt}, p); err == nil {
		t.Fatal("restore to smaller disk was allowed")
	}
}
