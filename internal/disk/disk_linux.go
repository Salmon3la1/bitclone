//go:build linux

package disk

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func deviceSize(f *os.File) (int64, error) { return seekSize(f) }

func sysRead(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sysInt(p string) int64 {
	v, _ := strconv.ParseInt(sysRead(p), 10, 64)
	return v
}

// Mounts maps real device paths to their mountpoints (from /proc/self/mounts),
// and includes active swap devices with mountpoint "[swap]".
func Mounts() map[string][]string {
	out := map[string][]string{}
	if f, err := os.Open("/proc/self/mounts"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 2 || !strings.HasPrefix(fl[0], "/dev/") {
				continue
			}
			dev := fl[0]
			if r, err := filepath.EvalSymlinks(dev); err == nil {
				dev = r
			}
			mp := strings.ReplaceAll(fl[1], `\040`, " ")
			out[dev] = append(out[dev], mp)
		}
		f.Close()
	}
	if b, err := os.ReadFile("/proc/swaps"); err == nil {
		for i, line := range strings.Split(string(b), "\n") {
			fl := strings.Fields(line)
			if i == 0 || len(fl) == 0 {
				continue
			}
			out[fl[0]] = append(out[fl[0]], "[swap]")
		}
	}
	return out
}

func udevProps(dev string) map[string]string {
	out := map[string]string{}
	b, err := exec.Command("udevadm", "info", "--query=property", "--name="+dev).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// List enumerates physical disks.
func List() ([]Info, error) {
	ents, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	mounts := Mounts()
	boot, _ := DiskOfPath("/run/live/medium")
	var out []Info
	for _, e := range ents {
		name := e.Name()
		if skipDev(name) {
			continue
		}
		base := "/sys/block/" + name
		size := sysInt(base+"/size") * 512
		if size == 0 {
			continue
		}
		in := Info{
			Path:       "/dev/" + name,
			Name:       name,
			Size:       size,
			SectorSize: int(sysInt(base + "/queue/logical_block_size")),
			Model:      strings.TrimSpace(sysRead(base+"/device/vendor") + " " + sysRead(base+"/device/model")),
			Serial:     sysRead(base + "/device/serial"),
			Removable:  sysRead(base+"/removable") == "1",
			ReadOnly:   sysRead(base+"/ro") == "1",
			BootMedia:  name == boot,
		}
		props := udevProps(in.Path)
		if v := props["ID_MODEL"]; v != "" {
			in.Model = strings.ReplaceAll(v, "_", " ")
		}
		if v := props["ID_SERIAL_SHORT"]; v != "" {
			in.Serial = v
		}
		in.Transport = props["ID_BUS"]
		if strings.HasPrefix(name, "nvme") {
			in.Transport = "nvme"
		}
		if in.SectorSize == 0 {
			in.SectorSize = 512
		}
		sub, _ := os.ReadDir(base)
		for _, s := range sub {
			pb := base + "/" + s.Name()
			if _, err := os.Stat(pb + "/partition"); err != nil {
				continue
			}
			p := PartInfo{
				Path:   "/dev/" + s.Name(),
				Name:   s.Name(),
				Number: int(sysInt(pb + "/partition")),
				Start:  sysInt(pb+"/start") * 512,
				Size:   sysInt(pb+"/size") * 512,
			}
			p.Mountpoints = mounts[p.Path]
			in.Partitions = append(in.Partitions, p)
		}
		sort.Slice(in.Partitions, func(i, j int) bool { return in.Partitions[i].Number < in.Partitions[j].Number })
		out = append(out, in)
	}
	return out, nil
}

func skipDev(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "sr", "fd", "nbd", "md", "mmcblk0boot", "mmcblk1boot"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// DiskOfPath returns the kernel name of the whole disk holding path (a file,
// directory or block device), or "" if it is not on a physical disk.
func DiskOfPath(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	dev := uint64(st.Dev)
	if st.Mode&syscall.S_IFMT == syscall.S_IFBLK {
		dev = uint64(st.Rdev)
	}
	maj := (dev>>8)&0xfff | (dev>>32)&^0xfff
	min := dev&0xff | (dev>>12)&^0xff
	real, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/block/%d:%d", maj, min))
	if err != nil {
		return "", nil
	}
	if _, err := os.Stat(real + "/partition"); err == nil {
		return filepath.Base(filepath.Dir(real)), nil
	}
	return filepath.Base(real), nil
}

// SectorSize returns the logical sector size of a block device, or 512 for files.
func SectorSize(path string) int {
	st, err := os.Stat(path)
	if err != nil || st.Mode()&os.ModeDevice == 0 {
		return 512
	}
	name, _ := DiskOfPath(path)
	if v := sysInt("/sys/block/" + name + "/queue/logical_block_size"); v > 0 {
		return int(v)
	}
	return 512
}

// IsBlockDevice reports whether path is a block device.
func IsBlockDevice(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode()&os.ModeDevice != 0 && st.Mode()&os.ModeCharDevice == 0
}

// CheckWritable refuses to write to the boot medium or to a disk with any
// mounted partition or active swap.
func CheckWritable(path string) error {
	if !IsBlockDevice(path) {
		return nil
	}
	name, err := DiskOfPath(path)
	if err != nil {
		return err
	}
	if boot, _ := DiskOfPath("/run/live/medium"); boot != "" && boot == name {
		return fmt.Errorf("%s is the USB stick IMAGen booted from", path)
	}
	for dev, mps := range Mounts() {
		if d, _ := DiskOfPath(dev); d == name {
			return fmt.Errorf("%s is in use: %s is mounted at %s (unmount it first)", path, dev, strings.Join(mps, ", "))
		}
	}
	return nil
}

// SameDisk reports whether two paths live on the same physical disk. At least
// one of them must be a block device: two ordinary files (disk image files used
// as source or target) never collide, even on the same filesystem.
func SameDisk(a, b string) bool {
	if !IsBlockDevice(a) && !IsBlockDevice(b) {
		return false
	}
	da, _ := DiskOfPath(a)
	db, _ := DiskOfPath(b)
	return da != "" && da == db
}

// Reread asks the kernel to re-read the partition table.
func Reread(path string) {
	if !IsBlockDevice(path) {
		return
	}
	exec.Command("blockdev", "--rereadpt", path).Run()
	exec.Command("udevadm", "settle").Run()
}

// Mount mounts a partition under /mnt/imagen/<name> and returns the mountpoint.
func Mount(dev string) (string, error) {
	mp := "/mnt/imagen/" + filepath.Base(dev)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	out, err := exec.Command("mount", dev, mp).CombinedOutput()
	if err != nil {
		// Some kernels need the NTFS driver named explicitly.
		if out2, err2 := exec.Command("mount", "-t", "ntfs3", dev, mp).CombinedOutput(); err2 == nil {
			return mp, nil
		} else {
			out = append(out, out2...)
		}
		return "", fmt.Errorf("mount %s: %s", dev, strings.TrimSpace(string(out)))
	}
	return mp, nil
}

// MountRO gives read-only access to a partition's files. If the partition is
// already mounted, its existing mountpoint is returned. Otherwise it is
// mounted read-only under /mnt/imagen/.ro-<name> (hidden from the file
// browser); call the returned function to unmount it again. Nothing is
// written to the partition: no journal replay, no hibernation-file removal.
func MountRO(dev string) (string, func(), error) {
	if r, err := filepath.EvalSymlinks(dev); err == nil {
		dev = r
	}
	if mps := Mounts()[dev]; len(mps) > 0 && mps[0] != "[swap]" {
		return mps[0], func() {}, nil
	}
	mp := "/mnt/imagen/.ro-" + filepath.Base(dev)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", nil, err
	}
	var msgs []string
	for _, args := range [][]string{
		{"-o", "ro", dev, mp},
		{"-t", "ntfs3", "-o", "ro", dev, mp},
		{"-t", "ntfs-3g", "-o", "ro", dev, mp},
	} {
		out, err := exec.Command("mount", args...).CombinedOutput()
		if err == nil {
			return mp, func() {
				exec.Command("umount", mp).Run()
				os.Remove(mp)
			}, nil
		}
		msgs = append(msgs, strings.TrimSpace(string(out)))
	}
	os.Remove(mp)
	return "", nil, fmt.Errorf("could not open %s read-only: %s", dev, strings.Join(msgs, "; "))
}

// Unmount unmounts a mountpoint.
func Unmount(mp string) error {
	exec.Command("sync").Run()
	if out, err := exec.Command("umount", mp).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %s", mp, strings.TrimSpace(string(out)))
	}
	return nil
}

// Power runs "poweroff" or "reboot".
func Power(action string) error {
	exec.Command("sync").Run()
	return exec.Command("systemctl", action).Run()
}
