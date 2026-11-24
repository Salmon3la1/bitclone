//go:build windows

// Command imagen-usb creates an ImaGen boot USB stick on Windows.
//
// Double-click it: it asks for administrator rights (needed to write raw
// disks), then opens its window. It can download the latest boot image from
// the project's GitHub releases or use a local .iso, and writes it to a USB
// stick with read-back verification.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Set at build time: -ldflags "-X main.releaseRepo=owner/repo -X main.version=v0.1.0"
var (
	releaseRepo = ""
	version     = "dev"
)

func main() {
	serve := flag.Bool("serve", false, "run the elevated backend (internal)")
	port := flag.Int("port", 0, "")
	token := flag.String("token", "", "")
	flag.Parse()

	if lf, err := os.OpenFile(filepath.Join(cacheDir(), "imagen-usb.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(lf)
	}

	if *serve {
		if err := runServer(*port, *token, false); err != nil {
			log.Println("server:", err)
		}
		return
	}

	tok := randomToken()
	p, err := freePort()
	if err != nil {
		messageBox("ImaGen USB", "Could not start: "+err.Error())
		return
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", p, tok)

	if isAdmin() {
		go func() {
			if waitReady(p, 15*time.Second) {
				openBrowser(url)
			}
		}()
		if err := runServer(p, tok, true); err != nil {
			messageBox("ImaGen USB", "Could not start: "+err.Error())
		}
		return
	}
	exe, _ := os.Executable()
	if err := runElevated(exe, fmt.Sprintf("-serve -port %d -token %s", p, tok)); err != nil {
		messageBox("ImaGen USB", "Writing a USB stick needs administrator rights.\n\n"+err.Error())
		return
	}
	if !waitReady(p, 60*time.Second) {
		messageBox("ImaGen USB", "The program did not start. See "+filepath.Join(cacheDir(), "imagen-usb.log"))
		return
	}
	openBrowser(url)
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitReady(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/hello", port)); err == nil {
			r.Body.Close()
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}
