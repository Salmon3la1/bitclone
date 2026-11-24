//go:build !linux

package disk

import (
	"errors"
	"os"
)

// On non-Linux systems bitclone works only with disk image files. This lets
// the engine be developed and tested on any OS; the bootable USB is Linux.

var errUnsupported = errors.New("raw disk access is only supported on Linux (the bootable USB); use image files here")

func deviceSize(f *os.File) (int64, error) { return seekSize(f) }

func List() ([]Info, error)                  { return nil, errUnsupported }
func Mounts() map[string][]string            { return map[string][]string{} }
func DiskOfPath(path string) (string, error) { return "", nil }
func SectorSize(path string) int             { return 512 }
func IsBlockDevice(path string) bool         { return false }
func CheckWritable(path string) error        { return nil }
func SameDisk(a, b string) bool              { return false }
func Reread(path string)                     {}
func Mount(dev string) (string, error)       { return "", errUnsupported }
func Unmount(mp string) error                { return errUnsupported }
func MountRO(dev string) (string, func(), error) {
	return "", nil, errUnsupported
}
func Power(action string) error { return errUnsupported }
