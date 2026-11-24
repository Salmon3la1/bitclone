package winaccttest

import (
	"os"
	"testing"
)

// IMAGEN_DEMO_VOLUME=dir writes the fake Windows volume there (for demos).
func TestWriteDemoVolume(t *testing.T) {
	dir := os.Getenv("IMAGEN_DEMO_VOLUME")
	if dir == "" {
		t.Skip("set IMAGEN_DEMO_VOLUME")
	}
	if err := WriteVolume(dir); err != nil {
		t.Fatal(err)
	}
}
