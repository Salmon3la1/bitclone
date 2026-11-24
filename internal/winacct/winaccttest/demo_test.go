package winaccttest

import (
	"os"
	"testing"
)

// BITCLONE_DEMO_VOLUME=dir writes the fake Windows volume there (for demos).
func TestWriteDemoVolume(t *testing.T) {
	dir := os.Getenv("BITCLONE_DEMO_VOLUME")
	if dir == "" {
		t.Skip("set BITCLONE_DEMO_VOLUME")
	}
	if err := WriteVolume(dir); err != nil {
		t.Fatal(err)
	}
}
