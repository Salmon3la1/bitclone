// Package regtest builds small registry hive files for tests.
package regtest

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// Key is a key to write.
type Key struct {
	Name   string
	Values []Value
	Keys   []*Key
}

// Value is a value to write. Type is written as-is (SAM stores RIDs there).
type Value struct {
	Name string
	Type uint32
	Data []byte
}

// K makes a key.
func K(name string, children ...*Key) *Key { return &Key{Name: name, Keys: children} }

// With adds values and returns the key.
func (k *Key) With(v ...Value) *Key { k.Values = append(k.Values, v...); return k }

// Path returns (creating) the key at a backslash path below k.
func (k *Key) Path(path string) *Key {
	cur := k
	for _, part := range strings.Split(path, `\`) {
		var next *Key
		for _, c := range cur.Keys {
			if c.Name == part {
				next = c
			}
		}
		if next == nil {
			next = K(part)
			cur.Keys = append(cur.Keys, next)
		}
		cur = next
	}
	return cur
}

// SZ is a REG_SZ value.
func SZ(name, s string) Value { return Value{name, 1, UTF16(s + "\x00")} }

// DWORD is a REG_DWORD value.
func DWORD(name string, n uint32) Value {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return Value{name, 4, b}
}

// BIN is a REG_BINARY value.
func BIN(name string, b []byte) Value { return Value{name, 3, b} }

// UTF16 encodes s as UTF-16LE.
func UTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

type writer struct{ data []byte } // hive bin contents, offsets relative to 0x1000

func (w *writer) alloc(payload []byte) uint32 {
	off := uint32(len(w.data))
	n := (4 + len(payload) + 7) &^ 7
	c := make([]byte, n)
	binary.LittleEndian.PutUint32(c, uint32(-int32(n)))
	copy(c[4:], payload)
	w.data = append(w.data, c...)
	return off
}

const bigChunk = 16344

func (w *writer) value(v Value) uint32 {
	le := binary.LittleEndian
	vk := make([]byte, 0x14+len(v.Name))
	copy(vk, "vk")
	le.PutUint16(vk[2:], uint16(len(v.Name)))
	le.PutUint32(vk[0x0C:], v.Type)
	le.PutUint16(vk[0x10:], 1) // ASCII name
	copy(vk[0x14:], v.Name)
	switch {
	case len(v.Data) <= 4:
		le.PutUint32(vk[4:], uint32(len(v.Data))|0x80000000)
		copy(vk[8:], v.Data)
	case len(v.Data) > bigChunk: // "db" big-data record
		var segs []byte
		for i := 0; i < len(v.Data); i += bigChunk {
			segs = le.AppendUint32(segs, w.alloc(v.Data[i:min(i+bigChunk, len(v.Data))]))
		}
		list := w.alloc(segs)
		db := make([]byte, 8)
		copy(db, "db")
		le.PutUint16(db[2:], uint16(len(segs)/4))
		le.PutUint32(db[4:], list)
		le.PutUint32(vk[4:], uint32(len(v.Data)))
		le.PutUint32(vk[8:], w.alloc(db))
	default:
		le.PutUint32(vk[4:], uint32(len(v.Data)))
		le.PutUint32(vk[8:], w.alloc(v.Data))
	}
	return w.alloc(vk)
}

func (w *writer) key(k *Key, root bool) uint32 {
	le := binary.LittleEndian
	nk := make([]byte, 0x4C+len(k.Name))
	copy(nk, "nk")
	flags := uint16(0x20) // ASCII name
	if root {
		flags |= 0x04
	}
	le.PutUint16(nk[2:], flags)
	le.PutUint16(nk[0x48:], uint16(len(k.Name)))
	copy(nk[0x4C:], k.Name)
	le.PutUint32(nk[0x1C:], 0xFFFFFFFF)
	le.PutUint32(nk[0x28:], 0xFFFFFFFF)
	if len(k.Keys) > 0 {
		// Alternate list kinds so the reader's lf, lh, li and ri paths all get exercised.
		var lst []byte
		switch len(k.Name) % 3 {
		case 0:
			lst = append([]byte("lh"), 0, 0)
		case 1:
			lst = append([]byte("lf"), 0, 0)
		default:
			lst = append([]byte("li"), 0, 0)
		}
		le.PutUint16(lst[2:], uint16(len(k.Keys)))
		for _, c := range k.Keys {
			lst = le.AppendUint32(lst, w.key(c, false))
			if lst[1] != 'i' {
				lst = le.AppendUint32(lst, 0) // hash (not checked by readers)
			}
		}
		off := w.alloc(lst)
		if len(k.Keys) > 2 { // wrap in an ri index
			off = w.alloc(le.AppendUint32([]byte{'r', 'i', 1, 0}, off))
		}
		le.PutUint32(nk[0x14:], uint32(len(k.Keys)))
		le.PutUint32(nk[0x1C:], off)
	}
	if len(k.Values) > 0 {
		var lst []byte
		for _, v := range k.Values {
			lst = le.AppendUint32(lst, w.value(v))
		}
		le.PutUint32(nk[0x24:], uint32(len(k.Values)))
		le.PutUint32(nk[0x28:], w.alloc(lst))
	}
	return w.alloc(nk)
}

// Build serialises a hive whose root key is root.
func Build(root *Key) []byte {
	w := &writer{data: make([]byte, 0x20)} // hbin header occupies the first 32 bytes
	rootOff := w.key(root, true)
	size := (len(w.data) + 4095) &^ 4095
	if rem := size - len(w.data); rem > 0 { // the rest of the bin is one free cell
		c := make([]byte, rem)
		binary.LittleEndian.PutUint32(c, uint32(rem))
		w.data = append(w.data, c...)
	}
	le := binary.LittleEndian
	copy(w.data, "hbin")
	le.PutUint32(w.data[8:], uint32(size))
	hdr := make([]byte, 4096)
	copy(hdr, "regf")
	le.PutUint32(hdr[4:], 1)
	le.PutUint32(hdr[8:], 1)
	le.PutUint32(hdr[0x14:], 1)
	le.PutUint32(hdr[0x18:], 5)
	le.PutUint32(hdr[0x20:], 1)
	le.PutUint32(hdr[0x24:], rootOff)
	le.PutUint32(hdr[0x28:], uint32(size))
	le.PutUint32(hdr[0x2C:], 1)
	var sum uint32
	for i := 0; i < 0x1FC; i += 4 {
		sum ^= le.Uint32(hdr[i:])
	}
	le.PutUint32(hdr[0x1FC:], sum)
	return append(hdr, w.data...)
}
