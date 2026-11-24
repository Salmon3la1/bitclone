package part

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"imagen/internal/testdisk"
)

var guid = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func TestGPTReadAndRelocate(t *testing.T) {
	disk := make([]byte, 16<<20)
	testdisk.GPT(disk, guid, 0xCAFEBABE, []testdisk.Part{
		{Start: 1 << 20, Size: 4 << 20, Type: testdisk.BasicData, GUID: [16]byte{0xAA}, Name: "Data"},
		{Start: 5 << 20, Size: 2 << 20, Type: testdisk.BasicData, GUID: [16]byte{0xBB}},
	})
	tb, err := Read(bytes.NewReader(disk), int64(len(disk)), 512)
	if err != nil {
		t.Fatal(err)
	}
	if tb.Scheme != "gpt" || tb.DiskID != "04030201-0605-0807-090A-0B0C0D0E0F10" || tb.MBRSig != "CAFEBABE" {
		t.Fatalf("table: %+v", tb)
	}
	if len(tb.Partitions) != 2 || tb.Partitions[0].Name != "Data" || tb.Partitions[0].TypeName != "Microsoft Basic Data" ||
		tb.Partitions[1].Start != 5<<20 || tb.Partitions[1].Size != 2<<20 {
		t.Fatalf("partitions: %+v", tb.Partitions)
	}

	// "Restore" to a 24 MiB disk and relocate the backup header.
	f, err := os.Create(filepath.Join(t.TempDir(), "big.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(disk)
	f.Truncate(24 << 20)
	if err := RelocateGPTBackup(f, 24<<20, 512); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 24<<20)
	f.ReadAt(big, 0)
	tb2, err := Read(bytes.NewReader(big), int64(len(big)), 512)
	if err != nil {
		t.Fatal(err)
	}
	if tb2.DiskID != tb.DiskID || tb2.Partitions[0].GUID != tb.Partitions[0].GUID {
		t.Fatal("identity changed after relocation")
	}
	lastLBA := uint64(24<<20)/512 - 1
	if tb2.GPT.AlternateLBA != lastLBA {
		t.Fatalf("alternate LBA %d, want %d", tb2.GPT.AlternateLBA, lastLBA)
	}
	bh, err := parseGPTHeader(big[lastLBA*512 : lastLBA*512+512])
	if err != nil {
		t.Fatalf("backup header invalid: %v", err)
	}
	if bh.MyLBA != lastLBA || bh.AlternateLBA != 1 || bh.DiskGUID != tb.DiskID {
		t.Fatalf("backup header: %+v", bh)
	}
	if !bytes.Equal(big[bh.EntriesLBA*512:bh.EntriesLBA*512+128*128], disk[2*512:2*512+128*128]) {
		t.Fatal("backup entries differ")
	}
	if binary.LittleEndian.Uint32(big[440:]) != 0xCAFEBABE {
		t.Fatal("MBR signature changed")
	}
	if binary.LittleEndian.Uint32(big[446+12:]) != uint32(lastLBA) {
		t.Fatal("protective MBR size not updated")
	}
	// Old backup header (end of 16 MiB) must be wiped.
	old := (16<<20)/512 - 1
	if string(big[old*512:old*512+8]) == "EFI PART" {
		t.Fatal("stale backup header left behind")
	}
}

func TestMBRLogical(t *testing.T) {
	disk := make([]byte, 64<<20)
	put := func(sec int64, idx int, typ byte, start, count uint32) {
		e := disk[sec*512+446+int64(idx)*16:]
		e[4] = typ
		binary.LittleEndian.PutUint32(e[8:], start)
		binary.LittleEndian.PutUint32(e[12:], count)
		disk[sec*512+510], disk[sec*512+511] = 0x55, 0xAA
	}
	binary.LittleEndian.PutUint32(disk[440:], 0x11223344)
	put(0, 0, 0x07, 2048, 20480)
	put(0, 1, 0x0F, 24576, 100000)
	// EBR 1 at 24576: logical at +2048, next EBR at +40960
	put(24576, 0, 0x83, 2048, 10000)
	put(24576, 1, 0x05, 40960, 20000)
	// EBR 2 at 24576+40960
	put(24576+40960, 0, 0x82, 2048, 8000)
	tb, err := Read(bytes.NewReader(disk), int64(len(disk)), 512)
	if err != nil {
		t.Fatal(err)
	}
	if tb.Scheme != "mbr" || tb.DiskID != "11223344" {
		t.Fatalf("%+v", tb)
	}
	dp := tb.DataPartitions()
	if len(dp) != 3 || dp[1].Index != 5 || dp[1].Start != (24576+2048)*512 || dp[2].Index != 6 || dp[2].Start != (24576+40960+2048)*512 {
		t.Fatalf("partitions: %+v", dp)
	}
}
