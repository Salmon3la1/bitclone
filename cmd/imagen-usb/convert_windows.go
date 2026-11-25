//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"imagen/internal/engine"
	"imagen/internal/image"
)

// Converting a .bci backup to a plain .img disk image, for programs that
// read raw images (data recovery tools, virtual machines).

var procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")

type bciInfo struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	FileSize int64     `json:"fileSize"`
	Parts    int       `json:"parts"`
	Mode     string    `json:"mode"`
	Created  time.Time `json:"created"`
	Model    string    `json:"model"`
	DiskSize int64     `json:"diskSize"`
	Data     int64     `json:"data"`
	Out      string    `json:"out"`
	Exists   bool      `json:"exists"`
	OutFS    string    `json:"outFS"`
	OutFree  int64     `json:"outFree"`
}

func inspectBCI(path, out string) (*bciInfo, error) {
	m, err := engine.ReadMeta(path)
	if err != nil {
		return nil, fmt.Errorf("%s is not an IMAGen image: %w", filepath.Base(path), err)
	}
	parts := image.Parts(path)
	in := &bciInfo{Path: path, Name: filepath.Base(path), FileSize: image.TotalSize(path), Parts: len(parts),
		Mode: m.Mode, Created: m.Created, Model: m.Source.Model, DiskSize: m.Source.Size, Data: m.DataBytes, Out: out}
	if in.Out == "" {
		in.Out = strings.TrimSuffix(path, filepath.Ext(path)) + ".img"
	}
	_, err = os.Stat(in.Out)
	in.Exists = err == nil
	in.OutFS, in.OutFree = volumeOf(filepath.Dir(in.Out))
	return in, nil
}

// volumeOf returns the filesystem name ("NTFS", "FAT32", "exFAT") and free
// bytes of the drive holding dir.
func volumeOf(dir string) (fsName string, free int64) {
	d, _ := syscall.UTF16PtrFromString(dir + `\`)
	var avail, total, totalFree uint64
	if r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree))); r != 0 {
		free = int64(avail)
	}
	root, _ := syscall.UTF16PtrFromString(filepath.VolumeName(dir) + `\`)
	name := make([]uint16, 64)
	if r, _, _ := procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(root)), 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&name[0])), uintptr(len(name))); r != 0 {
		fsName = syscall.UTF16ToString(name)
	}
	return
}

// fileDialog shows a Windows Open or Save dialog and returns the chosen path ("" if cancelled).
func fileDialog(save bool, title, filter, initial string) (string, error) {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	kind := "OpenFileDialog"
	extra := ""
	if save {
		kind = "SaveFileDialog"
		extra = "$f.OverwritePrompt = $false\n"
	}
	if initial != "" {
		extra += "$f.InitialDirectory = " + q(filepath.Dir(initial)) + "\n$f.FileName = " + q(filepath.Base(initial)) + "\n"
	}
	out, err := powershell(`Add-Type -AssemblyName System.Windows.Forms
$f = New-Object System.Windows.Forms.` + kind + `
$f.Title = ` + q(title) + `
$f.Filter = ` + q(filter) + `
` + extra + `$o = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true }
if ($f.ShowDialog($o) -eq 'OK') { $f.FileName }`)
	return strings.TrimSpace(string(out)), err
}

func (s *usbServer) bciBrowse(w http.ResponseWriter, r *http.Request) {
	p, err := fileDialog(false, "Choose an IMAGen backup image", "IMAGen images (*.bci)|*.bci|All files (*.*)|*.*", "")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if p == "" {
		writeJSON(w, map[string]any{"cancelled": true})
		return
	}
	s.sendBCI(w, p, "")
}

func (s *usbServer) bciSaveAs(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path, Out string }
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		writeErr(w, 400, errors.New("choose an image first"))
		return
	}
	p, err := fileDialog(true, "Save the disk image as", "Disk images (*.img)|*.img|All files (*.*)|*.*", req.Out)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if p == "" {
		writeJSON(w, map[string]any{"cancelled": true})
		return
	}
	s.sendBCI(w, req.Path, p)
}

func (s *usbServer) sendBCI(w http.ResponseWriter, path, out string) {
	in, err := inspectBCI(path, out)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"bci": in})
}

func (s *usbServer) convert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path, Out string
		Overwrite bool
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" || req.Out == "" {
		writeErr(w, 400, errors.New("choose an image and where to save it"))
		return
	}
	if strings.EqualFold(filepath.Clean(req.Path), filepath.Clean(req.Out)) {
		writeErr(w, 400, errors.New("choose a different file name for the .img"))
		return
	}
	if _, err := os.Stat(req.Out); err == nil && !req.Overwrite {
		writeErr(w, 409, fmt.Errorf("%s already exists", filepath.Base(req.Out)))
		return
	}
	s.start(w, "convert", "Converting "+filepath.Base(req.Path), func(ctx context.Context, p *engine.Progress) error {
		_, err := engine.Export(ctx, req.Path, req.Out, p)
		return err
	})
}
