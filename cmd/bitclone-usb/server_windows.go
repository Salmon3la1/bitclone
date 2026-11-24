//go:build windows

package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"bitclone/internal/engine"
)

//go:embed static
var static embed.FS

type uJob struct {
	ID      int       `json:"id"`
	Kind    string    `json:"kind"`  // download, write
	State   string    `json:"state"` // running, done, failed, cancelled
	Error   string    `json:"error,omitempty"`
	Summary string    `json:"summary"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
	p       *engine.Progress
	cancel  context.CancelFunc
}

type usbServer struct {
	token string
	host  string

	mu       sync.Mutex
	iso      *ISOInfo
	job      *uJob
	seq      int
	lastSeen time.Time
}

func runServer(port int, token string, _ bool) error {
	s := &usbServer{token: token, host: fmt.Sprintf("127.0.0.1:%d", port), lastSeen: time.Now().Add(60 * time.Second)}
	if isos := findLocalISOs(); len(isos) > 0 {
		s.iso = isos[0]
	}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/api/status", s.auth(s.status))
	mux.HandleFunc("/api/isos", s.auth(s.isos))
	mux.HandleFunc("/api/disks", s.auth(s.disks))
	mux.HandleFunc("/api/iso/select", s.auth(s.selectISO))
	mux.HandleFunc("/api/iso/browse", s.auth(s.browseISO))
	mux.HandleFunc("/api/iso/download", s.auth(s.download))
	mux.HandleFunc("/api/write", s.auth(s.write))
	mux.HandleFunc("/api/cancel", s.auth(s.cancel))
	mux.HandleFunc("/api/quit", s.auth(s.quit))
	go s.watchdog()
	srv := &http.Server{Addr: s.host, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("bitclone-usb %s listening on %s", version, s.host)
	return srv.ListenAndServe()
}

// auth requires the per-launch token and the exact Host header, so other
// web pages (or DNS-rebinding tricks) can't drive a process that runs with
// administrator rights.
func (s *usbServer) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/api/status" && r.URL.Path != "/api/isos" && r.URL.Path != "/api/disks" && r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		s.lastSeen = time.Now()
		s.mu.Unlock()
		h(w, r)
	}
}

// watchdog exits once the window has been closed (no polling) and nothing is running.
func (s *usbServer) watchdog() {
	for range time.Tick(5 * time.Second) {
		s.mu.Lock()
		idle := time.Since(s.lastSeen) > 30*time.Second && (s.job == nil || s.job.State != "running")
		s.mu.Unlock()
		if idle {
			log.Println("window closed; exiting")
			os.Exit(0)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(v)
}

func (s *usbServer) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := map[string]any{"version": version, "repo": releaseRepo, "iso": s.iso, "admin": isAdmin()}
	if s.job != nil {
		j := *s.job
		resp["job"] = j
		resp["progress"] = s.job.p.Snapshot()
	}
	writeJSON(w, resp)
}

func (s *usbServer) isos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"isos": findLocalISOs()})
}

func (s *usbServer) disks(w http.ResponseWriter, r *http.Request) {
	d, err := listUSBDisks()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if d == nil {
		d = []USBDisk{}
	}
	writeJSON(w, map[string]any{"disks": d})
}

func (s *usbServer) setISO(w http.ResponseWriter, path string) {
	in, err := inspectISO(path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	s.iso = in
	s.mu.Unlock()
	writeJSON(w, map[string]any{"iso": in})
}

func (s *usbServer) selectISO(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		writeErr(w, 400, errors.New("path required"))
		return
	}
	s.setISO(w, req.Path)
}

func (s *usbServer) browseISO(w http.ResponseWriter, r *http.Request) {
	out, err := powershell(`Add-Type -AssemblyName System.Windows.Forms
$f = New-Object System.Windows.Forms.OpenFileDialog
$f.Title = 'Choose the bitclone boot image'
$f.Filter = 'Boot images (*.iso;*.img)|*.iso;*.img|All files (*.*)|*.*'
$o = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true }
if ($f.ShowDialog($o) -eq 'OK') { $f.FileName }`)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		writeJSON(w, map[string]any{"cancelled": true})
		return
	}
	s.setISO(w, p)
}

func (s *usbServer) start(w http.ResponseWriter, kind, summary string, run func(ctx context.Context, p *engine.Progress) error) {
	s.mu.Lock()
	if s.job != nil && s.job.State == "running" {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("something is already running"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.seq++
	j := &uJob{ID: s.seq, Kind: kind, State: "running", Summary: summary, Started: time.Now(), p: &engine.Progress{}, cancel: cancel}
	j.p.OnLog = func(l string) { log.Println(l) }
	s.job = j
	s.mu.Unlock()
	go func() {
		err := run(ctx, j.p)
		s.mu.Lock()
		defer s.mu.Unlock()
		j.Ended = time.Now()
		switch {
		case err == nil:
			j.State = "done"
		case errors.Is(err, context.Canceled):
			j.State, j.Error = "cancelled", "cancelled"
		default:
			j.State, j.Error = "failed", err.Error()
		}
		if err != nil {
			j.p.Logf("%s: %s", j.State, j.Error)
		}
		cancel()
	}()
	writeJSON(w, map[string]any{"id": j.ID})
}

func (s *usbServer) download(w http.ResponseWriter, r *http.Request) {
	if releaseRepo == "" {
		writeErr(w, 400, errors.New("this build has no download location; choose an ISO file instead"))
		return
	}
	url := "https://github.com/" + releaseRepo + "/releases/latest/download/bitclone-amd64.iso"
	s.start(w, "download", "Downloading the latest boot image", func(ctx context.Context, p *engine.Progress) error {
		path, err := downloadISO(ctx, url, p)
		if err != nil {
			return err
		}
		in, err := inspectISO(path)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.iso = in
		s.mu.Unlock()
		p.Logf("saved to %s", path)
		return nil
	})
}

func (s *usbServer) write(w http.ResponseWriter, r *http.Request) {
	var req USBDisk
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	iso := s.iso
	s.mu.Unlock()
	if iso == nil {
		writeErr(w, 400, errors.New("choose a boot image first"))
		return
	}
	// Re-read the disk list: the request must match a current USB disk.
	disks, err := listUSBDisks()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var target *USBDisk
	for i := range disks {
		if disks[i].Number == req.Number && disks[i].Size == req.Size && disks[i].Model == req.Model {
			target = &disks[i]
		}
	}
	if target == nil {
		writeErr(w, 400, errors.New("that USB drive is no longer connected; refresh the list"))
		return
	}
	onDisk := func(path string) bool {
		if len(path) < 2 || path[1] != ':' {
			return false
		}
		for _, l := range target.Letters {
			if strings.EqualFold(l, path[:1]) {
				return true
			}
		}
		return false
	}
	if onDisk(iso.Path) {
		writeErr(w, 400, errors.New("the boot image is stored on this USB drive; copy it to your PC first"))
		return
	}
	if exe, err := os.Executable(); err == nil && onDisk(exe) {
		writeErr(w, 400, errors.New("this program is running from that USB drive; copy it to your PC first"))
		return
	}
	d := *target
	s.start(w, "write", fmt.Sprintf("Writing %s to %s", iso.Name, d.Model), func(ctx context.Context, p *engine.Progress) error {
		p.Start("Preparing USB stick", 0)
		p.Logf("target: disk %d, %s, %s", d.Number, d.Model, engine.HumanBytes(d.Size))
		if err := prepareDisk(d); err != nil {
			return fmt.Errorf("preparing USB stick: %w", err)
		}
		for _, h := range lockVolumes(d.Letters) {
			defer closeHandle(h)
		}
		dev, err := openPhysical(d.Number)
		if err != nil {
			return err
		}
		defer dev.Close()
		src, err := os.Open(iso.Path)
		if err != nil {
			return err
		}
		defer src.Close()
		if err := writeImage(ctx, src, iso.Size, dev, d.Size, dev.Sync, p); err != nil {
			return err
		}
		rescan(dev)
		p.Logf("USB stick is ready")
		return nil
	})
}

func (s *usbServer) cancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.job != nil && s.job.State == "running" {
		s.job.cancel()
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *usbServer) quit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	busy := s.job != nil && s.job.State == "running"
	s.mu.Unlock()
	if busy {
		writeErr(w, 409, errors.New("still working"))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	go func() { time.Sleep(300 * time.Millisecond); os.Exit(0) }()
}
