// Package part parses MBR and GPT partition tables and can relocate the GPT
// backup header when an image is restored onto a larger disk, without
// changing any disk or partition identifiers.
package part

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"unicode/utf16"
)

// Partition describes one entry of the partition table.
type Partition struct {
	Index     int    `json:"index"`
	Start     int64  `json:"start"` // bytes from start of disk
	Size      int64  `json:"size"`  // bytes
	Type      string `json:"type"`  // MBR type byte ("0x07") or GPT type GUID
	TypeName  string `json:"typeName"`
	Name      string `json:"name,omitempty"` // GPT partition name
	GUID      string `json:"guid,omitempty"` // GPT unique partition GUID
	Bootable  bool   `json:"bootable,omitempty"`
	Logical   bool   `json:"logical,omitempty"`   // MBR logical partition
	Container bool   `json:"container,omitempty"` // MBR extended container (holds no data itself)
}

// Table is a parsed partition table.
type Table struct {
	Scheme     string      `json:"scheme"` // "mbr" or "gpt"
	SectorSize int         `json:"sectorSize"`
	DiskID     string      `json:"diskId"` // MBR disk signature or GPT disk GUID
	MBRSig     string      `json:"mbrSignature"`
	Partitions []Partition `json:"partitions"`
	GPT        *GPTHeader  `json:"gpt,omitempty"`
}

// DataPartitions returns partitions that hold data (skips extended containers).
func (t *Table) DataPartitions() []Partition {
	var out []Partition
	for _, p := range t.Partitions {
		if !p.Container {
			out = append(out, p)
		}
	}
	return out
}

// GPTHeader holds the fields of a GPT header we care about.
type GPTHeader struct {
	HeaderSize   uint32 `json:"headerSize"`
	MyLBA        uint64 `json:"myLba"`
	AlternateLBA uint64 `json:"alternateLba"`
	FirstUsable  uint64 `json:"firstUsableLba"`
	LastUsable   uint64 `json:"lastUsableLba"`
	DiskGUID     string `json:"diskGuid"`
	EntriesLBA   uint64 `json:"entriesLba"`
	NumEntries   uint32 `json:"numEntries"`
	EntrySize    uint32 `json:"entrySize"`
	EntriesCRC   uint32 `json:"entriesCrc"`
}

// ErrNoTable is returned when the disk has no recognizable partition table.
var ErrNoTable = errors.New("no partition table found")

func readAt(r io.ReaderAt, off int64, n int) ([]byte, error) {
	b := make([]byte, n)
	m, err := r.ReadAt(b, off)
	if m == n {
		return b, nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

// Read parses the partition table of a disk of the given size. sectorSize is
// the device's logical sector size; pass 0 to probe 512 and 4096.
func Read(r io.ReaderAt, size int64, sectorSize int) (*Table, error) {
	mbr, err := readAt(r, 0, 512)
	if err != nil {
		return nil, err
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, ErrNoTable
	}
	sizes := []int{512, 4096}
	if sectorSize > 0 {
		sizes = []int{sectorSize}
	}
	protective := false
	for i := 0; i < 4; i++ {
		if mbr[446+16*i+4] == 0xEE {
			protective = true
		}
	}
	if protective {
		for _, ss := range sizes {
			if t, err := readGPT(r, size, ss); err == nil {
				t.MBRSig = fmt.Sprintf("%08X", binary.LittleEndian.Uint32(mbr[440:]))
				return t, nil
			}
		}
	}
	ss := 512
	if sectorSize > 0 {
		ss = sectorSize
	}
	return readMBR(r, mbr, ss)
}

func readMBR(r io.ReaderAt, mbr []byte, ss int) (*Table, error) {
	sig := binary.LittleEndian.Uint32(mbr[440:])
	t := &Table{Scheme: "mbr", SectorSize: ss, DiskID: fmt.Sprintf("%08X", sig), MBRSig: fmt.Sprintf("%08X", sig)}
	sec := int64(ss)
	idx := 0
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i : 446+16*(i+1)]
		typ := e[4]
		start := int64(binary.LittleEndian.Uint32(e[8:]))
		count := int64(binary.LittleEndian.Uint32(e[12:]))
		idx++
		if typ == 0 || count == 0 {
			continue
		}
		p := Partition{Index: idx, Start: start * sec, Size: count * sec, Type: fmt.Sprintf("0x%02X", typ),
			TypeName: mbrTypeName(typ), Bootable: e[0] == 0x80}
		if isExtended(typ) {
			p.Container = true
			t.Partitions = append(t.Partitions, p)
			logicals, err := readEBRChain(r, start, sec)
			if err != nil {
				return nil, err
			}
			t.Partitions = append(t.Partitions, logicals...)
			continue
		}
		t.Partitions = append(t.Partitions, p)
	}
	// Renumber logical partitions Linux-style: 5, 6, 7...
	n := 5
	for i := range t.Partitions {
		if t.Partitions[i].Logical {
			t.Partitions[i].Index = n
			n++
		}
	}
	return t, nil
}

func isExtended(t byte) bool { return t == 0x05 || t == 0x0F || t == 0x85 }

func readEBRChain(r io.ReaderAt, extStart, sec int64) ([]Partition, error) {
	var out []Partition
	ebr := extStart
	seen := map[int64]bool{}
	for guard := 0; guard < 256; guard++ {
		if seen[ebr] {
			return nil, errors.New("mbr: EBR chain loops")
		}
		seen[ebr] = true
		b, err := readAt(r, ebr*sec, 512)
		if err != nil {
			return nil, fmt.Errorf("mbr: reading EBR: %w", err)
		}
		if b[510] != 0x55 || b[511] != 0xAA {
			break
		}
		e1, e2 := b[446:462], b[462:478]
		if typ := e1[4]; typ != 0 {
			start := ebr + int64(binary.LittleEndian.Uint32(e1[8:]))
			count := int64(binary.LittleEndian.Uint32(e1[12:]))
			if count > 0 {
				out = append(out, Partition{Start: start * sec, Size: count * sec, Type: fmt.Sprintf("0x%02X", typ),
					TypeName: mbrTypeName(typ), Bootable: e1[0] == 0x80, Logical: true})
			}
		}
		if e2[4] == 0 || binary.LittleEndian.Uint32(e2[8:]) == 0 {
			break
		}
		ebr = extStart + int64(binary.LittleEndian.Uint32(e2[8:]))
	}
	return out, nil
}

func parseGPTHeader(b []byte) (*GPTHeader, error) {
	if string(b[0:8]) != "EFI PART" {
		return nil, errors.New("gpt: bad signature")
	}
	hs := binary.LittleEndian.Uint32(b[12:])
	if hs < 92 || int(hs) > len(b) {
		return nil, errors.New("gpt: bad header size")
	}
	want := binary.LittleEndian.Uint32(b[16:])
	tmp := append([]byte(nil), b[:hs]...)
	binary.LittleEndian.PutUint32(tmp[16:], 0)
	if crc32.ChecksumIEEE(tmp) != want {
		return nil, errors.New("gpt: header CRC mismatch")
	}
	h := &GPTHeader{
		HeaderSize:   hs,
		MyLBA:        binary.LittleEndian.Uint64(b[24:]),
		AlternateLBA: binary.LittleEndian.Uint64(b[32:]),
		FirstUsable:  binary.LittleEndian.Uint64(b[40:]),
		LastUsable:   binary.LittleEndian.Uint64(b[48:]),
		DiskGUID:     FormatGUID(b[56:72]),
		EntriesLBA:   binary.LittleEndian.Uint64(b[72:]),
		NumEntries:   binary.LittleEndian.Uint32(b[80:]),
		EntrySize:    binary.LittleEndian.Uint32(b[84:]),
		EntriesCRC:   binary.LittleEndian.Uint32(b[88:]),
	}
	if h.EntrySize < 128 || h.EntrySize > 4096 || h.NumEntries > 16384 {
		return nil, errors.New("gpt: implausible entry geometry")
	}
	return h, nil
}

func readGPT(r io.ReaderAt, size int64, ss int) (*Table, error) {
	sec := int64(ss)
	b, err := readAt(r, sec, ss)
	if err != nil {
		return nil, err
	}
	h, err := parseGPTHeader(b)
	if err != nil {
		return nil, err
	}
	n := int(h.NumEntries) * int(h.EntrySize)
	ents, err := readAt(r, int64(h.EntriesLBA)*sec, n)
	if err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(ents) != h.EntriesCRC {
		return nil, errors.New("gpt: entries CRC mismatch")
	}
	t := &Table{Scheme: "gpt", SectorSize: ss, DiskID: h.DiskGUID, GPT: h}
	for i := 0; i < int(h.NumEntries); i++ {
		e := ents[i*int(h.EntrySize):]
		if isZero(e[0:16]) {
			continue
		}
		first := int64(binary.LittleEndian.Uint64(e[32:]))
		last := int64(binary.LittleEndian.Uint64(e[40:]))
		if last < first {
			continue
		}
		typ := FormatGUID(e[0:16])
		t.Partitions = append(t.Partitions, Partition{
			Index:    i + 1,
			Start:    first * sec,
			Size:     (last - first + 1) * sec,
			Type:     typ,
			TypeName: gptTypeName(typ),
			GUID:     FormatGUID(e[16:32]),
			Name:     utf16Name(e[56:128]),
		})
	}
	return t, nil
}

// RelocateGPTBackup moves the GPT backup header and entry array to the end of
// a disk of newSize bytes (used after restoring onto a larger disk). The disk
// GUID, partition GUIDs and entries are unchanged; only the header pointers and
// CRCs are rewritten, and the protective MBR's size field is updated. The old
// backup header sector is zeroed so tools don't find a stale copy.
func RelocateGPTBackup(rw interface {
	io.ReaderAt
	io.WriterAt
}, newSize int64, ss int) error {
	sec := int64(ss)
	hb, err := readAt(rw, sec, ss)
	if err != nil {
		return err
	}
	h, err := parseGPTHeader(hb)
	if err != nil {
		return err
	}
	lastLBA := uint64(newSize/sec) - 1
	if h.AlternateLBA == lastLBA {
		return nil // already in place
	}
	entBytes := int64(h.NumEntries) * int64(h.EntrySize)
	entSectors := uint64((entBytes + sec - 1) / sec)
	ents, err := readAt(rw, int64(h.EntriesLBA)*sec, int(entSectors)*ss)
	if err != nil {
		return err
	}
	oldAlt := h.AlternateLBA
	backupEntriesLBA := lastLBA - entSectors
	if backupEntriesLBA <= h.LastUsable {
		return errors.New("gpt: no room for backup table on target")
	}

	// Primary header: point at the new alternate location.
	prim := append([]byte(nil), hb...)
	binary.LittleEndian.PutUint64(prim[32:], lastLBA)
	putHeaderCRC(prim)

	// Backup header: mirror of primary with swapped LBAs.
	bak := append([]byte(nil), hb...)
	binary.LittleEndian.PutUint64(bak[24:], lastLBA)
	binary.LittleEndian.PutUint64(bak[32:], 1)
	binary.LittleEndian.PutUint64(bak[72:], backupEntriesLBA)
	putHeaderCRC(bak)

	if _, err := rw.WriteAt(ents, int64(backupEntriesLBA)*sec); err != nil {
		return err
	}
	if _, err := rw.WriteAt(bak, int64(lastLBA)*sec); err != nil {
		return err
	}
	if _, err := rw.WriteAt(prim, sec); err != nil {
		return err
	}
	if oldAlt > 1 && oldAlt < lastLBA && oldAlt > h.LastUsable {
		if _, err := rw.WriteAt(make([]byte, ss), int64(oldAlt)*sec); err != nil {
			return err
		}
	}
	// Protective MBR size field (disk signature at 0x1B8 untouched).
	mbr, err := readAt(rw, 0, 512)
	if err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i:]
		if e[4] == 0xEE {
			cnt := uint64(newSize/sec) - 1
			if cnt > 0xFFFFFFFF {
				cnt = 0xFFFFFFFF
			}
			binary.LittleEndian.PutUint32(e[12:], uint32(cnt))
			if _, err := rw.WriteAt(mbr, 0); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func putHeaderCRC(b []byte) {
	hs := binary.LittleEndian.Uint32(b[12:])
	binary.LittleEndian.PutUint32(b[16:], 0)
	binary.LittleEndian.PutUint32(b[16:], crc32.ChecksumIEEE(b[:hs]))
}

// FormatGUID formats a mixed-endian on-disk GUID.
func FormatGUID(b []byte) string {
	return fmt.Sprintf("%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		binary.LittleEndian.Uint32(b[0:]), binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:]),
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func utf16Name(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

func mbrTypeName(t byte) string {
	switch t {
	case 0x01:
		return "FAT12"
	case 0x04, 0x06, 0x0E:
		return "FAT16"
	case 0x0B, 0x0C:
		return "FAT32"
	case 0x07:
		return "NTFS/exFAT"
	case 0x05, 0x0F, 0x85:
		return "Extended"
	case 0x27:
		return "Windows Recovery"
	case 0x82:
		return "Linux swap"
	case 0x83:
		return "Linux"
	case 0x8E:
		return "Linux LVM"
	case 0xEE:
		return "GPT protective"
	case 0xEF:
		return "EFI System"
	case 0xFD:
		return "Linux RAID"
	}
	return "Unknown"
}

var gptTypes = map[string]string{
	"C12A7328-F81F-11D2-BA4B-00A0C93EC93B": "EFI System",
	"E3C9E316-0B5C-4DB8-817D-F92DF00215AE": "Microsoft Reserved",
	"EBD0A0A2-B9E5-4433-87C0-68B6B72699C7": "Microsoft Basic Data",
	"DE94BBA4-06D1-4D40-A16A-BFD50179D6AC": "Windows Recovery",
	"0FC63DAF-8483-4772-8E79-3D69D8477DE4": "Linux filesystem",
	"0657FD6D-A4AB-43C4-84E5-0933C84B4F4F": "Linux swap",
	"E6D6D379-F507-44C2-A23C-238F2A3DF928": "Linux LVM",
	"A19D880F-05FC-4D3B-A006-743F0F84911E": "Linux RAID",
	"4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709": "Linux root (x86-64)",
	"BC13C2FF-59E6-4262-A352-B275FD6F7172": "Linux extended boot",
	"21686148-6449-6E6F-744E-656564454649": "BIOS boot",
	"5808C8AA-7E8F-42E0-85D2-E1E90434CFB3": "Windows LDM metadata",
	"AF9B60A0-1431-4F62-BC68-3311714A69AD": "Windows LDM data",
}

func gptTypeName(guid string) string {
	if n, ok := gptTypes[strings.ToUpper(guid)]; ok {
		return n
	}
	return "Unknown"
}
