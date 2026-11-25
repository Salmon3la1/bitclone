//go:build windows

// Package sparse marks files as sparse, so ranges that are never written
// take no disk space.
package sparse

import (
	"os"
	"syscall"
)

const fsctlSetSparse = 0x000900C4

// Set marks f as a sparse file. NTFS and ReFS support it; on other
// filesystems (FAT32, exFAT) it fails and the file is simply written in full.
func Set(f *os.File) error {
	var n uint32
	return syscall.DeviceIoControl(syscall.Handle(f.Fd()), fsctlSetSparse, nil, 0, nil, 0, &n, nil)
}
