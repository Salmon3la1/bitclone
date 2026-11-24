// Package winaccttest builds a fake Windows system volume (registry hives
// only) for tests and demos.
package winaccttest

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	rt "imagen/internal/winreg/regtest"
)

// MachineSID is the fixture's local machine SID.
const MachineSID = "S-1-5-21-111-222-333"

// LastLogon is Bob's last sign-in time in the fixture.
var LastLogon = time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC)

// SIDBytes encodes a SID string.
func SIDBytes(s string) []byte {
	f := strings.Split(s, "-")[1:] // 1, auth, subs...
	auth, _ := strconv.ParseUint(f[1], 10, 48)
	b := []byte{1, byte(len(f) - 2), 0, 0, 0, 0, 0, 0}
	for i := 0; i < 6; i++ {
		b[2+i] = byte(auth >> (8 * (5 - i)))
	}
	for _, sub := range f[2:] {
		n, _ := strconv.ParseUint(sub, 10, 32)
		b = binary.LittleEndian.AppendUint32(b, uint32(n))
	}
	return b
}

func filetime(t time.Time) uint64 { return uint64(t.UnixNano()/100) + 116444736000000000 }

type user struct {
	rid             uint32
	name, full, msa string
	disabled        bool
	last            time.Time
	logons          uint16
}

func userKey(u user) *rt.Key {
	le := binary.LittleEndian
	f := make([]byte, 0x50)
	if !u.last.IsZero() {
		le.PutUint64(f[0x08:], filetime(u.last))
	}
	le.PutUint32(f[0x30:], u.rid)
	acb := uint16(0x0010) // normal account
	if u.disabled {
		acb |= 0x0001
	}
	le.PutUint16(f[0x38:], acb)
	le.PutUint16(f[0x42:], u.logons)

	v := make([]byte, 0xCC)
	put := func(i int, s string) {
		d := rt.UTF16(s)
		le.PutUint32(v[i*12:], uint32(len(v)-0xCC))
		le.PutUint32(v[i*12+4:], uint32(len(d)))
		v = append(v, d...)
		for len(v)%4 != 0 {
			v = append(v, 0)
		}
	}
	put(1, u.name)
	put(2, u.full)
	put(3, "")
	k := rt.K(fmt.Sprintf("%08X", u.rid)).With(rt.BIN("F", f), rt.BIN("V", v))
	if u.msa != "" {
		k.With(rt.SZ("InternetUserName", u.msa))
	}
	return k
}

// Hives returns SAM, SOFTWARE and SYSTEM hive files.
func Hives() map[string][]byte {
	users := []user{
		{rid: 500, name: "Administrator", full: "", disabled: true},
		{rid: 501, name: "Guest", disabled: true},
		{rid: 1001, name: "Bob", full: "Bob Smith", last: LastLogon, logons: 42},
		{rid: 1002, name: "alice", full: "Alice Example", msa: "alice@outlook.com", logons: 3},
	}
	sam := rt.K("CMI-CreateHive{C4E7BA2B-68E8-499C-B1A1-371AC8D717C7}")
	names := sam.Path(`SAM\Domains\Account\Users\Names`)
	usersKey := sam.Path(`SAM\Domains\Account\Users`)
	for _, u := range users {
		names.Keys = append(names.Keys, rt.K(u.name).With(rt.Value{Name: "", Type: u.rid}))
		usersKey.Keys = append(usersKey.Keys, userKey(u))
	}
	domV := append(make([]byte, 0x40), SIDBytes(MachineSID)...)
	sam.Path(`SAM\Domains\Account`).With(rt.BIN("V", domV), rt.BIN("F", make([]byte, 0x50)))

	c := make([]byte, 0x34)
	var members []byte
	admins := []string{MachineSID + "-500", MachineSID + "-1001", "S-1-12-1-10-20-30-40"}
	for _, s := range admins {
		members = append(members, SIDBytes(s)...)
	}
	binary.LittleEndian.PutUint32(c[0x28:], 0)
	binary.LittleEndian.PutUint32(c[0x30:], uint32(len(admins)))
	sam.Path(`SAM\Domains\Builtin\Aliases\00000220`).With(rt.BIN("C", append(c, members...)))

	soft := rt.K("ROOT")
	soft.Path(`Microsoft\Windows NT\CurrentVersion`).With(rt.SZ("ProductName", "Windows 10 Pro"),
		rt.SZ("CurrentBuild", "22631"), rt.DWORD("UBR", 4460), rt.SZ("DisplayVersion", "23H2"))
	pl := `Microsoft\Windows NT\CurrentVersion\ProfileList\`
	prof := func(sid, path string, t time.Time) {
		k := soft.Path(pl + sid).With(rt.Value{Name: "ProfileImagePath", Type: 2, Data: rt.UTF16(path + "\x00")})
		if !t.IsZero() {
			ft := filetime(t)
			k.With(rt.DWORD("LocalProfileLoadTimeHigh", uint32(ft>>32)), rt.DWORD("LocalProfileLoadTimeLow", uint32(ft)))
		}
	}
	prof("S-1-5-18", `%systemroot%\system32\config\systemprofile`, time.Time{})
	prof(MachineSID+"-1001", `C:\Users\Bob`, time.Time{})
	prof(MachineSID+"-1002", `%SystemDrive%\Users\alice`, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	prof("S-1-12-1-10-20-30-40", `C:\Users\JaneDoe`, time.Date(2026, 11, 22, 17, 5, 0, 0, time.UTC))
	prof("S-1-5-21-9-8-7-1105", `C:\Users\carl.d`, time.Time{})
	soft.Path(`Microsoft\IdentityStore\Cache\S-1-12-1-10-20-30-40\IdentityCache\S-1-12-1-10-20-30-40`).
		With(rt.SZ("UserName", "jane@contoso.com"))

	sys := rt.K("ROOT")
	sys.Path("Select").With(rt.DWORD("Current", 1))
	sys.Path(`ControlSet001\Control\ComputerName\ComputerName`).With(rt.SZ("ComputerName", "DESKTOP-TEST"))

	return map[string][]byte{"SAM": rt.Build(sam), "SOFTWARE": rt.Build(soft), "SYSTEM": rt.Build(sys)}
}

// WriteVolume writes the fixture under dir as Windows/System32/config/*.
func WriteVolume(dir string) error {
	cfg := filepath.Join(dir, "Windows", "System32", "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		return err
	}
	for name, b := range Hives() {
		if err := os.WriteFile(filepath.Join(cfg, name), b, 0o644); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(dir, "Users"), 0o755)
}
