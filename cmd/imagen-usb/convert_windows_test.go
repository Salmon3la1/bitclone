//go:build windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"imagen/internal/engine"
)

func TestInspectAndConvertBCI(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "disk.raw")
	data := bytes.Repeat([]byte("IMAGen test data "), 1<<19) // ~8.5 MB
	data = data[:8<<20]
	copy(data[(4<<20):], make([]byte, 1<<20)) // an all-zero stretch
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "pc.bci")
	if err := engine.Backup(context.Background(), engine.BackupOptions{Source: src, Dest: img, Mode: engine.ModeFull, Level: 1}, &engine.Progress{}); err != nil {
		t.Fatal(err)
	}
	in, err := inspectBCI(img, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.Out != filepath.Join(dir, "pc.img") || in.Exists || in.DiskSize != 8<<20 || in.Mode != "full" || in.OutFS == "" || in.OutFree <= 0 {
		t.Fatalf("unexpected info: %+v", in)
	}
	if _, err := engine.Export(context.Background(), img, in.Out, &engine.Progress{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(in.Out)
	if !bytes.Equal(got, data) {
		t.Fatal("converted image differs from the source")
	}
	if in2, _ := inspectBCI(img, ""); !in2.Exists {
		t.Fatal("existing .img not reported")
	}
	if _, err := inspectBCI(src, ""); err == nil {
		t.Fatal("a non-IMAGen file was accepted")
	}
}
