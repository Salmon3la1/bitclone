package gui

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"imagen/internal/disk"
	"imagen/internal/engine"
	"imagen/internal/image"
)

// FolderName is the folder IMAGen saves images in by default, at the top
// of the chosen drive.
const FolderName = "IMAGen"

type partSpace struct {
	Used  int64 `json:"used"`  // -1 when unknown
	Total int64 `json:"total"` // -1 when unknown
}

// space reports used and total bytes for every partition (except the boot
// stick's), so the interface can show free space and what a disk holds.
func (s *server) space(w http.ResponseWriter, r *http.Request) {
	out := map[string]partSpace{}
	disks, err := disk.List()
	if err != nil {
		writeJSON(w, map[string]any{"space": out})
		return
	}
	for _, d := range disks {
		if d.BootMedia {
			continue
		}
		for _, p := range d.Partitions {
			used, total := engine.PartitionSpace(p)
			out[p.Path] = partSpace{used, total}
		}
	}
	writeJSON(w, map[string]any{"space": out})
}

type foundImage struct {
	Device string      `json:"device"` // partition holding it
	Disk   string      `json:"disk"`   // that disk's model
	Label  string      `json:"label"`  // that partition's label
	Rel    string      `json:"rel"`    // path inside the partition, "/" separated
	Path   string      `json:"path"`   // full path when the partition is mounted; else empty
	Size   int64       `json:"size"`
	Meta   *image.Meta `json:"meta"`
}

// readableFS are the filesystems searched for images.
func readableFS(fs string) bool {
	return fs == "ntfs" || fs == "exfat" || strings.HasPrefix(fs, "fat") || strings.HasPrefix(fs, "ext")
}

// images searches every drive for .bci images: at the top of the drive, in
// its IMAGen folder, and one folder below that. Unmounted partitions are
// mounted read-only just long enough to look.
func (s *server) images(w http.ResponseWriter, r *http.Request) {
	found := []foundImage{}
	disks, err := engine.DescribeDisks()
	if err != nil {
		writeJSON(w, map[string]any{"images": found})
		return
	}
	for _, d := range disks {
		if d.BootMedia {
			continue
		}
		for _, p := range d.Partitions {
			if !readableFS(p.FS) {
				continue
			}
			mp, done, err := disk.MountRO(p.Path)
			if err != nil {
				continue
			}
			visible := !strings.Contains(mp, "/.ro-")
			for _, rel := range findImages(mp) {
				full := filepath.Join(mp, filepath.FromSlash(rel))
				m, err := engine.ReadMeta(full)
				if err != nil {
					continue
				}
				fi := foundImage{Device: p.Path, Disk: d.Model, Label: p.Label, Rel: rel, Size: image.TotalSize(full), Meta: m}
				if visible {
					fi.Path = full
				}
				found = append(found, fi)
			}
			done()
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Meta.Created.After(found[j].Meta.Created) })
	writeJSON(w, map[string]any{"images": found})
}

// findImages lists the .bci images (first file of a split image) at the
// top of root, in its IMAGen folder (any capitalization) and in that
// folder's subfolders. Paths are relative to root, "/" separated.
func findImages(root string) []string {
	var out []string
	look := func(rel string) []os.DirEntry {
		ents, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".bci") {
				out = append(out, path.Join(rel, e.Name()))
			}
		}
		return ents
	}
	for _, e := range look("") {
		if !e.IsDir() || !strings.EqualFold(e.Name(), FolderName) {
			continue
		}
		for _, sub := range look(e.Name()) {
			if sub.IsDir() && !strings.HasPrefix(sub.Name(), ".") {
				look(path.Join(e.Name(), sub.Name()))
			}
		}
	}
	return out
}

// releaseDisk unmounts every partition of a disk that is about to be
// overwritten. IMAGen mounts drives itself, so the user should not have
// to unmount them by hand first.
func releaseDisk(target string) error {
	d := engine.FindDisk(target)
	if d == nil {
		return nil
	}
	mounts := disk.Mounts()
	for _, p := range d.Partitions {
		for _, mp := range mounts[p.Path] {
			if mp == "[swap]" {
				continue
			}
			if err := disk.Unmount(mp); err != nil {
				return err
			}
		}
	}
	return nil
}
