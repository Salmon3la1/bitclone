package engine

import (
	"bitclone/internal/disk"
	"bitclone/internal/fs"
)

// DescribeDisks lists disks and identifies the filesystem of each partition.
func DescribeDisks() ([]disk.Info, error) {
	disks, err := disk.List()
	if err != nil {
		return nil, err
	}
	for i := range disks {
		for j := range disks[i].Partitions {
			p := &disks[i].Partitions[j]
			dev, err := disk.Open(p.Path)
			if err != nil {
				continue
			}
			if r := fs.Identify(dev, dev.Size); r != nil {
				p.FS, p.Label, p.UUID = r.FS, r.Label, r.UUID
			}
			dev.Close()
		}
	}
	return disks, nil
}

// FindDisk returns the info for a disk path, or nil.
func FindDisk(path string) *disk.Info {
	disks, err := disk.List()
	if err != nil {
		return nil
	}
	for i := range disks {
		if disks[i].Path == path {
			return &disks[i]
		}
	}
	return nil
}
