package winacct_test

import (
	"errors"
	"os"
	"testing"
	"testing/fstest"

	"bitclone/internal/winacct"
	"bitclone/internal/winacct/winaccttest"
)

func volume() fstest.MapFS {
	m := fstest.MapFS{"Users": {Mode: os.ModeDir}}
	// Odd casing on purpose: Linux NTFS drivers are case-sensitive.
	for name, b := range winaccttest.Hives() {
		casing := map[string]string{"SAM": "SAM", "SOFTWARE": "software", "SYSTEM": "System"}[name]
		m["WINDOWS/system32/Config/"+casing] = &fstest.MapFile{Data: b}
	}
	return m
}

func TestScan(t *testing.T) {
	in, err := winacct.Scan(volume())
	if err != nil {
		t.Fatal(err)
	}
	if in.Product != "Windows 11 Pro" || in.Version != "23H2" || in.Build != "22631.4460" || in.ComputerName != "DESKTOP-TEST" {
		t.Fatalf("install: %+v", in)
	}
	got := map[string]winacct.Account{}
	var order []string
	for _, a := range in.Accounts {
		got[a.Name] = a
		order = append(order, a.Name)
		t.Logf("%-14s %-9s admin=%-5v disabled=%-5v builtin=%-5v %s %s %s", a.Name, a.Kind, a.Admin, a.Disabled, a.Builtin, a.SID, a.Email, a.Profile)
	}
	if len(in.Accounts) != 6 {
		t.Fatalf("want 6 accounts, got %v", order)
	}
	if order[len(order)-1] != "Guest" && order[len(order)-2] != "Guest" {
		t.Fatalf("built-in accounts should sort last: %v", order)
	}

	bob := got["Bob"]
	if bob.Kind != "local" || !bob.Admin || bob.Disabled || bob.FullName != "Bob Smith" || bob.Logons != 42 ||
		bob.SID != winaccttest.MachineSID+"-1001" || bob.Profile != `C:\Users\Bob` ||
		bob.LastLogon == nil || !bob.LastLogon.Equal(winaccttest.LastLogon) {
		t.Fatalf("Bob: %+v", bob)
	}
	alice := got["alice"]
	if alice.Kind != "microsoft" || alice.Email != "alice@outlook.com" || alice.Admin || alice.Profile != `C:\Users\alice` ||
		alice.LastLogon == nil || alice.LastLogon.Month() != 10 {
		t.Fatalf("alice: %+v", alice)
	}
	jane := got["JaneDoe"]
	if jane.Kind != "azuread" || jane.Email != "jane@contoso.com" || !jane.Admin || jane.LastLogon == nil {
		t.Fatalf("JaneDoe: %+v", jane)
	}
	if carl := got["carl.d"]; carl.Kind != "domain" || carl.Admin {
		t.Fatalf("carl.d: %+v", carl)
	}
	adm := got["Administrator"]
	if !adm.Builtin || !adm.Disabled || !adm.Admin {
		t.Fatalf("Administrator: %+v", adm)
	}
	if _, ok := got["systemprofile"]; ok {
		t.Fatal("system profile listed as an account")
	}
}

func TestNoWindows(t *testing.T) {
	if _, err := winacct.Scan(fstest.MapFS{"data/file.txt": {Data: []byte("x")}}); !errors.Is(err, winacct.ErrNoWindows) {
		t.Fatalf("got %v", err)
	}
}

func TestCorruptSAM(t *testing.T) {
	v := volume()
	v["WINDOWS/system32/Config/SAM"] = &fstest.MapFile{Data: []byte("regf but nothing else")}
	if _, err := winacct.Scan(v); err == nil {
		t.Fatal("corrupt SAM accepted")
	}
}

func TestOnDisk(t *testing.T) { // real files via os.DirFS (io.ReaderAt path)
	dir := t.TempDir()
	if err := winaccttest.WriteVolume(dir); err != nil {
		t.Fatal(err)
	}
	in, err := winacct.Scan(os.DirFS(dir))
	if err != nil || len(in.Accounts) != 6 {
		t.Fatalf("%v %+v", err, in)
	}
}
