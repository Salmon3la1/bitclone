//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// USBDisk is a disk bitclone-usb may write to.
type USBDisk struct {
	Number   int      `json:"number"`
	Model    string   `json:"model"`
	Serial   string   `json:"serial"`
	Size     int64    `json:"size"`
	Bus      string   `json:"bus"`
	IsBoot   bool     `json:"isBoot"`
	IsSystem bool     `json:"isSystem"`
	Style    string   `json:"style"`
	Sector   int      `json:"sector"`
	Letters  []string `json:"letters"`
	Labels   []string `json:"labels"`
	Large    bool     `json:"large"` // probably an external hard drive, not a stick
}

const listScript = `
$ErrorActionPreference = 'SilentlyContinue'
$l = Get-Disk | ForEach-Object {
  $d = $_
  $parts = @(Get-Partition -DiskNumber $d.Number | Where-Object { $_.DriveLetter -and $_.DriveLetter -ne [char]0 })
  [pscustomobject]@{
    Number = $d.Number; Model = [string]$d.FriendlyName; Serial = ([string]$d.SerialNumber).Trim(); Size = $d.Size
    Bus = [string]$d.BusType; IsBoot = $d.IsBoot; IsSystem = $d.IsSystem; Style = [string]$d.PartitionStyle
    Sector = $d.LogicalSectorSize
    Letters = @($parts | ForEach-Object { [string]$_.DriveLetter })
    Labels = @($parts | ForEach-Object { [string](Get-Volume -DriveLetter $_.DriveLetter).FileSystemLabel })
  }
}
ConvertTo-Json -InputObject @($l) -Depth 3 -Compress
`

func eligibleBus(b string) bool {
	switch strings.ToUpper(b) {
	case "USB", "SD", "MMC":
		return true
	}
	return false
}

// listUSBDisks returns removable USB/SD disks only. Internal, boot and
// system disks are never returned.
func listUSBDisks() ([]USBDisk, error) {
	out, err := powershell(listScript)
	if err != nil {
		return nil, fmt.Errorf("listing disks: %w", err)
	}
	var all []USBDisk
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("listing disks: %w", err)
	}
	var usb []USBDisk
	for _, d := range all {
		if !eligibleBus(d.Bus) || d.IsBoot || d.IsSystem || d.Size <= 0 {
			continue
		}
		d.Large = d.Size > 256e9
		usb = append(usb, d)
	}
	return usb, nil
}

// prepareDisk re-checks that disk number n is still the disk the user
// picked, then removes all its partitions so Windows releases its volumes.
func prepareDisk(want USBDisk) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$d = Get-Disk -Number %d
if ($d.IsBoot -or $d.IsSystem) { throw 'refusing: this is a system disk' }
if (@('USB','SD','MMC') -notcontains [string]$d.BusType) { throw 'refusing: not a USB/SD disk' }
if ($d.Size -ne %d -or [string]$d.FriendlyName -ne '%s') { throw 'the selected drive changed (was it unplugged?); refresh and pick it again' }
if ($d.IsReadOnly) { Set-Disk -Number %d -IsReadOnly $false }
if ($d.IsOffline) { Set-Disk -Number %d -IsOffline $false }
if ([string]$d.PartitionStyle -ne 'RAW') { Clear-Disk -Number %d -RemoveData -RemoveOEM -Confirm:$false }
`, want.Number, want.Size, strings.ReplaceAll(want.Model, "'", "''"), want.Number, want.Number, want.Number)
	_, err := powershell(script)
	return err
}

const (
	fsctlLockVolume           = 0x00090018
	fsctlDismountVolume       = 0x00090020
	ioctlDiskUpdateProperties = 0x00070140
)

func ioctl(h syscall.Handle, code uint32) error {
	var n uint32
	return syscall.DeviceIoControl(h, code, nil, 0, nil, 0, &n, nil)
}

// lockVolumes locks and dismounts any volumes still mounted with a drive
// letter; the handles are returned so the locks stay held while writing.
func lockVolumes(letters []string) []syscall.Handle {
	var hs []syscall.Handle
	for _, l := range letters {
		p, _ := syscall.UTF16PtrFromString(`\\.\` + l + `:`)
		h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		ioctl(h, fsctlLockVolume)
		ioctl(h, fsctlDismountVolume)
		hs = append(hs, h)
	}
	return hs
}

// openPhysical opens \\.\PhysicalDriveN for raw read/write.
func openPhysical(n int) (*os.File, error) {
	name := fmt.Sprintf(`\\.\PhysicalDrive%d`, n)
	p, _ := syscall.UTF16PtrFromString(name)
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return nil, errors.New("access denied to the USB stick (is another program using it?)")
		}
		return nil, fmt.Errorf("opening %s: %w", name, err)
	}
	return os.NewFile(uintptr(h), name), nil
}

// rescan tells Windows to re-read the stick's new partition table.
func rescan(f *os.File) {
	ioctl(syscall.Handle(f.Fd()), ioctlDiskUpdateProperties)
}

func closeHandle(h syscall.Handle) { syscall.CloseHandle(h) }
