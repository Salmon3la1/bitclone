// Command imagen images and restores whole disks.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"imagen/internal/engine"
	"imagen/internal/gui"
)

const usage = `imagen — sector-exact disk imaging

Usage:
  imagen list                              list disks
  imagen analyze [-mode used|full] DISK    show what a backup would copy
  imagen backup  -source DISK -out FILE.bci [-mode used|full] [-level 1] [-split 4000M] [-verify] [-force]
  imagen restore -image FILE.bci -target DISK [-yes] [-zero-fill] [-no-fix-gpt] [-verify-first]
  imagen verify  FILE.bci                  check image integrity
  imagen info    FILE.bci                  show image metadata
  imagen export  FILE.bci FILE.img         convert to a plain disk image (for recovery tools, VMs)
  imagen accounts [-dir FOLDER] [-json]   list Windows user accounts on all disks (read-only)
  imagen gui     [-listen 127.0.0.1:8080]  start the graphical interface

Modes:
  used  copy only blocks in use on NTFS, ext2/3/4 and FAT; other partitions,
        partition tables and boot areas are copied sector by sector (default)
  full  copy every sector of the disk

Either way the partition table, MBR disk signature, GPT disk/partition GUIDs
and filesystem serials/UUIDs are copied exactly, so the restored disk keeps
the source disk's identity.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "list":
		err = cmdList()
	case "analyze":
		err = cmdAnalyze(os.Args[2:])
	case "backup":
		err = cmdBackup(ctx, os.Args[2:])
	case "restore":
		err = cmdRestore(ctx, os.Args[2:])
	case "verify":
		err = cmdVerify(ctx, os.Args[2:])
	case "export":
		err = cmdExport(ctx, os.Args[2:])
	case "info":
		err = cmdInfo(os.Args[2:])
	case "accounts":
		err = cmdAccounts(os.Args[2:])
	case "gui":
		err = cmdGUI(os.Args[2:])
	case "ping":
		err = cmdPing(os.Args[2:])
	case "version":
		fmt.Println("IMAGen", engine.ToolVersion)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdList() error {
	disks, err := engine.DescribeDisks()
	if err != nil {
		return err
	}
	for _, d := range disks {
		flags := ""
		if d.BootMedia {
			flags += " [boot USB]"
		}
		if d.Removable {
			flags += " [removable]"
		}
		fmt.Printf("%-14s %10s  %s  S/N %s  %s%s\n", d.Path, engine.HumanBytes(d.Size), d.Model, d.Serial, d.Transport, flags)
		for _, p := range d.Partitions {
			mp := ""
			if len(p.Mountpoints) > 0 {
				mp = " mounted at " + strings.Join(p.Mountpoints, ",")
			}
			fmt.Printf("   %-14s %10s  %-8s %s%s\n", p.Path, engine.HumanBytes(p.Size), p.FS, p.Label, mp)
		}
	}
	return nil
}

func cmdAnalyze(args []string) error {
	fl := flag.NewFlagSet("analyze", flag.ExitOnError)
	mode := fl.String("mode", engine.ModeUsed, "used or full")
	fl.Parse(args)
	if fl.NArg() != 1 {
		return fmt.Errorf("analyze needs one disk")
	}
	plan, err := engine.Analyze(fl.Arg(0), *mode)
	if err != nil {
		return err
	}
	printPlan(plan)
	return nil
}

func printPlan(plan *engine.Plan) {
	fmt.Printf("Disk size: %s, sector size %d\n", engine.HumanBytes(plan.DiskSize), plan.SectorSize)
	if t := plan.Table; t != nil {
		fmt.Printf("Partition table: %s, disk ID %s, MBR signature %s\n", t.Scheme, t.DiskID, t.MBRSig)
	} else {
		fmt.Println("Partition table: none")
	}
	for _, p := range plan.Parts {
		fmt.Printf("  #%-3d %-22s %10s  %-6s %-16q  %s, %s", p.Index, p.TypeName, engine.HumanBytes(p.Size), p.FS, p.Label, p.Strategy, engine.HumanBytes(p.UsedBytes))
		if p.Note != "" {
			fmt.Printf("  (%s)", p.Note)
		}
		fmt.Println()
	}
	for _, w := range plan.Warnings {
		fmt.Println("warning:", w)
	}
	fmt.Printf("Data to copy: %s in %d region(s)\n", engine.HumanBytes(plan.DataBytes), len(plan.Extents))
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	mult := int64(1)
	for suf, m := range map[string]int64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40} {
		if strings.HasSuffix(s, suf) {
			mult = m
			s = strings.TrimSuffix(s, suf)
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v * mult, err
}

func cliProgress() (*engine.Progress, func()) {
	p := &engine.Progress{OnLog: func(l string) { fmt.Fprintln(os.Stderr, l) }}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				s := p.Snapshot()
				if s.Total > 0 {
					eta := "?"
					if s.ETA >= 0 {
						eta = (time.Duration(s.ETA) * time.Second).String()
					}
					fmt.Fprintf(os.Stderr, "\r%5.1f%%  %s / %s  %s/s  ETA %s   ", s.Percent,
						engine.HumanBytes(s.Done), engine.HumanBytes(s.Total), engine.HumanBytes(int64(s.Rate)), eta)
				}
			}
		}
	}()
	return p, func() { close(done); fmt.Fprintln(os.Stderr) }
}

func cmdBackup(ctx context.Context, args []string) error {
	fl := flag.NewFlagSet("backup", flag.ExitOnError)
	src := fl.String("source", "", "source disk (e.g. /dev/sda) or image file")
	out := fl.String("out", "", "output image path (.bci)")
	mode := fl.String("mode", engine.ModeUsed, "used or full")
	level := fl.Int("level", 1, "compression level 0-9 (0 = none)")
	split := fl.String("split", "0", "split image into files of this size, e.g. 4000M for FAT32")
	verify := fl.Bool("verify", false, "verify image after writing")
	force := fl.Bool("force", false, "overwrite an existing image")
	fl.Parse(args)
	if *src == "" || *out == "" {
		return fmt.Errorf("backup needs -source and -out")
	}
	sz, err := parseSize(*split)
	if err != nil {
		return fmt.Errorf("bad -split: %v", err)
	}
	o := engine.BackupOptions{Source: *src, Dest: *out, Mode: *mode, Level: *level, SplitSize: sz, Verify: *verify, Overwrite: *force}
	if d := engine.FindDisk(*src); d != nil {
		o.Model, o.Serial = d.Model, d.Serial
	}
	p, done := cliProgress()
	defer done()
	return engine.Backup(ctx, o, p)
}

func cmdRestore(ctx context.Context, args []string) error {
	fl := flag.NewFlagSet("restore", flag.ExitOnError)
	img := fl.String("image", "", "image file (.bci)")
	tgt := fl.String("target", "", "target disk (e.g. /dev/sdb) — ALL DATA ON IT WILL BE OVERWRITTEN")
	yes := fl.Bool("yes", false, "do not ask for confirmation")
	zero := fl.Bool("zero-fill", false, "write zeros to areas not stored in the image")
	noFix := fl.Bool("no-fix-gpt", false, "do not move the GPT backup header on a larger target")
	vf := fl.Bool("verify-first", false, "verify the whole image before writing")
	force := fl.Bool("force", false, "ignore sector size mismatch")
	fl.Parse(args)
	if *img == "" || *tgt == "" {
		return fmt.Errorf("restore needs -image and -target")
	}
	meta, err := engine.ReadMeta(*img)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Printf("Image of %s (%s, %s) taken %s\n", meta.Source.Model, meta.Source.Path, engine.HumanBytes(meta.Source.Size), meta.Created.Local().Format(time.RFC1123))
		fmt.Printf("ALL DATA ON %s WILL BE DESTROYED. Continue? [y/N] ", *tgt)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return fmt.Errorf("aborted")
		}
	}
	p, done := cliProgress()
	defer done()
	return engine.Restore(ctx, engine.RestoreOptions{Image: *img, Target: *tgt, FixGPT: !*noFix, ZeroFill: *zero, VerifyFirst: *vf, Force: *force}, p)
}

func cmdVerify(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("verify needs one image")
	}
	p, done := cliProgress()
	defer done()
	_, err := engine.Verify(ctx, args[0], p)
	return err
}

func cmdExport(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("export needs an image and the .img file to write")
	}
	if _, err := os.Stat(args[1]); err == nil {
		return fmt.Errorf("%s already exists", args[1])
	}
	p, done := cliProgress()
	defer done()
	_, err := engine.Export(ctx, args[0], args[1], p)
	return err
}

func cmdInfo(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("info needs one image")
	}
	m, err := engine.ReadMeta(args[0])
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(b))
	return nil
}

func cmdGUI(args []string) error {
	fl := flag.NewFlagSet("gui", flag.ExitOnError)
	listen := fl.String("listen", "127.0.0.1:8080", "address to serve the interface on")
	fl.Parse(args)
	fmt.Fprintf(os.Stderr, "IMAGen GUI on http://%s/\n", *listen)
	return gui.Serve(*listen)
}

// ping waits until the GUI backend answers (used by the kiosk service).
func cmdPing(args []string) error {
	fl := flag.NewFlagSet("ping", flag.ExitOnError)
	addr := fl.String("addr", "127.0.0.1:8080", "")
	timeout := fl.Duration("timeout", 20*time.Second, "")
	fl.Parse(args)
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if r, err := http.Get("http://" + *addr + "/api/ping"); err == nil {
			r.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("backend not reachable at %s", *addr)
}

// cmdAccounts lists Windows user accounts on every disk, or in one folder.
func cmdAccounts(args []string) error {
	fl := flag.NewFlagSet("accounts", flag.ExitOnError)
	dir := fl.String("dir", "", "scan this folder (a mounted Windows volume) instead of all disks")
	asJSON := fl.Bool("json", false, "print JSON")
	fl.Parse(args)
	var list []engine.WindowsInstall
	if *dir != "" {
		in, err := engine.WindowsAccountsAt(*dir)
		if err != nil {
			return err
		}
		list = []engine.WindowsInstall{{Partition: *dir, Install: in}}
	} else {
		var err error
		if list, err = engine.WindowsAccounts(); err != nil {
			return err
		}
	}
	if *asJSON {
		b, _ := json.MarshalIndent(list, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(list) == 0 {
		fmt.Println("No Windows installation found.")
	}
	kinds := map[string]string{"local": "local", "microsoft": "Microsoft", "azuread": "work/school", "domain": "domain"}
	for _, w := range list {
		if w.Error != "" {
			fmt.Printf("%s: %s\n\n", w.Partition, w.Error)
			continue
		}
		fmt.Printf("%s  %s %s (build %s)  PC name %s\n", w.Partition, w.Product, w.Version, w.Build, w.ComputerName)
		for _, m := range w.Warnings {
			fmt.Println("  note:", m)
		}
		for _, a := range w.Accounts {
			flags := []string{kinds[a.Kind]}
			if a.Admin {
				flags = append(flags, "admin")
			}
			if a.Disabled {
				flags = append(flags, "disabled")
			}
			if a.Builtin {
				flags = append(flags, "built-in")
			}
			last := "never"
			if a.LastLogon != nil {
				last = a.LastLogon.Local().Format("2006-01-02 15:04")
			}
			who := a.Name
			if a.Email != "" {
				who += " <" + a.Email + ">"
			} else if a.FullName != "" {
				who += " (" + a.FullName + ")"
			}
			fmt.Printf("  %-36s %-34s last sign-in %-16s %s\n", who, strings.Join(flags, ", "), last, a.Profile)
		}
		fmt.Println()
	}
	return nil
}
