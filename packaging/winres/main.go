// Command winres writes the Windows resources of orosdrive.exe and
// "orOS Drive.exe" (version information, application manifest and icon)
// as a COFF object (.syso) that `go build` links into the executable.
//
// Windows Explorer shows these details in the file's Properties, and
// antivirus heuristics count an executable without them as suspicious.
// The output depends only on the flags, so release builds stay
// reproducible. Standard library only, no third-party tools.
//
// Usage (packaging/package.sh runs it before each Windows build):
//
//	go run ./packaging/winres -arch amd64 -version 0.3.3 \
//	    -name "orOS Drive.exe" -desc "orOS Drive" \
//	    -ico packaging/windows/oros-drive.ico \
//	    -out cmd/orosdrive/rsrc_windows_amd64.syso
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Resource types and the language every resource is stored under
// (English, United States; the version block names the same).
const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24
	langEnUS    = 0x0409
	codepageU16 = 1200
)

// Info is what goes into the version block.
type Info struct {
	Version     string // "1.2.3"; the file and product version
	Name        string // OriginalFilename and InternalName
	Description string // FileDescription, shown by Task Manager
}

func main() {
	arch := flag.String("arch", "amd64", "amd64 or arm64")
	version := flag.String("version", "", "version like 0.3.3")
	name := flag.String("name", "", "executable file name, like orosdrive.exe")
	desc := flag.String("desc", "orOS Drive", "file description")
	ico := flag.String("ico", "", ".ico file to embed as the application icon")
	out := flag.String("out", "", "the .syso file to write")
	flag.Parse()
	if *version == "" || *name == "" || *ico == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	icon, err := os.ReadFile(*ico)
	if err == nil {
		var obj []byte
		obj, err = Build(*arch, Info{*version, *name, *desc}, icon)
		if err == nil {
			err = os.WriteFile(*out, obj, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
}

// Build returns the COFF object holding the resources.
func Build(arch string, info Info, ico []byte) ([]byte, error) {
	m, ok := map[string]struct{ machine, reloc uint16 }{
		"amd64": {0x8664, 3}, // IMAGE_REL_AMD64_ADDR32NB
		"arm64": {0xaa64, 2}, // IMAGE_REL_ARM64_ADDR32NB
	}[arch]
	if !ok {
		return nil, fmt.Errorf("unknown arch %q", arch)
	}
	ver, err := VersionInfo(info)
	if err != nil {
		return nil, err
	}
	images, group, err := Icons(ico)
	if err != nil {
		return nil, err
	}
	res := map[uint32]map[uint32][]byte{
		rtVersion:   {1: ver},
		rtManifest:  {1: []byte(Manifest(info))},
		rtGroupIcon: {1: group},
		rtIcon:      {},
	}
	for i, img := range images {
		res[rtIcon][uint32(i+1)] = img
	}
	sec, relocs := section(res)
	return coff(m.machine, m.reloc, sec, relocs), nil
}

// section lays out the resource tree (type, id, language) followed by
// the data. It returns the section and the offsets of the data entries'
// OffsetToData fields, which need an image-relative relocation.
func section(res map[uint32]map[uint32][]byte) ([]byte, []uint32) {
	types := keys(res)
	var dirs []byte
	var data bytes.Buffer
	var relocs []uint32
	var fix []func(base uint32) // patch data offsets once size is known
	put := func(v ...uint32) {
		for _, x := range v {
			dirs = binary.LittleEndian.AppendUint32(dirs, x)
		}
	}
	dir := func(n int) {
		put(0, 0, 0)                                             // characteristics, time, version
		dirs = binary.LittleEndian.AppendUint16(dirs, 0)         // named entries
		dirs = binary.LittleEndian.AppendUint16(dirs, uint16(n)) // id entries
	}
	const sub = 0x80000000
	// Level 1: types. Level 2 and 3 directories follow in order.
	dir(len(types))
	l1 := len(dirs)
	for range types {
		put(0, 0)
	}
	for i, t := range types {
		binary.LittleEndian.PutUint32(dirs[l1+8*i:], t)
		binary.LittleEndian.PutUint32(dirs[l1+8*i+4:], sub|uint32(len(dirs)))
		ids := keys(res[t])
		dir(len(ids))
		l2 := len(dirs)
		for range ids {
			put(0, 0)
		}
		for j, id := range ids {
			binary.LittleEndian.PutUint32(dirs[l2+8*j:], id)
			binary.LittleEndian.PutUint32(dirs[l2+8*j+4:], sub|uint32(len(dirs)))
			dir(1)
			put(langEnUS, uint32(len(dirs)+8))
			entry := len(dirs)
			blob := res[t][id]
			at := uint32(data.Len())
			data.Write(blob)
			for data.Len()%8 != 0 {
				data.WriteByte(0)
			}
			put(0, uint32(len(blob)), 0, 0)
			relocs = append(relocs, uint32(entry))
			fix = append(fix, func(base uint32) {
				binary.LittleEndian.PutUint32(dirs[entry:], base+at)
			})
		}
	}
	base := uint32(len(dirs))
	for _, f := range fix {
		f(base)
	}
	return append(dirs, data.Bytes()...), relocs
}

func keys[V any](m map[uint32]V) []uint32 {
	k := make([]uint32, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Slice(k, func(i, j int) bool { return k[i] < k[j] })
	return k
}

// coff wraps the .rsrc section in an object file: header, one section,
// its relocations, a symbol for the section and an empty string table.
func coff(machine, relocType uint16, sec []byte, relocs []uint32) []byte {
	const hdr, shdr = 20, 40
	rawAt := uint32(hdr + shdr)
	relAt := rawAt + uint32(len(sec))
	symAt := relAt + uint32(10*len(relocs))
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	// IMAGE_FILE_HEADER
	w(machine)
	w(uint16(1)) // sections
	w(uint32(0)) // time stamp: none, for reproducible builds
	w(symAt)
	w(uint32(1)) // symbols
	w(uint16(0)) // optional header size
	w(uint16(0)) // characteristics
	// IMAGE_SECTION_HEADER
	b.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0)) // virtual size
	w(uint32(0)) // virtual address
	w(uint32(len(sec)))
	w(rawAt)
	w(relAt)
	w(uint32(0)) // line numbers
	w(uint16(len(relocs)))
	w(uint16(0))
	w(uint32(0x40000040)) // initialized data, readable
	b.Write(sec)
	for _, r := range relocs {
		w(r)         // offset in the section
		w(uint32(0)) // symbol 0: the section itself
		w(relocType)
	}
	// Symbol ".rsrc": value 0, section 1, type 0, class static, no aux.
	b.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0))
	w(uint16(1))
	w(uint16(0))
	w(uint8(3))
	w(uint8(0))
	w(uint32(4)) // string table: just its size
	return b.Bytes()
}

// Icons splits an .ico file into its images (RT_ICON, ids 1..n) and the
// group directory that points at them (RT_GROUP_ICON).
func Icons(ico []byte) ([][]byte, []byte, error) {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil, nil, errors.New("not an .ico file")
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	if n == 0 || len(ico) < 6+16*n {
		return nil, nil, errors.New("bad .ico directory")
	}
	group := append([]byte(nil), ico[:6]...)
	var images [][]byte
	for i := 0; i < n; i++ {
		e := ico[6+16*i : 22+16*i]
		size := binary.LittleEndian.Uint32(e[8:])
		off := binary.LittleEndian.Uint32(e[12:])
		if uint64(off)+uint64(size) > uint64(len(ico)) {
			return nil, nil, fmt.Errorf("icon image %d is outside the file", i+1)
		}
		images = append(images, ico[off:off+size])
		group = append(group, e[:4]...)                              // width, height, colours, reserved
		group = binary.LittleEndian.AppendUint16(group, 1)           // planes
		group = append(group, e[6:8]...)                             // bit count
		group = binary.LittleEndian.AppendUint32(group, size)        // bytes
		group = binary.LittleEndian.AppendUint16(group, uint16(i+1)) // RT_ICON id
	}
	return images, group, nil
}

// Manifest is the application manifest: run as the user (no elevation
// prompt) and declare the Windows versions the program is tested on.
func Manifest(info Info) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="koulaxizis.orOSDrive" version="` + fourPart(info.Version) + `"/>
  <description>` + info.Description + `</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
</assembly>
`
}

// fourPart turns "0.3.3" into "0.3.3.0".
func fourPart(v string) string {
	p, _ := parts(v)
	return fmt.Sprintf("%d.%d.%d.%d", p[0], p[1], p[2], p[3])
}

// parts reads the numbers of a version. A pre-release or build suffix
// ("0.3.0-ada4faf", CI builds) is left out of the numbers; the version
// strings keep it.
func parts(v string) ([4]uint16, error) {
	var p [4]uint16
	num, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "+")
	num, _, _ = strings.Cut(num, "-")
	fs := strings.Split(num, ".")
	if len(fs) < 1 || len(fs) > 4 {
		return p, fmt.Errorf("bad version %q", v)
	}
	for i, f := range fs {
		n, err := strconv.ParseUint(f, 10, 16)
		if err != nil {
			return p, fmt.Errorf("bad version %q", v)
		}
		p[i] = uint16(n)
	}
	return p, nil
}

// VersionInfo builds the VS_VERSIONINFO block.
func VersionInfo(info Info) ([]byte, error) {
	p, err := parts(info.Version)
	if err != nil {
		return nil, err
	}
	ms := uint32(p[0])<<16 | uint32(p[1])
	ls := uint32(p[2])<<16 | uint32(p[3])
	var fixed bytes.Buffer
	for _, v := range []uint32{
		0xfeef04bd, 0x00010000, // signature, structure version
		ms, ls, ms, ls, // file and product version
		0x3f, 0, // flags mask, flags
		0x00040004, // VOS_NT_WINDOWS32
		1, 0,       // VFT_APP
		0, 0, // date
	} {
		_ = binary.Write(&fixed, binary.LittleEndian, v)
	}
	strs := [][2]string{
		{"CompanyName", "orOS"},
		{"FileDescription", info.Description},
		{"FileVersion", info.Version},
		{"InternalName", strings.TrimSuffix(info.Name, ".exe")},
		{"LegalCopyright", "Copyright (c) 2026 Christos Koulaxizis. MIT License."},
		{"OriginalFilename", info.Name},
		{"ProductName", "orOS Drive"},
		{"ProductVersion", info.Version},
		{"Comments", "Free and open source: https://github.com/koulaxizis/oros-drive"},
	}
	var table []byte
	for _, s := range strs {
		v := u16z(s[1])
		table = append(table, node(s[0], v, uint16(len(v)/2), 1, nil)...)
	}
	tr := binary.LittleEndian.AppendUint16(nil, langEnUS)
	tr = binary.LittleEndian.AppendUint16(tr, codepageU16)
	children := append(
		node("StringFileInfo", nil, 0, 1, node("040904B0", nil, 0, 1, table)),
		node("VarFileInfo", nil, 0, 1, node("Translation", tr, uint16(len(tr)), 0, nil))...)
	return node("VS_VERSION_INFO", fixed.Bytes(), uint16(fixed.Len()), 0, children), nil
}

// node writes one version-resource structure: wLength, wValueLength,
// wType, the key, padding, the value, padding, the children. Every part
// starts on a 4-byte boundary; children are already padded.
func node(key string, value []byte, valueLen, typ uint16, children []byte) []byte {
	b := make([]byte, 6)
	b = append(b, u16z(key)...)
	b = pad4(b)
	b = append(b, value...)
	if len(children) > 0 {
		b = pad4(b)
		b = append(b, children...)
	}
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	binary.LittleEndian.PutUint16(b[2:], valueLen)
	binary.LittleEndian.PutUint16(b[4:], typ)
	return pad4(b)
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// u16z is s in UTF-16LE with a terminating zero.
func u16z(s string) []byte {
	var b []byte
	for _, r := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, r)
	}
	return append(b, 0, 0)
}
