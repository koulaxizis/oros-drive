package app

import (
	"testing"

	"github.com/koulaxizis/oros-drive/mirror"
)

func TestSyncText(t *testing.T) {
	if got := SyncText(nil, false); got != "Checking files…" {
		t.Fatal(got)
	}
	p := &mirror.Progress{Files: 22600, FilesDone: 120, Bytes: 43 << 30, BytesDone: 1288490188}
	if got := SyncText(p, false); got != "Uploading 120 of 22600 (1.2 GB of 43 GB)" {
		t.Fatal(got)
	}
	if got := SyncText(p, true); got != "Ανεβαίνουν 120 από 22600 (1,2 GB από 43 GB)" {
		t.Fatal(got)
	}
	if got := sizeText(512); got != "512 B" {
		t.Fatal(got)
	}
}
