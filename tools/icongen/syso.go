package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"sort"
)

// A minimal Windows resource compiler: writes a COFF object (.syso) with a
// .rsrc section that the Go linker places into the .exe. It holds the icon
// (RT_ICON + RT_GROUP_ICON) and an application manifest (RT_MANIFEST).

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtManifest  = 24
	langEnUS    = 0x0409
)

const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="ImaGen.USBCreator" version="1.0.0.0" processorArchitecture="amd64"/>
  <description>ImaGen USB Creator</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{35138b9a-5d96-4fbd-8e2d-a2440225f93a}"/>
      <supportedOS Id="{4a2f28e3-53b9-4441-ba9c-d69d4a4a6e38}"/>
      <supportedOS Id="{1f676c76-80e1-4239-95bb-83d0f6d0da78}"/>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true</dpiAware>
      <longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>
    </windowsSettings>
  </application>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
</assembly>
`

type resource struct {
	typ, id uint16
	data    []byte
}

// writeSyso writes an amd64 COFF object holding the given resources.
func writeSyso(path string, sizes []int, pngs [][]byte) error {
	var res []resource
	var group bytes.Buffer
	binary.Write(&group, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	for i, p := range pngs {
		id := uint16(i + 1)
		res = append(res, resource{rtIcon, id, p})
		dim := uint8(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		group.Write([]byte{dim, dim, 0, 0})
		binary.Write(&group, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&group, binary.LittleEndian, uint32(len(p)))
		binary.Write(&group, binary.LittleEndian, id)
	}
	res = append(res, resource{rtGroupIcon, 1, group.Bytes()})
	res = append(res, resource{rtManifest, 1, []byte(manifest)})
	sec, relocs := buildRsrc(res)

	const fileHdr, secHdr = 20, 40
	secOff := fileHdr + secHdr
	relOff := secOff + len(sec)
	symOff := relOff + 10*len(relocs)

	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	// IMAGE_FILE_HEADER
	le(uint16(0x8664)) // AMD64
	le(uint16(1))      // sections
	le(uint32(0))      // timestamp
	le(uint32(symOff))
	le(uint32(1)) // symbols
	le(uint16(0)) // optional header size
	le(uint16(0)) // characteristics
	// IMAGE_SECTION_HEADER
	b.WriteString(".rsrc\x00\x00\x00")
	le(uint32(0)) // virtual size
	le(uint32(0)) // virtual address
	le(uint32(len(sec)))
	le(uint32(secOff))
	le(uint32(relOff))
	le(uint32(0)) // line numbers
	le(uint16(len(relocs)))
	le(uint16(0))
	le(uint32(0x40000040)) // INITIALIZED_DATA | MEM_READ
	b.Write(sec)
	for _, r := range relocs {
		le(uint32(r))
		le(uint32(0)) // symbol 0 (.rsrc)
		le(uint16(3)) // IMAGE_REL_AMD64_ADDR32NB
	}
	// Symbol table: .rsrc, section 1, static.
	b.WriteString(".rsrc\x00\x00\x00")
	le(uint32(0))
	le(uint16(1))
	le(uint16(0))
	le(uint8(3))
	le(uint8(0))
	le(uint32(4)) // empty string table
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// buildRsrc lays out the three-level resource directory (type, id,
// language), the data entries and the data. It returns the section bytes and
// the offsets of the data-entry RVA fields that need relocating.
func buildRsrc(res []resource) ([]byte, []int) {
	byType := map[uint16][]resource{}
	var types []uint16
	for _, r := range res {
		if _, ok := byType[r.typ]; !ok {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], r)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	for _, t := range types {
		rs := byType[t]
		sort.Slice(rs, func(i, j int) bool { return rs[i].id < rs[j].id })
	}

	dirSize := func(n int) int { return 16 + 8*n }
	// Offsets: root, then one dir per type, then one dir per resource
	// (language level), then data entries, then data.
	off := dirSize(len(types))
	typeDir := map[uint16]int{}
	for _, t := range types {
		typeDir[t] = off
		off += dirSize(len(byType[t]))
	}
	var order []resource
	for _, t := range types {
		order = append(order, byType[t]...)
	}
	langDir := make([]int, len(order))
	for i := range order {
		langDir[i] = off
		off += dirSize(1)
	}
	entry := make([]int, len(order))
	for i := range order {
		entry[i] = off
		off += 16
	}
	data := make([]int, len(order))
	for i, r := range order {
		off = (off + 7) &^ 7
		data[i] = off
		off += len(r.data)
	}

	sec := make([]byte, (off+7)&^7)
	put16 := func(o int, v uint16) { binary.LittleEndian.PutUint16(sec[o:], v) }
	put32 := func(o int, v uint32) { binary.LittleEndian.PutUint32(sec[o:], v) }
	dir := func(o, n int) { put16(o+14, uint16(n)) } // id entries only
	const sub = 0x80000000

	dir(0, len(types))
	for i, t := range types {
		put32(16+8*i, uint32(t))
		put32(16+8*i+4, sub|uint32(typeDir[t]))
	}
	k := 0
	for _, t := range types {
		rs := byType[t]
		dir(typeDir[t], len(rs))
		for i, r := range rs {
			put32(typeDir[t]+16+8*i, uint32(r.id))
			put32(typeDir[t]+16+8*i+4, sub|uint32(langDir[k]))
			k++
		}
	}
	var relocs []int
	for i, r := range order {
		dir(langDir[i], 1)
		put32(langDir[i]+16, langEnUS)
		put32(langDir[i]+20, uint32(entry[i])) // leaf: no high bit
		put32(entry[i], uint32(data[i]))       // RVA, relocated by the linker
		put32(entry[i]+4, uint32(len(r.data)))
		relocs = append(relocs, entry[i])
		copy(sec[data[i]:], r.data)
	}
	return sec, relocs
}
