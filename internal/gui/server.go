// Package gui serves the web interface shown full-screen on the boot USB.
package gui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"imagen/internal/disk"
	"imagen/internal/engine"
	"imagen/internal/image"
)

//go:embed static
var static embed.FS

type job struct {
	ID       int64            `json:"id"`
	Kind     string           `json:"kind"`  // backup, restore, verify
	State    string           `json:"state"` // running, done, failed, cancelled
	Error    string           `json:"error,omitempty"`
	Started  time.Time        `json:"started"`
	Ended    time.Time        `json:"ended,omitempty"`
	Summary  string           `json:"summary"`
	Progress *engine.Progress `json:"-"`
	cancel   context.CancelFunc
}

type server struct {
	mu  sync.Mutex
	job *job
	seq int64
}

// Serve runs the HTTP server until it fails.
func Serve(addr string) error {
	s := &server{}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
	// Logged once, when the kiosk browser has loaded the page (the boot test
	// in CI waits for this line).
	var connected sync.Once
	mux.HandleFunc("/api/system", func(w http.ResponseWriter, r *http.Request) {
		connected.Do(func() { log.Printf("interface connected (%s)", r.UserAgent()) })
		s.system(w, r)
	})
	mux.HandleFunc("/api/disks", s.disks)
	mux.HandleFunc("/api/space", s.space)
	mux.HandleFunc("/api/images", s.images)
	mux.HandleFunc("/api/analyze", s.analyze)
	mux.HandleFunc("/api/browse", s.browse)
	mux.HandleFunc("/api/image", s.imageInfo)
	mux.HandleFunc("/api/job", s.jobStatus)
	mux.HandleFunc("/api/mkdir", post(s.mkdir))
	mux.HandleFunc("/api/mount", post(s.mount))
	mux.HandleFunc("/api/unmount", post(s.unmount))
	mux.HandleFunc("/api/backup", post(s.backup))
	mux.HandleFunc("/api/restore", post(s.restore))
	mux.HandleFunc("/api/verify", post(s.verify))
	mux.HandleFunc("/api/cancel", post(s.cancelJob))
	mux.HandleFunc("/api/power", post(s.power))
	mux.HandleFunc("/api/accounts", post(s.accounts))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

// post wraps a handler: POST only, and a custom header that cross-site pages
// cannot send without a CORS preflight (which we never grant).
func post(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Imagen") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
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
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func (s *server) system(w http.ResponseWriter, r *http.Request) {
	_, err := os.Stat("/run/live/medium")
	writeJSON(w, map[string]any{
		"version": engine.ToolVersion,
		"os":      runtime.GOOS,
		"live":    err == nil,
		"rawDisk": runtime.GOOS == "linux",
	})
}

func (s *server) disks(w http.ResponseWriter, r *http.Request) {
	d, err := engine.DescribeDisks()
	if err != nil {
		writeJSON(w, map[string]any{"disks": []any{}, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"disks": d})
}

func (s *server) analyze(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("disk")
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = engine.ModeUsed
	}
	plan, err := engine.Analyze(path, mode)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"plan": plan, "regions": len(plan.Extents)})
}

type entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Image bool   `json:"image"`
	Mod   string `json:"mod"`
}

func defaultDir() string {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/mnt/imagen"); err == nil {
			return "/mnt/imagen"
		}
		return "/mnt"
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func (s *server) browse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = defaultDir()
	}
	p = filepath.Clean(p)
	ents, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	out := []entry{}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		en := entry{Name: e.Name(), Path: filepath.Join(p, e.Name()), Dir: info.IsDir(), Mod: info.ModTime().Format("2006-01-02 15:04")}
		if !en.Dir {
			if !strings.HasSuffix(strings.ToLower(en.Name), ".bci") {
				continue
			}
			en.Image = true
			en.Size = image.TotalSize(en.Path)
		}
		out = append(out, en)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	writeJSON(w, map[string]any{"path": p, "parent": parent, "entries": out, "sep": string(filepath.Separator)})
}

func (s *server) imageInfo(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	m, err := engine.ReadMeta(p)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"meta": m, "fileSize": image.TotalSize(p), "files": len(image.Parts(p))})
}

func (s *server) mkdir(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		writeErr(w, 400, errors.New("bad request"))
		return
	}
	if err := os.MkdirAll(req.Path, 0o755); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) mount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string
		// ReadOnlyOK allows a read-only mount when a writable one fails
		// (enough for opening an image).
		ReadOnlyOK bool
	}
	if err := readJSON(r, &req); err != nil || !strings.HasPrefix(req.Device, "/dev/") {
		writeErr(w, 400, errors.New("bad device"))
		return
	}
	for _, mp := range disk.Mounts()[req.Device] {
		if mp != "[swap]" && !strings.Contains(mp, "/.ro-") {
			writeJSON(w, map[string]any{"mountpoint": mp})
			return
		}
	}
	var mp string
	var ro bool
	var err error
	if req.ReadOnlyOK {
		mp, ro, err = disk.MountForReading(req.Device)
	} else {
		mp, err = disk.Mount(req.Device)
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"mountpoint": mp, "readOnly": ro})
}

func (s *server) unmount(w http.ResponseWriter, r *http.Request) {
	var req struct{ Mountpoint string }
	if err := readJSON(r, &req); err != nil || req.Mountpoint == "" {
		writeErr(w, 400, errors.New("bad request"))
		return
	}
	if s.busy() {
		writeErr(w, 409, errors.New("a job is running"))
		return
	}
	if err := disk.Unmount(req.Mountpoint); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job != nil && s.job.State == "running"
}

func (s *server) start(w http.ResponseWriter, kind, summary string, run func(ctx context.Context, p *engine.Progress) error) {
	s.mu.Lock()
	if s.job != nil && s.job.State == "running" {
		s.mu.Unlock()
		writeErr(w, 409, errors.New("another job is already running"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.seq++
	j := &job{ID: s.seq, Kind: kind, State: "running", Started: time.Now(), Summary: summary, Progress: &engine.Progress{}, cancel: cancel}
	s.job = j
	s.mu.Unlock()
	go func() {
		err := run(ctx, j.Progress)
		s.mu.Lock()
		defer s.mu.Unlock()
		j.Ended = time.Now()
		switch {
		case err == nil:
			j.State = "done"
		case errors.Is(err, context.Canceled):
			j.State = "cancelled"
			j.Error = "cancelled by user"
		default:
			j.State = "failed"
			j.Error = err.Error()
		}
		if err != nil {
			j.Progress.Logf("%s: %s", j.State, j.Error)
		}
		cancel()
	}()
	writeJSON(w, map[string]any{"id": j.ID})
}

func (s *server) backup(w http.ResponseWriter, r *http.Request) {
	var o engine.BackupOptions
	if err := readJSON(r, &o); err != nil || o.Source == "" || o.Dest == "" {
		writeErr(w, 400, errors.New("source and destination are required"))
		return
	}
	if !strings.HasSuffix(strings.ToLower(o.Dest), ".bci") {
		o.Dest += ".bci"
	}
	if d := engine.FindDisk(o.Source); d != nil {
		o.Model, o.Serial = d.Model, d.Serial
	}
	s.start(w, "backup", "Backup of "+o.Source+" → "+o.Dest, func(ctx context.Context, p *engine.Progress) error {
		// The destination folder (normally the drive's IMAGen folder) is
		// created when the backup starts.
		if err := os.MkdirAll(filepath.Dir(o.Dest), 0o755); err != nil {
			return err
		}
		return engine.Backup(ctx, o, p)
	})
}

func (s *server) restore(w http.ResponseWriter, r *http.Request) {
	var o engine.RestoreOptions
	if err := readJSON(r, &o); err != nil || o.Image == "" || o.Target == "" {
		writeErr(w, 400, errors.New("image and target are required"))
		return
	}
	s.start(w, "restore", "Restore of "+o.Image+" → "+o.Target, func(ctx context.Context, p *engine.Progress) error {
		if err := releaseDisk(o.Target); err != nil {
			return fmt.Errorf("could not release the target disk: %w", err)
		}
		return engine.Restore(ctx, o, p)
	})
}

func (s *server) verify(w http.ResponseWriter, r *http.Request) {
	var req struct{ Image string }
	if err := readJSON(r, &req); err != nil || req.Image == "" {
		writeErr(w, 400, errors.New("image is required"))
		return
	}
	s.start(w, "verify", "Verify "+req.Image, func(ctx context.Context, p *engine.Progress) error {
		_, err := engine.Verify(ctx, req.Image, p)
		return err
	})
}

func (s *server) jobStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j := s.job
	var cp job
	if j != nil {
		cp = *j
	}
	s.mu.Unlock()
	if j == nil {
		writeJSON(w, map[string]any{"job": nil})
		return
	}
	writeJSON(w, map[string]any{"job": cp, "progress": j.Progress.Snapshot()})
}

func (s *server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.job != nil && s.job.State == "running" {
		s.job.cancel()
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

// accounts lists Windows user accounts. With a folder, it scans that folder;
// otherwise every NTFS partition (mounted read-only for the scan). POST
// because it mounts partitions; refused while a job runs so nothing gets
// mounted under a backup or restore.
func (s *server) accounts(w http.ResponseWriter, r *http.Request) {
	var req struct{ Folder string }
	readJSON(r, &req)
	if s.busy() {
		writeErr(w, 409, errors.New("a backup or restore is running; wait for it to finish"))
		return
	}
	if req.Folder != "" {
		in, err := engine.WindowsAccountsAt(filepath.Clean(req.Folder))
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, map[string]any{"installs": []engine.WindowsInstall{{Partition: req.Folder, Install: in}}})
		return
	}
	list, err := engine.WindowsAccounts()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"installs": list})
}

func (s *server) power(w http.ResponseWriter, r *http.Request) {
	var req struct{ Action string }
	readJSON(r, &req)
	if req.Action != "poweroff" && req.Action != "reboot" {
		writeErr(w, 400, errors.New("bad action"))
		return
	}
	if s.busy() {
		writeErr(w, 409, errors.New("a job is running; cancel it first"))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := disk.Power(req.Action); err != nil {
			log.Printf("%s failed: %v", req.Action, err)
		}
	}()
}
