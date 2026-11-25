package engine

import (
	"imagen/internal/disk"
	"imagen/internal/fs"
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

// PartitionSpace reports how much of a partition's filesystem is used and
// its total size, or -1 for both when that cannot be told. A mounted
// filesystem is asked directly; otherwise its allocation bitmap is read
// (NTFS, ext2/3/4, FAT), without mounting it.
func PartitionSpace(p disk.PartInfo) (used, total int64) {
	for _, mp := range p.Mountpoints {
		if mp == "[swap]" {
			continue
		}
		if free, tot, ok := disk.FreeSpace(mp); ok {
			return tot - free, tot
		}
	}
	dev, err := disk.Open(p.Path)
	if err != nil {
		return -1, -1
	}
	defer dev.Close()
	r := fs.Detect(dev, dev.Size)
	if r == nil || r.Used == nil {
		return -1, -1
	}
	total = r.FSSize
	if total <= 0 {
		total = dev.Size
	}
	return r.UsedBytes(), total
}
