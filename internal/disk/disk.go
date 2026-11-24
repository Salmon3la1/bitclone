// Package disk opens raw disks (or disk image files) and, on Linux,
// enumerates block devices and performs safety checks before writing.
package disk

import (
	"io"
	"os"
)

// Info describes a whole disk.
type Info struct {
	Path       string     `json:"path"` // /dev/sda
	Name       string     `json:"name"` // sda
	Size       int64      `json:"size"`
	SectorSize int        `json:"sectorSize"`
	Model      string     `json:"model,omitempty"`
	Serial     string     `json:"serial,omitempty"`
	Transport  string     `json:"transport,omitempty"` // sata, usb, nvme...
	Removable  bool       `json:"removable"`
	ReadOnly   bool       `json:"readOnly"`
	BootMedia  bool       `json:"bootMedia"` // the USB stick we booted from
	Partitions []PartInfo `json:"partitions"`
}

// PartInfo is a partition as the kernel sees it.
type PartInfo struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Number      int      `json:"number"`
	Start       int64    `json:"start"`
	Size        int64    `json:"size"`
	Mountpoints []string `json:"mountpoints,omitempty"`
	FS          string   `json:"fs,omitempty"`
	Label       string   `json:"label,omitempty"`
	UUID        string   `json:"uuid,omitempty"`
}

// Device is an open disk or image file.
type Device struct {
	*os.File
	Size int64
}

// Open opens a disk read-only.
func Open(path string) (*Device, error) { return open(path, os.O_RDONLY) }

// OpenWrite opens a disk read-write.
func OpenWrite(path string) (*Device, error) { return open(path, os.O_RDWR) }

func open(path string, flag int) (*Device, error) {
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, err
	}
	size, err := deviceSize(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Device{File: f, Size: size}, nil
}

func seekSize(f *os.File) (int64, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = f.Seek(0, io.SeekStart)
	return size, err
}
