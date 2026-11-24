package fs

import (
	"bytes"
	"reflect"
	"testing"

	"bitclone/internal/extent"
	"bitclone/internal/testdisk"
)

func check(t *testing.T, name string, p []byte, want []extent.Extent) *Result {
	t.Helper()
	r := Detect(bytes.NewReader(p), int64(len(p)))
	if r == nil {
		t.Fatalf("%s: not detected", name)
	}
	if r.Err != "" {
		t.Fatalf("%s: %s", name, r.Err)
	}
	if !reflect.DeepEqual(r.Used, want) {
		t.Fatalf("%s: used extents\n got %v\nwant %v", name, r.Used, want)
	}
	return r
}

func TestFAT16(t *testing.T) {
	p := make([]byte, 32<<20)
	want := testdisk.FAT16(p, []int{2, 3, 4, 50, 51, 9000}, 1)
	r := check(t, "fat16", p, want)
	if r.FS != "fat16" || r.Label != "TESTVOL" || r.UUID != "1234-ABCD" {
		t.Fatalf("identity: %+v", r)
	}
}

func TestNTFS(t *testing.T) {
	p := make([]byte, 64<<20)
	want := testdisk.NTFS(p, []int64{20, 21, 22, 5000, 5001, 16000}, 2)
	r := check(t, "ntfs", p, want)
	if r.FS != "ntfs" || r.UUID != "1122334455667788" {
		t.Fatalf("identity: %+v", r)
	}
}

func TestExt4(t *testing.T) {
	p := make([]byte, 65536*4096)
	want := testdisk.Ext4(p, []int64{2000, 2001, 2002, 30000}, 3)
	r := check(t, "ext4", p, want)
	if r.FS != "ext4" || r.Label != "rootfs" || r.UUID != "deadbeef-0102-0304-0506-0708090a0b0c" {
		t.Fatalf("identity: %+v", r)
	}
}

func TestUnknown(t *testing.T) {
	p := make([]byte, 1<<20)
	testdisk.Fill(p, 9)
	if r := Detect(bytes.NewReader(p), int64(len(p))); r != nil {
		t.Fatalf("random data detected as %+v", r)
	}
}

func TestRunList(t *testing.T) {
	// len 0x10 at +0x100, len 0x5 at -0x20 (relative), sparse len 3
	runs, err := decodeRuns([]byte{0x21, 0x10, 0x00, 0x01, 0x11, 0x05, 0xE0, 0x01, 0x03, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	want := []run{{lcn: 0x100, length: 0x10}, {lcn: 0xE0, length: 5}, {length: 3, sparse: true}}
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("got %+v want %+v", runs, want)
	}
}
