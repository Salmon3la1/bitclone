// Package extent provides byte-range helpers used to describe which parts of
// a disk must be copied.
package extent

import "sort"

// Extent is a half-open byte range [Off, Off+Len).
type Extent struct {
	Off int64 `json:"off"`
	Len int64 `json:"len"`
}

// End returns the first byte after the extent.
func (e Extent) End() int64 { return e.Off + e.Len }

// Normalize sorts the list, drops empty extents and merges overlapping or
// adjacent ones. Extents separated by a gap of at most maxGap bytes are also
// merged (the gap is then copied too, which trades a little space for far
// fewer seeks on spinning disks).
func Normalize(list []Extent, maxGap int64) []Extent {
	out := make([]Extent, 0, len(list))
	for _, e := range list {
		if e.Len > 0 {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Off < out[j].Off })
	res := out[:0]
	for _, e := range out {
		if n := len(res); n > 0 && e.Off <= res[n-1].End()+maxGap {
			if e.End() > res[n-1].End() {
				res[n-1].Len = e.End() - res[n-1].Off
			}
			continue
		}
		res = append(res, e)
	}
	return res
}

// Clamp trims every extent to [lo, hi).
func Clamp(list []Extent, lo, hi int64) []Extent {
	out := make([]Extent, 0, len(list))
	for _, e := range list {
		s, t := e.Off, e.End()
		if s < lo {
			s = lo
		}
		if t > hi {
			t = hi
		}
		if t > s {
			out = append(out, Extent{s, t - s})
		}
	}
	return out
}

// Shift adds delta to every offset.
func Shift(list []Extent, delta int64) []Extent {
	out := make([]Extent, len(list))
	for i, e := range list {
		out[i] = Extent{e.Off + delta, e.Len}
	}
	return out
}

// Total returns the summed length. The list should be normalized first.
func Total(list []Extent) int64 {
	var t int64
	for _, e := range list {
		t += e.Len
	}
	return t
}

// Complement returns the ranges of [lo, hi) not covered by list.
func Complement(list []Extent, lo, hi int64) []Extent {
	list = Normalize(Clamp(list, lo, hi), 0)
	var out []Extent
	pos := lo
	for _, e := range list {
		if e.Off > pos {
			out = append(out, Extent{pos, e.Off - pos})
		}
		pos = e.End()
	}
	if pos < hi {
		out = append(out, Extent{pos, hi - pos})
	}
	return out
}

// FromBitmap converts an allocation bitmap (LSB-first bit order, as used by
// NTFS and ext*) into extents. Bit i covers [base+i*unit, base+(i+1)*unit).
// Bits beyond the end of bm are treated as "used" so that a short bitmap can
// never cause data to be skipped.
func FromBitmap(bm []byte, nbits, unit, base int64) []Extent {
	var out []Extent
	runStart := int64(-1)
	flush := func(i int64) {
		if runStart >= 0 {
			out = append(out, Extent{base + runStart*unit, (i - runStart) * unit})
			runStart = -1
		}
	}
	for i := int64(0); i < nbits; {
		bi := i >> 3
		if bi >= int64(len(bm)) {
			if runStart < 0 {
				runStart = i
			}
			i = nbits
			break
		}
		b := bm[bi]
		if i&7 == 0 && i+8 <= nbits {
			if b == 0 {
				flush(i)
				i += 8
				continue
			}
			if b == 0xFF {
				if runStart < 0 {
					runStart = i
				}
				i += 8
				continue
			}
		}
		if (b>>(uint(i)&7))&1 == 1 {
			if runStart < 0 {
				runStart = i
			}
		} else {
			flush(i)
		}
		i++
	}
	flush(nbits)
	return out
}
