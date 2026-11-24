package engine

import (
	"errors"
	"os"

	"imagen/internal/disk"
	"imagen/internal/winacct"
)

// WindowsInstall is a Windows installation found on a partition.
type WindowsInstall struct {
	Disk      string `json:"disk"`
	DiskModel string `json:"diskModel"`
	Partition string `json:"partition"`
	Size      int64  `json:"size"`
	Label     string `json:"label,omitempty"`
	*winacct.Install
	Error string `json:"error,omitempty"` // the partition could not be read
}

// WindowsAccounts looks at every NTFS partition on every disk (except the
// boot USB), mounts it read-only if needed, and lists the user accounts of
// each Windows installation it finds. BitLocker volumes are reported as
// unreadable rather than skipped, so the user knows they exist.
func WindowsAccounts() ([]WindowsInstall, error) {
	disks, err := DescribeDisks()
	if err != nil {
		return nil, err
	}
	out := []WindowsInstall{}
	for _, d := range disks {
		if d.BootMedia {
			continue
		}
		for _, p := range d.Partitions {
			wi := WindowsInstall{Disk: d.Path, DiskModel: d.Model, Partition: p.Path, Size: p.Size, Label: p.Label}
			switch p.FS {
			case "bitlocker":
				wi.Error = "This partition is encrypted with BitLocker, so its accounts can't be read."
				out = append(out, wi)
				continue
			case "ntfs":
			default:
				continue
			}
			mp, done, err := disk.MountRO(p.Path)
			if err != nil {
				wi.Error = err.Error()
				out = append(out, wi)
				continue
			}
			in, err := winacct.Scan(os.DirFS(mp))
			done()
			switch {
			case errors.Is(err, winacct.ErrNoWindows):
				continue // a data partition
			case err != nil:
				wi.Error = err.Error()
			default:
				wi.Install = in
			}
			out = append(out, wi)
		}
	}
	return out, nil
}

// WindowsAccountsAt scans a folder that holds a Windows volume's files (a
// mounted partition, or a copy of Windows\System32\config).
func WindowsAccountsAt(dir string) (*winacct.Install, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, errors.New("not a folder: " + dir)
	}
	return winacct.Scan(os.DirFS(dir))
}
