// Package winacct lists the user accounts of a Windows installation by
// reading its registry hives (SAM, SOFTWARE, SYSTEM) from a mounted volume.
// It is strictly read-only and never reads password hashes or secrets.
package winacct

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"bitclone/internal/winreg"
)

// Account is one user account.
type Account struct {
	Name      string     `json:"name"`
	FullName  string     `json:"fullName,omitempty"`
	SID       string     `json:"sid"`
	Kind      string     `json:"kind"`            // local, microsoft, domain, azuread
	Email     string     `json:"email,omitempty"` // Microsoft / work or school account
	Admin     bool       `json:"admin"`
	Disabled  bool       `json:"disabled"`
	Builtin   bool       `json:"builtin"` // Administrator, Guest, DefaultAccount, WDAGUtilityAccount
	LastLogon *time.Time `json:"lastLogon,omitempty"`
	Logons    int        `json:"logons,omitempty"`
	Profile   string     `json:"profile,omitempty"` // C:\Users\name
}

// Install describes one Windows installation.
type Install struct {
	Product      string    `json:"product"` // Windows 11 Pro
	Version      string    `json:"version,omitempty"`
	Build        string    `json:"build,omitempty"`
	ComputerName string    `json:"computerName,omitempty"`
	Accounts     []Account `json:"accounts"`
	Warnings     []string  `json:"warnings,omitempty"`
}

const configDir = "Windows/System32/config"

// ErrNoWindows means the volume holds no Windows installation.
var ErrNoWindows = errors.New("no Windows installation on this volume")

// resolve finds a slash path in fsys, matching each element case-insensitively
// (Linux NTFS drivers are case-sensitive; Windows is not).
func resolve(fsys fs.FS, p string) (string, error) {
	cur := "."
	for _, part := range strings.Split(p, "/") {
		ents, err := fs.ReadDir(fsys, cur)
		if err != nil {
			return "", err
		}
		found := ""
		for _, e := range ents {
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
				if e.Name() == part {
					break
				}
			}
		}
		if found == "" {
			return "", fs.ErrNotExist
		}
		cur = path.Join(cur, found)
	}
	return cur, nil
}

// HasWindows reports whether fsys looks like a Windows system volume.
func HasWindows(fsys fs.FS) bool {
	_, err := resolve(fsys, configDir+"/SAM")
	return err == nil
}

type hive struct {
	root  *winreg.Key
	dirty bool
	close func() error
}

func openHive(fsys fs.FS, name string) (*hive, error) {
	p, err := resolve(fsys, configDir+"/"+name)
	if err != nil {
		return nil, err
	}
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	ra, ok := f.(io.ReaderAt)
	if !ok {
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		ra, f = bytes.NewReader(b), nil
	}
	h, err := winreg.Open(ra, st.Size())
	if err == nil {
		var root *winreg.Key
		if root, err = h.Root(); err == nil {
			return &hive{root: root, dirty: h.Dirty, close: func() error {
				if f != nil {
					return f.Close()
				}
				return nil
			}}, nil
		}
	}
	if f != nil {
		f.Close()
	}
	return nil, fmt.Errorf("%s: %w", name, err)
}

// Scan reads the accounts of the Windows installation at the root of fsys.
func Scan(fsys fs.FS) (in *Install, err error) {
	defer func() { // the parser is bounds-checked; this is a last line of defence
		if r := recover(); r != nil {
			in, err = nil, fmt.Errorf("unreadable registry data: %v", r)
		}
	}()
	if !HasWindows(fsys) {
		return nil, ErrNoWindows
	}
	in = &Install{Product: "Windows"}
	dirty := false

	sam, err := openHive(fsys, "SAM")
	if err != nil {
		return nil, fmt.Errorf("read the account database: %w", err)
	}
	defer sam.close()
	dirty = dirty || sam.dirty

	var soft *winreg.Key
	if h, err := openHive(fsys, "SOFTWARE"); err == nil {
		defer h.close()
		soft, dirty = h.root, dirty || h.dirty
		readVersion(soft, in)
	} else {
		in.Warnings = append(in.Warnings, "Could not read the SOFTWARE hive, so the Windows version, profile folders and work/domain accounts are missing: "+err.Error())
	}
	if h, err := openHive(fsys, "SYSTEM"); err == nil {
		defer h.close()
		in.ComputerName = computerName(h.root)
	}
	if dirty {
		in.Warnings = append(in.Warnings, "Windows was not shut down cleanly (or Fast Startup is on). Accounts added or changed in the last session may be missing.")
	}

	local, machineSID, err := samAccounts(sam.root)
	if err != nil {
		return nil, err
	}
	admins := adminMembers(sam.root)
	profiles := profileList(soft)

	if machineSID == "" { // fall back: a profile SID whose RID and folder match a local account
		for _, p := range profiles {
			for _, a := range local {
				if strings.HasSuffix(p.sid, "-"+a.SID) && strings.HasPrefix(p.sid, "S-1-5-21-") &&
					strings.EqualFold(winBase(p.path), a.Name) {
					machineSID = strings.TrimSuffix(p.sid, "-"+a.SID)
				}
			}
		}
	}
	used := map[string]bool{}
	for i := range local {
		a := &local[i]
		rid := a.SID
		if machineSID != "" {
			a.SID = machineSID + "-" + rid
		} else {
			a.SID = "RID " + rid
		}
		a.Admin = admins[a.SID]
		if p, ok := profiles[a.SID]; ok {
			used[a.SID] = true
			a.Profile = p.path
			if a.LastLogon == nil {
				a.LastLogon = p.loaded
			}
		}
	}
	accts := local
	for sid, p := range profiles {
		if used[sid] {
			continue
		}
		var kind string
		switch {
		case strings.HasPrefix(sid, "S-1-12-1-"):
			kind = "azuread"
		case strings.HasPrefix(sid, "S-1-5-21-"):
			kind = "domain"
		default:
			continue // system, service and app-container profiles
		}
		a := Account{Name: winBase(p.path), SID: sid, Kind: kind, Admin: admins[sid], Profile: p.path, LastLogon: p.loaded}
		if e := identityName(soft, sid); e != "" {
			a.Email = e
		}
		accts = append(accts, a)
	}
	sort.SliceStable(accts, func(i, j int) bool {
		if accts[i].Builtin != accts[j].Builtin {
			return !accts[i].Builtin
		}
		return strings.ToLower(accts[i].Name) < strings.ToLower(accts[j].Name)
	})
	in.Accounts = accts
	return in, nil
}

func readVersion(soft *winreg.Key, in *Install) {
	const cv = `Microsoft\Windows NT\CurrentVersion`
	if p := soft.GetString(cv, "ProductName"); p != "" {
		in.Product = p
	}
	in.Build = soft.GetString(cv, "CurrentBuild")
	if ubr, ok := soft.GetUint(cv, "UBR"); ok && in.Build != "" {
		in.Build += "." + strconv.FormatUint(ubr, 10)
	}
	in.Version = soft.GetString(cv, "DisplayVersion")
	if in.Version == "" {
		in.Version = soft.GetString(cv, "ReleaseId")
	}
	// Windows 11 still says "Windows 10" in ProductName; build 22000+ is 11.
	if b, _ := strconv.Atoi(strings.SplitN(in.Build, ".", 2)[0]); b >= 22000 {
		in.Product = strings.Replace(in.Product, "Windows 10", "Windows 11", 1)
	}
}

func computerName(sys *winreg.Key) string {
	cur, ok := sys.GetUint("Select", "Current")
	if !ok || cur == 0 || cur > 999 {
		cur = 1
	}
	return sys.GetString(fmt.Sprintf(`ControlSet%03d\Control\ComputerName\ComputerName`, cur), "ComputerName")
}

// samAccounts returns local accounts with SID set to the bare RID (decimal),
// plus the machine SID if it could be read.
func samAccounts(sam *winreg.Key) ([]Account, string, error) {
	names, err := sam.Subkey(`SAM\Domains\Account\Users\Names`)
	if err != nil {
		return nil, "", fmt.Errorf("account database has no user list: %w", err)
	}
	users, _ := sam.Subkey(`SAM\Domains\Account\Users`)
	subs, err := names.Subkeys()
	if err != nil {
		return nil, "", err
	}
	var out []Account
	for _, n := range subs {
		def, err := n.Value("")
		if err != nil {
			continue
		}
		rid := def.Type // SAM stores the RID in the default value's type field
		a := Account{Name: n.Name, SID: strconv.FormatUint(uint64(rid), 10), Kind: "local",
			Builtin: rid == 500 || rid == 501 || rid == 503 || rid == 504}
		if users != nil {
			if u, err := users.Subkey(fmt.Sprintf("%08X", rid)); err == nil {
				userDetails(u, &a)
			}
		}
		out = append(out, a)
	}
	machine := ""
	if acct, err := sam.Subkey(`SAM\Domains\Account`); err == nil {
		if v, err := acct.Value("V"); err == nil {
			if b, err := v.Data(); err == nil && len(b) >= 24 {
				if sid, n := parseSID(b[len(b)-24:]); n == 24 && strings.HasPrefix(sid, "S-1-5-21-") {
					machine = sid
				}
			}
		}
	}
	return out, machine, nil
}

func userDetails(u *winreg.Key, a *Account) {
	le := binary.LittleEndian
	if v, err := u.Value("F"); err == nil {
		if f, err := v.Data(); err == nil && len(f) >= 0x44 {
			a.LastLogon = filetime(le.Uint64(f[0x08:]))
			a.Disabled = le.Uint16(f[0x38:])&0x0001 != 0
			a.Logons = int(le.Uint16(f[0x42:]))
		}
	}
	if v, err := u.Value("V"); err == nil {
		if b, err := v.Data(); err == nil && len(b) >= 0xCC {
			field := func(i int) string { // offset table entry i, data relative to 0xCC
				off, n := int(le.Uint32(b[i*12:]))+0xCC, int(le.Uint32(b[i*12+4:]))
				if n <= 0 || n > 1024 || off < 0xCC || off+n > len(b) {
					return ""
				}
				return fromUTF16(b[off : off+n])
			}
			if name := field(1); name != "" {
				a.Name = name
			}
			a.FullName = field(2)
		}
	}
	if v, err := u.Value("InternetUserName"); err == nil { // linked Microsoft account
		if s, err := v.String(); err == nil && s != "" {
			a.Kind, a.Email = "microsoft", s
		}
	}
}

// adminMembers returns the SIDs in the built-in Administrators group.
func adminMembers(sam *winreg.Key) map[string]bool {
	out := map[string]bool{}
	k, err := sam.Subkey(`SAM\Domains\Builtin\Aliases\00000220`)
	if err != nil {
		return out
	}
	v, err := k.Value("C")
	if err != nil {
		return out
	}
	c, err := v.Data()
	if err != nil || len(c) < 0x34 {
		return out
	}
	le := binary.LittleEndian
	off, n := int(le.Uint32(c[0x28:]))+0x34, int(le.Uint32(c[0x30:]))
	for i := 0; i < n && i < 1000 && off < len(c); i++ {
		sid, used := parseSID(c[off:])
		if used == 0 {
			break
		}
		out[sid] = true
		off += used
	}
	return out
}

type profile struct {
	sid, path string
	loaded    *time.Time
}

func profileList(soft *winreg.Key) map[string]profile {
	out := map[string]profile{}
	if soft == nil {
		return out
	}
	pl, err := soft.Subkey(`Microsoft\Windows NT\CurrentVersion\ProfileList`)
	if err != nil {
		return out
	}
	subs, _ := pl.Subkeys()
	for _, s := range subs {
		sid := strings.TrimSuffix(s.Name, ".bak")
		p := profile{sid: sid, path: s.GetString("", "ProfileImagePath")}
		p.path = strings.Replace(p.path, "%SystemDrive%", "C:", 1)
		hi, ok1 := s.GetUint("", "LocalProfileLoadTimeHigh")
		lo, ok2 := s.GetUint("", "LocalProfileLoadTimeLow")
		if ok1 && ok2 {
			p.loaded = filetime(hi<<32 | lo)
		}
		if old, dup := out[sid]; !dup || old.path == "" {
			out[sid] = p
		}
	}
	return out
}

// identityName returns the sign-in name Windows cached for a work/school or
// Microsoft account SID.
func identityName(soft *winreg.Key, sid string) string {
	if soft == nil {
		return ""
	}
	return soft.GetString(`Microsoft\IdentityStore\Cache\`+sid+`\IdentityCache\`+sid, "UserName")
}

func winBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// parseSID decodes a binary SID; returns its string form and length in bytes.
func parseSID(b []byte) (string, int) {
	if len(b) < 8 || b[0] != 1 {
		return "", 0
	}
	n := int(b[1])
	if n > 15 || len(b) < 8+4*n {
		return "", 0
	}
	var auth uint64
	for _, c := range b[2:8] {
		auth = auth<<8 | uint64(c)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "S-1-%d", auth)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "-%d", binary.LittleEndian.Uint32(b[8+4*i:]))
	}
	return sb.String(), 8 + 4*n
}

func filetime(ft uint64) *time.Time {
	const epoch = 116444736000000000 // 1601-01-01 to 1970-01-01 in 100 ns units
	if ft <= epoch || ft >= 0x7FFFFFFFFFFFFFFF {
		return nil
	}
	d := ft - epoch
	t := time.Unix(int64(d/1e7), int64(d%1e7)*100).UTC()
	if t.Year() < 1990 || t.Year() > 2200 {
		return nil
	}
	return &t
}

func fromUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}
