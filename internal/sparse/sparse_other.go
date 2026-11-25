//go:build !windows

// Package sparse marks files as sparse, so ranges that are never written
// take no disk space.
package sparse

import "os"

// Set is a no-op: Unix filesystems leave unwritten ranges as holes anyway.
func Set(f *os.File) error { return nil }
