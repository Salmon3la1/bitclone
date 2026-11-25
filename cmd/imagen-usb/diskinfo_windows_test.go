//go:build windows

package main

import (
	"os"
	"strings"
	"testing"
)

// The Windows drive must be found and matched to a physical disk through
// its volume extents, without administrator rights.
func TestLetterVolumesFindsSystemDrive(t *testing.T) {
	sys := strings.ToUpper(os.Getenv("SystemDrive"))
	if len(sys) < 1 {
		t.Skip("no SystemDrive")
	}
	for _, v := range letterVolumes() {
		if v.Letter == sys[:1] {
			if len(v.Disks) == 0 {
				t.Fatalf("%s: no disk found through volume extents", v.Letter)
			}
			if d := describeDisk(v.Disks[0]); d.Name == "" {
				t.Logf("%s: storage descriptor gave no name (allowed on virtual disks)", v.Letter)
			}
			return
		}
	}
	t.Fatalf("system drive %s not listed", sys)
}

func TestCleanText(t *testing.T) {
	if got := cleanText([]byte("  SanDisk \x01 Cruzer  ")); got != "SanDisk Cruzer" {
		t.Fatalf("got %q", got)
	}
}
