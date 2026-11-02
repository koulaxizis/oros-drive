package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readIco(t *testing.T) []byte {
	t.Helper()
	ico, err := os.ReadFile("../windows/oros-drive.ico")
	if err != nil {
		t.Fatal(err)
	}
	return ico
}

// The same flags give the same bytes, so releases stay reproducible.
func TestDeterministic(t *testing.T) {
	ico := readIco(t)
	info := Info{"0.3.3", "orOS Drive.exe", "orOS Drive"}
	a, err := Build("amd64", info, ico)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Build("amd64", info, ico)
	if !bytes.Equal(a, b) {
		t.Fatal("two builds differ")
	}
}

func TestVersionParts(t *testing.T) {
	for in, want := range map[string][4]uint16{
		"0.3.3":         {0, 3, 3, 0},
		"v1.2.3":        {1, 2, 3, 0},
		"0.3.0-ada4faf": {0, 3, 0, 0},
		"1.2.3+build.7": {1, 2, 3, 0},
		"1.2.3.4":       {1, 2, 3, 4},
	} {
		got, err := parts(in)
		if err != nil || got != want {
			t.Errorf("parts(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1.x", "1.2.3.4.5", "-abc", "70000.0.0"} {
		if _, err := parts(in); err == nil {
			t.Errorf("parts(%q) accepted", in)
		}
	}
}

func TestBadInput(t *testing.T) {
	ico := readIco(t)
	if _, err := Build("386", Info{"1.0.0", "x.exe", "x"}, ico); err == nil {
		t.Error("unknown arch accepted")
	}
	if _, err := Build("amd64", Info{"1.x", "x.exe", "x"}, ico); err == nil {
		t.Error("bad version accepted")
	}
	if _, err := Build("amd64", Info{"1.0.0", "x.exe", "x"}, []byte("not an icon")); err == nil {
		t.Error("bad icon accepted")
	}
}

// Links the object into a real Windows program for each architecture and
// reads the resources back from the executable, the way Explorer does.
func TestLinkedIntoExe(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	ico := readIco(t)
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			dir := t.TempDir()
			obj, err := Build(arch, Info{"0.3.3", "orOS Drive.exe", "orOS Drive"}, ico)
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"go.mod":  "module hello\n\ngo 1.24\n",
				"main.go": "package main\n\nfunc main() {}\n",
			}
			for n, s := range files {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "rsrc_windows_"+arch+".syso"), obj, 0o644); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, "hello.exe")
			cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags", "-s -w -H=windowsgui", "-o", exe, ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+arch, "CGO_ENABLED=0", "GOFLAGS=")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go build: %v\n%s", err, out)
			}
			res := resources(t, exe)
			ver := res[[2]uint32{rtVersion, 1}]
			for _, want := range []string{"VS_VERSION_INFO", "orOS Drive", "0.3.3", "orOS Drive.exe", "Christos Koulaxizis"} {
				if !bytes.Contains(ver, u16z(want)[:len(u16z(want))-2]) {
					t.Errorf("version block lacks %q", want)
				}
			}
			if !bytes.Contains(ver, []byte{0xbd, 0x04, 0xef, 0xfe, 0, 0, 1, 0, 3, 0, 0, 0, 0, 0, 3, 0}) {
				t.Error("fixed file info lacks signature or version 0.3.3.0")
			}
			man := string(res[[2]uint32{rtManifest, 1}])
			if !strings.Contains(man, `level="asInvoker"`) || !strings.Contains(man, `version="0.3.3.0"`) {
				t.Errorf("manifest: %s", man)
			}
			group := res[[2]uint32{rtGroupIcon, 1}]
			n := int(binary.LittleEndian.Uint16(group[4:]))
			if n != int(binary.LittleEndian.Uint16(ico[4:])) || len(group) != 6+14*n {
				t.Fatalf("group icon has %d images, %d bytes", n, len(group))
			}
			for i := 1; i <= n; i++ {
				img := res[[2]uint32{rtIcon, uint32(i)}]
				want := binary.LittleEndian.Uint32(group[6+14*(i-1)+8:])
				if uint32(len(img)) != want || !bytes.Contains(ico, img) {
					t.Errorf("icon %d: %d bytes, group says %d", i, len(img), want)
				}
			}
		})
	}
}

// resources walks the resource tree of a PE file and returns the data of
// every (type, id) leaf.
func resources(t *testing.T, path string) map[[2]uint32][]byte {
	t.Helper()
	f, err := pe.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var dd pe.DataDirectory
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		dd = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
	case *pe.OptionalHeader32:
		dd = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
	}
	if dd.VirtualAddress == 0 {
		t.Fatal("no resource directory in the executable")
	}
	var sec *pe.Section
	for _, s := range f.Sections {
		if dd.VirtualAddress >= s.VirtualAddress && dd.VirtualAddress < s.VirtualAddress+s.VirtualSize {
			sec = s
		}
	}
	if sec == nil {
		t.Fatal("resource directory outside every section")
	}
	data, err := sec.Data()
	if err != nil {
		t.Fatal(err)
	}
	base := dd.VirtualAddress - sec.VirtualAddress
	u32 := func(off uint32) uint32 { return binary.LittleEndian.Uint32(data[base+off:]) }
	entries := func(dir uint32) [][2]uint32 {
		n := uint32(binary.LittleEndian.Uint16(data[base+dir+12:])) + uint32(binary.LittleEndian.Uint16(data[base+dir+14:]))
		var e [][2]uint32
		for i := uint32(0); i < n; i++ {
			e = append(e, [2]uint32{u32(dir + 16 + 8*i), u32(dir + 16 + 8*i + 4)})
		}
		return e
	}
	out := map[[2]uint32][]byte{}
	for _, typ := range entries(0) {
		for _, id := range entries(typ[1] &^ 0x80000000) {
			lang := entries(id[1] &^ 0x80000000)
			if len(lang) != 1 || lang[0][0] != langEnUS {
				t.Fatalf("type %d id %d: languages %v", typ[0], id[0], lang)
			}
			leaf := lang[0][1]
			rva, size := u32(leaf), u32(leaf+4)
			off := rva - sec.VirtualAddress
			out[[2]uint32{typ[0], id[0]}] = data[off : off+size]
		}
	}
	return out
}
