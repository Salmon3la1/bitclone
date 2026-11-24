package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"imagen/internal/engine"
)

// ISOInfo describes a boot image on disk.
type ISOInfo struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Label    string `json:"label"`
	Hybrid   bool   `json:"hybrid"` // has an MBR, so it boots when written to a USB stick
	Imagen   bool   `json:"imagen"` // volume label is IMAGEN
	Modified string `json:"modified"`
}

func inspectISO(path string) (*ISOInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	in := &ISOInfo{Path: path, Name: filepath.Base(path), Size: st.Size(), Modified: st.ModTime().Format("2006-01-02 15:04")}
	head := make([]byte, 0x8000+2048)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, errors.New("file is too small to be a boot image")
	}
	in.Hybrid = head[510] == 0x55 && head[511] == 0xAA
	if string(head[0x8001:0x8006]) == "CD001" {
		in.Label = strings.TrimSpace(string(head[0x8028:0x8048]))
	}
	in.Imagen = strings.EqualFold(in.Label, "IMAGEN")
	if !in.Hybrid {
		return in, errors.New("this image has no boot record for USB sticks (not an ISO-hybrid image)")
	}
	return in, nil
}

func cacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	d = filepath.Join(d, "imagen-usb")
	os.MkdirAll(d, 0o755)
	return d
}

// findLocalISOs looks for imagen*.iso next to the program, in the
// current folder, in Downloads, in a dist folder and in the download cache.
func findLocalISOs() []*ISOInfo {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "dist"))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd, filepath.Join(wd, "dist"))
	}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, "Downloads"))
	}
	dirs = append(dirs, cacheDir())
	seen := map[string]bool{}
	var out []*ISOInfo
	for _, d := range dirs {
		m, _ := filepath.Glob(filepath.Join(d, "imagen*.iso"))
		for _, p := range m {
			ap, _ := filepath.Abs(p)
			if seen[strings.ToLower(ap)] {
				continue
			}
			seen[strings.ToLower(ap)] = true
			if in, err := inspectISO(ap); err == nil {
				out = append(out, in)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out
}

// downloadISO fetches url into the cache folder, checking url+".sha256" when
// the release provides it.
func downloadISO(ctx context.Context, url string, p *engine.Progress) (string, error) {
	client := &http.Client{Timeout: 0}
	wantSum := ""
	if req, err := http.NewRequestWithContext(ctx, "GET", url+".sha256", nil); err == nil {
		if r, err := client.Do(req); err == nil {
			if r.StatusCode == 200 {
				line, _ := bufio.NewReader(io.LimitReader(r.Body, 1024)).ReadString('\n')
				if f := strings.Fields(line); len(f) > 0 && len(f[0]) == 64 {
					wantSum = strings.ToLower(f[0])
				}
			}
			r.Body.Close()
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	r, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return "", fmt.Errorf("download failed: %s (has a release been published?)", r.Status)
	}
	dest := filepath.Join(cacheDir(), "imagen-amd64.iso")
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	p.Start("Downloading boot image", r.ContentLength)
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := r.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return "", err
			}
			h.Write(buf[:n])
			p.Add(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return "", fmt.Errorf("download interrupted: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if wantSum != "" && got != wantSum {
		os.Remove(tmp)
		return "", errors.New("downloaded file is damaged (checksum mismatch); try again")
	}
	if wantSum != "" {
		p.Logf("checksum OK (%s…)", got[:16])
	}
	os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	now := time.Now()
	os.Chtimes(dest, now, now)
	return dest, nil
}
