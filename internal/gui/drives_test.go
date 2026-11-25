package gui

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestFindImages(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"old.bci",              // top of the drive (saved before the IMAGen folder)
		"imagen/pc.bci",        // IMAGen folder, any capitalization
		"imagen/pc.bci.001",    // later part of a split image: not listed
		"imagen/Laptop/lt.BCI", // one folder below
		"imagen/a/b/deep.bci",  // too deep
		"Photos/holiday.bci",   // other folders are not searched
		"imagen/notes.txt",
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := findImages(root)
	sort.Strings(got)
	want := []string{"imagen/Laptop/lt.BCI", "imagen/pc.bci", "old.bci"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
