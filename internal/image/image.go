// Package image implements the .bci disk image container.
//
// Layout (all integers little-endian):
//
//	magic "BCLIMG01" | u32 version | u32 metaLen | metaLen bytes of JSON Meta
//	repeated chunk records:
//	  "CHNK" | u64 diskOffset | u32 rawLen | u32 storedLen | u32 crc32c(raw) | u8 codec | 7 pad
//	  storedLen bytes of payload
//	end record:
//	  "ENDS" | u64 chunkCount | u64 rawBytes | [32]byte sha256
//
// The SHA-256 covers every chunk header and payload in order, so truncation,
// reordering and corruption are all detected. Chunks are written in ascending
// disk-offset order; regions not covered by any chunk were not copied (free
// space in "used" mode).
package image

import (
	"bufio"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"sync"
	"time"

	"imagen/internal/extent"
	"imagen/internal/part"
)

const (
	Magic   = "BCLIMG01"
	Version = 1

	CodecRaw     = 0
	CodecDeflate = 1
	CodecZero    = 2

	MaxChunk = 64 << 20
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ErrCorrupt is returned when the image fails an integrity check.
var ErrCorrupt = errors.New("image is corrupt or truncated")

// SourceInfo describes the disk the image was taken from.
type SourceInfo struct {
	Path       string `json:"path"`
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	Size       int64  `json:"size"`
	SectorSize int    `json:"sectorSize"`
}

// PartInfo describes a source partition and how it was captured.
type PartInfo struct {
	part.Partition
	FS        string `json:"fs,omitempty"`
	Label     string `json:"label,omitempty"`
	UUID      string `json:"uuid,omitempty"`
	Strategy  string `json:"strategy"` // "used" or "full"
	UsedBytes int64  `json:"usedBytes"`
	Note      string `json:"note,omitempty"`
}

// Meta is stored as JSON at the start of every image.
type Meta struct {
	Tool        string      `json:"tool"`
	ToolVersion string      `json:"toolVersion"`
	Created     time.Time   `json:"created"`
	Mode        string      `json:"mode"` // "used" or "full"
	ChunkSize   int         `json:"chunkSize"`
	Source      SourceInfo  `json:"source"`
	Table       *part.Table `json:"table,omitempty"`
	Parts       []PartInfo  `json:"parts,omitempty"`
	DataBytes   int64       `json:"dataBytes"` // bytes of disk captured
	Regions     int         `json:"regions"`
	Warnings    []string    `json:"warnings,omitempty"`
	// Extents is filled in only for small region lists; informational.
	Extents []extent.Extent `json:"extents,omitempty"`
}

// Encode compresses one chunk of raw disk data.
func Encode(raw []byte, level int) (codec byte, stored []byte, crc uint32) {
	crc = crc32.Checksum(raw, castagnoli)
	if allZero(raw) {
		return CodecZero, nil, crc
	}
	if level == 0 {
		return CodecRaw, raw, crc
	}
	var buf bytes.Buffer
	buf.Grow(len(raw) / 2)
	w := flateWriter(&buf, level)
	w.Write(raw)
	w.Close()
	putFlateWriter(w, level)
	if buf.Len() >= len(raw) {
		return CodecRaw, raw, crc
	}
	return CodecDeflate, buf.Bytes(), crc
}

var flatePools sync.Map // level -> *sync.Pool

func flateWriter(dst io.Writer, level int) *flate.Writer {
	p, _ := flatePools.LoadOrStore(level, &sync.Pool{})
	if w, ok := p.(*sync.Pool).Get().(*flate.Writer); ok {
		w.Reset(dst)
		return w
	}
	w, _ := flate.NewWriter(dst, level)
	return w
}

func putFlateWriter(w *flate.Writer, level int) {
	p, _ := flatePools.Load(level)
	p.(*sync.Pool).Put(w)
}

func allZero(b []byte) bool {
	for len(b) >= 8 {
		if binary.LittleEndian.Uint64(b) != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Chunk is one record read back from an image.
type Chunk struct {
	Off    int64
	RawLen int
	Codec  byte
	CRC    uint32
	Stored []byte
}

// Decode returns the raw data of a chunk, verifying its checksum.
func Decode(c *Chunk) ([]byte, error) {
	var raw []byte
	switch c.Codec {
	case CodecZero:
		raw = make([]byte, c.RawLen)
	case CodecRaw:
		raw = c.Stored
	case CodecDeflate:
		raw = make([]byte, c.RawLen)
		fr := flate.NewReader(bytes.NewReader(c.Stored))
		if _, err := io.ReadFull(fr, raw); err != nil {
			return nil, fmt.Errorf("%w: chunk at %d: %v", ErrCorrupt, c.Off, err)
		}
		fr.Close()
	default:
		return nil, fmt.Errorf("%w: unknown codec %d", ErrCorrupt, c.Codec)
	}
	if len(raw) != c.RawLen || crc32.Checksum(raw, castagnoli) != c.CRC {
		return nil, fmt.Errorf("%w: checksum mismatch in chunk at disk offset %d", ErrCorrupt, c.Off)
	}
	return raw, nil
}

// Writer writes an image stream.
type Writer struct {
	bw    *bufio.Writer
	h     hash.Hash
	count uint64
	raw   uint64
	last  int64
}

// NewWriter writes the header and metadata.
func NewWriter(w io.Writer, meta *Meta) (*Writer, error) {
	js, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	iw := &Writer{bw: bufio.NewWriterSize(w, 1<<20), h: sha256.New(), last: -1}
	var hdr [16]byte
	copy(hdr[:], Magic)
	binary.LittleEndian.PutUint32(hdr[8:], Version)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(js)))
	iw.write(hdr[:])
	iw.write(js)
	return iw, iw.bw.Flush()
}

func (w *Writer) write(b []byte) {
	w.bw.Write(b)
	w.h.Write(b)
}

// WriteChunk appends one encoded chunk. Offsets must be strictly increasing.
func (w *Writer) WriteChunk(off int64, rawLen int, codec byte, stored []byte, crc uint32) error {
	if off <= w.last {
		return errors.New("image: chunks out of order")
	}
	w.last = off
	var h [32]byte
	copy(h[:], "CHNK")
	binary.LittleEndian.PutUint64(h[4:], uint64(off))
	binary.LittleEndian.PutUint32(h[12:], uint32(rawLen))
	binary.LittleEndian.PutUint32(h[16:], uint32(len(stored)))
	binary.LittleEndian.PutUint32(h[20:], crc)
	h[24] = codec
	w.write(h[:])
	w.write(stored)
	w.count++
	w.raw += uint64(rawLen)
	_, err := w.bw.Write(nil)
	return err
}

// Close writes the end record and flushes. It does not close the underlying writer.
func (w *Writer) Close() error {
	var e [20]byte
	copy(e[:], "ENDS")
	binary.LittleEndian.PutUint64(e[4:], w.count)
	binary.LittleEndian.PutUint64(e[12:], w.raw)
	w.write(e[:])
	sum := w.h.Sum(nil)
	w.bw.Write(sum)
	return w.bw.Flush()
}

// Reader reads an image stream.
type Reader struct {
	br    *bufio.Reader
	h     hash.Hash
	Meta  *Meta
	count uint64
	raw   uint64
	done  bool
}

// NewReader reads and validates the header.
func NewReader(r io.Reader) (*Reader, error) {
	ir := &Reader{br: bufio.NewReaderSize(r, 1<<20), h: sha256.New()}
	var hdr [16]byte
	if err := ir.read(hdr[:]); err != nil {
		return nil, fmt.Errorf("not an ImaGen image: %w", err)
	}
	if string(hdr[:8]) != Magic {
		return nil, errors.New("not an ImaGen image (bad magic)")
	}
	if v := binary.LittleEndian.Uint32(hdr[8:]); v != Version {
		return nil, fmt.Errorf("unsupported image version %d", v)
	}
	n := binary.LittleEndian.Uint32(hdr[12:])
	if n > 256<<20 {
		return nil, ErrCorrupt
	}
	js := make([]byte, n)
	if err := ir.read(js); err != nil {
		return nil, err
	}
	ir.Meta = &Meta{}
	if err := json.Unmarshal(js, ir.Meta); err != nil {
		return nil, fmt.Errorf("%w: metadata: %v", ErrCorrupt, err)
	}
	return ir, nil
}

func (r *Reader) read(b []byte) error {
	if _, err := io.ReadFull(r.br, b); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	r.h.Write(b)
	return nil
}

// Next returns the next chunk, or io.EOF after the end record has been
// read and the image checksum verified.
func (r *Reader) Next() (*Chunk, error) {
	if r.done {
		return nil, io.EOF
	}
	var tag [4]byte
	if err := r.read(tag[:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	switch string(tag[:]) {
	case "CHNK":
		var h [28]byte
		if err := r.read(h[:]); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		c := &Chunk{
			Off:    int64(binary.LittleEndian.Uint64(h[0:])),
			RawLen: int(binary.LittleEndian.Uint32(h[8:])),
			CRC:    binary.LittleEndian.Uint32(h[16:]),
			Codec:  h[20],
		}
		sl := binary.LittleEndian.Uint32(h[12:])
		if c.RawLen > MaxChunk || sl > MaxChunk {
			return nil, ErrCorrupt
		}
		c.Stored = make([]byte, sl)
		if err := r.read(c.Stored); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		r.count++
		r.raw += uint64(c.RawLen)
		return c, nil
	case "ENDS":
		var e [16]byte
		if err := r.read(e[:]); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		want := r.h.Sum(nil)
		var got [32]byte
		if _, err := io.ReadFull(r.br, got[:]); err != nil {
			return nil, fmt.Errorf("%w: missing checksum", ErrCorrupt)
		}
		if binary.LittleEndian.Uint64(e[0:]) != r.count || binary.LittleEndian.Uint64(e[8:]) != r.raw || !bytes.Equal(want, got[:]) {
			return nil, fmt.Errorf("%w: image checksum mismatch", ErrCorrupt)
		}
		r.done = true
		return nil, io.EOF
	}
	return nil, fmt.Errorf("%w: bad record tag", ErrCorrupt)
}
