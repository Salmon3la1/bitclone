//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bitclone-usb is a Windows program. On Linux/macOS write the ISO with: sudo dd if=bitclone-amd64.iso of=/dev/sdX bs=4M conv=fsync")
	os.Exit(1)
}
