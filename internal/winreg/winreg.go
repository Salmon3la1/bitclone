// Package winreg reads Windows registry hive files (SAM, SOFTWARE, SYSTEM…)
// directly from disk, read-only. Hive data comes from an untrusted disk, so
// every offset and length is bounds-checked; malformed data returns errors,
// never panics.
package winreg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// Value types.
const (
	REG_NONE      = 0
	REG_SZ        = 1
	REG_EXPAND_SZ = 2
	REG_BINARY    = 3
	REG_DWORD     = 4
	REG_MULTI_SZ  = 7
	REG_QWORD     = 11
)

var (
	ErrNotFound = errors.New("registry key or value not found")
	ErrCorrupt  = errors.New("registry hive is corrupt")
)

const (
	binStart     = 0x1000
	maxCell      = 64 << 20 // no legitimate cell is this large
	bigDataChunk = 16344
	maxDepth     = 8 // nesting of ri index lists
)

// Hive is an open registry hive file.
type Hive struct {
	r     io.ReaderAt
	size  int64
	root  uint32
	Dirty bool // the primary sequence numbers differ: Windows did not close the hive cleanly
	minor uint32
}

// Open parses the hive header.
func Open(r io.ReaderAt, size int64) (*Hive, error) {
	hdr := make([]byte, 512)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return nil, fmt.Errorf("read hive header: %w", err)
	}
	if string(hdr[:4]) != "regf" {
		return nil, errors.New("not a registry hive (no regf signature)")
	}
	le := binary.LittleEndian
	h := &Hive{r: r, size: size, root: le.Uint32(hdr[0x24:]), minor: le.Uint32(hdr[0x18:])}
	h.Dirty = le.Uint32(hdr[4:]) != le.Uint32(hdr[8:])
	return h, nil
}

// cell returns the data of the cell at a hive-bin-relative offset.
func (h *Hive) cell(off uint32) ([]byte, error) {
	if off == 0 || off == 0xFFFFFFFF {
		return nil, ErrNotFound
	}
	abs := int64(binStart) + int64(off)
	if abs+4 > h.size {
		return nil, ErrCorrupt
	}
	var sz [4]byte
	if _, err := h.r.ReadAt(sz[:], abs); err != nil {
		return nil, err
	}
	n := int64(int32(binary.LittleEndian.Uint32(sz[:])))
	if n < 0 {
		n = -n
	}
	if n < 4 || n > maxCell || abs+n > h.size {
		return nil, ErrCorrupt
	}
	b := make([]byte, n-4)
	if _, err := h.r.ReadAt(b, abs+4); err != nil {
		return nil, err
	}
	return b, nil
}

// Key is a registry key.
type Key struct {
	h    *Hive
	nk   []byte
	Name string
}

// Root returns the hive's root key.
func (h *Hive) Root() (*Key, error) { return h.key(h.root) }

func (h *Hive) key(off uint32) (*Key, error) {
	b, err := h.cell(off)
	if err != nil {
		return nil, err
	}
	if len(b) < 0x4C || string(b[:2]) != "nk" {
		return nil, ErrCorrupt
	}
	le := binary.LittleEndian
	nl := int(le.Uint16(b[0x48:]))
	if 0x4C+nl > len(b) {
		return nil, ErrCorrupt
	}
	name := decodeName(b[0x4C:0x4C+nl], le.Uint16(b[2:])&0x20 != 0)
	return &Key{h: h, nk: b, Name: name}, nil
}

func decodeName(b []byte, ascii bool) string {
	if ascii { // Latin-1
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return string(r)
	}
	return utf16le(b)
}

func utf16le(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// Subkeys lists the key's direct subkeys.
func (k *Key) Subkeys() ([]*Key, error) {
	le := binary.LittleEndian
	if le.Uint32(k.nk[0x14:]) == 0 {
		return nil, nil
	}
	var offs []uint32
	if err := k.h.listOffsets(le.Uint32(k.nk[0x1C:]), &offs, 0); err != nil {
		return nil, err
	}
	out := make([]*Key, 0, len(offs))
	for _, o := range offs {
		sk, err := k.h.key(o)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, nil
}

func (h *Hive) listOffsets(off uint32, out *[]uint32, depth int) error {
	if depth > maxDepth {
		return ErrCorrupt
	}
	b, err := h.cell(off)
	if err != nil {
		return err
	}
	if len(b) < 4 {
		return ErrCorrupt
	}
	le := binary.LittleEndian
	n := int(le.Uint16(b[2:]))
	switch string(b[:2]) {
	case "lf", "lh":
		if 4+n*8 > len(b) {
			return ErrCorrupt
		}
		for i := 0; i < n; i++ {
			*out = append(*out, le.Uint32(b[4+i*8:]))
		}
	case "li", "ri":
		if 4+n*4 > len(b) {
			return ErrCorrupt
		}
		for i := 0; i < n; i++ {
			o := le.Uint32(b[4+i*4:])
			if string(b[:2]) == "ri" {
				if err := h.listOffsets(o, out, depth+1); err != nil {
					return err
				}
			} else {
				*out = append(*out, o)
			}
		}
	default:
		return ErrCorrupt
	}
	return nil
}

// Subkey walks a backslash-separated path, matching names case-insensitively.
func (k *Key) Subkey(path string) (*Key, error) {
	cur := k
	for _, part := range strings.Split(path, `\`) {
		if part == "" {
			continue
		}
		subs, err := cur.Subkeys()
		if err != nil {
			return nil, err
		}
		var next *Key
		for _, s := range subs {
			if strings.EqualFold(s.Name, part) {
				next = s
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		cur = next
	}
	return cur, nil
}

// Value is a registry value. Type holds the raw type field, which some
// keys (SAM's Names\<user>) use to store a number instead of a type.
type Value struct {
	h    *Hive
	vk   []byte
	Name string
	Type uint32
}

// Values lists the key's values.
func (k *Key) Values() ([]*Value, error) {
	le := binary.LittleEndian
	n := int(le.Uint32(k.nk[0x24:]))
	if n == 0 {
		return nil, nil
	}
	list, err := k.h.cell(le.Uint32(k.nk[0x28:]))
	if err != nil {
		return nil, err
	}
	if n*4 > len(list) {
		return nil, ErrCorrupt
	}
	out := make([]*Value, 0, n)
	for i := 0; i < n; i++ {
		b, err := k.h.cell(le.Uint32(list[i*4:]))
		if err != nil {
			return nil, err
		}
		if len(b) < 0x14 || string(b[:2]) != "vk" {
			return nil, ErrCorrupt
		}
		nl := int(le.Uint16(b[2:]))
		if 0x14+nl > len(b) {
			return nil, ErrCorrupt
		}
		out = append(out, &Value{h: k.h, vk: b, Type: le.Uint32(b[0x0C:]),
			Name: decodeName(b[0x14:0x14+nl], le.Uint16(b[0x10:])&1 != 0)})
	}
	return out, nil
}

// Value returns a value by name ("" is the key's default value).
func (k *Key) Value(name string) (*Value, error) {
	vals, err := k.Values()
	if err != nil {
		return nil, err
	}
	for _, v := range vals {
		if strings.EqualFold(v.Name, name) {
			return v, nil
		}
	}
	return nil, fmt.Errorf("%w: value %q", ErrNotFound, name)
}

// Data returns the value's raw bytes.
func (v *Value) Data() ([]byte, error) {
	le := binary.LittleEndian
	size := le.Uint32(v.vk[4:])
	off := le.Uint32(v.vk[8:])
	if size&0x80000000 != 0 { // stored inline in the offset field
		n := size &^ 0x80000000
		if n > 4 {
			return nil, ErrCorrupt
		}
		return append([]byte(nil), v.vk[8:8+n]...), nil
	}
	if size == 0 {
		return nil, nil
	}
	if size > maxCell {
		return nil, ErrCorrupt
	}
	b, err := v.h.cell(off)
	if err != nil {
		return nil, err
	}
	if len(b) >= 8 && string(b[:2]) == "db" && size > bigDataChunk && v.h.minor >= 4 {
		return v.h.bigData(b, int(size))
	}
	if int(size) > len(b) {
		return nil, ErrCorrupt
	}
	return b[:size], nil
}

func (h *Hive) bigData(db []byte, size int) ([]byte, error) {
	le := binary.LittleEndian
	n := int(le.Uint16(db[2:]))
	list, err := h.cell(le.Uint32(db[4:]))
	if err != nil {
		return nil, err
	}
	if n*4 > len(list) {
		return nil, ErrCorrupt
	}
	out := make([]byte, 0, size)
	for i := 0; i < n && len(out) < size; i++ {
		seg, err := h.cell(le.Uint32(list[i*4:]))
		if err != nil {
			return nil, err
		}
		take := min(bigDataChunk, size-len(out), len(seg))
		out = append(out, seg[:take]...)
	}
	if len(out) != size {
		return nil, ErrCorrupt
	}
	return out, nil
}

// String decodes REG_SZ / REG_EXPAND_SZ (UTF-16LE, trailing NULs removed).
func (v *Value) String() (string, error) {
	b, err := v.Data()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(utf16le(b), "\x00"), nil
}

// Uint returns a REG_DWORD or REG_QWORD.
func (v *Value) Uint() (uint64, error) {
	b, err := v.Data()
	if err != nil {
		return 0, err
	}
	switch {
	case len(b) >= 8 && v.Type == REG_QWORD:
		return binary.LittleEndian.Uint64(b), nil
	case len(b) >= 4:
		return uint64(binary.LittleEndian.Uint32(b)), nil
	}
	return 0, ErrCorrupt
}

// GetString reads a string value at path\name, or "" if it is missing.
func (k *Key) GetString(path, name string) string {
	sk, err := k.Subkey(path)
	if err != nil {
		return ""
	}
	v, err := sk.Value(name)
	if err != nil {
		return ""
	}
	s, _ := v.String()
	return s
}

// GetUint reads a DWORD/QWORD value at path\name.
func (k *Key) GetUint(path, name string) (uint64, bool) {
	sk, err := k.Subkey(path)
	if err != nil {
		return 0, false
	}
	v, err := sk.Value(name)
	if err != nil {
		return 0, false
	}
	n, err := v.Uint()
	return n, err == nil
}
