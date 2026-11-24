package winreg_test

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"strings"
	"testing"

	"imagen/internal/winreg"
	rt "imagen/internal/winreg/regtest"
)

func open(t *testing.T, b []byte) *winreg.Key {
	t.Helper()
	h, err := winreg.Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	root, err := h.Root()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadBack(t *testing.T) {
	big := make([]byte, 40000) // needs a "db" big-data record
	rand.New(rand.NewSource(1)).Read(big)
	root := rt.K("ROOT")
	root.Path(`Software\Vendor\App`).With(rt.SZ("Name", "Zoë ✓"), rt.DWORD("Count", 7), rt.BIN("Big", big),
		rt.BIN("Tiny", []byte{1, 2}), rt.SZ("", "default"))
	for _, n := range []string{"a", "bb", "ccc", "dddd"} {
		root.Path(`Software\Many\` + n)
	}
	k := open(t, rt.Build(root))

	if got := k.GetString(`software\VENDOR\app`, "name"); got != "Zoë ✓" {
		t.Fatalf("string: %q", got)
	}
	if n, ok := k.GetUint(`Software\Vendor\App`, "Count"); !ok || n != 7 {
		t.Fatalf("dword: %d %v", n, ok)
	}
	if got := k.GetString(`Software\Vendor\App`, ""); got != "default" {
		t.Fatalf("default value: %q", got)
	}
	app, _ := k.Subkey(`Software\Vendor\App`)
	for name, want := range map[string][]byte{"Big": big, "Tiny": {1, 2}} {
		v, err := app.Value(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.Data()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %d bytes, err %v", name, len(got), err)
		}
	}
	many, _ := k.Subkey(`Software\Many`)
	subs, err := many.Subkeys()
	if err != nil || len(subs) != 4 {
		t.Fatalf("subkeys: %d %v", len(subs), err)
	}
	if _, err := k.Subkey(`Software\Nope`); !errors.Is(err, winreg.ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
}

// Corrupted hives must give errors, never panics.
func TestCorruptNoPanic(t *testing.T) {
	root := rt.K("ROOT")
	root.Path(`A\B\C`).With(rt.SZ("x", strings.Repeat("y", 100)), rt.DWORD("d", 1))
	good := rt.Build(root)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 3000; i++ {
		b := append([]byte(nil), good...)
		for j := 0; j < 1+rng.Intn(8); j++ {
			b[4096+rng.Intn(len(b)-4096)] = byte(rng.Intn(256))
		}
		if i%10 == 0 {
			b = b[:4096+rng.Intn(len(b)-4096)]
		}
		h, err := winreg.Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			continue
		}
		var walk func(k *winreg.Key, d int)
		walk = func(k *winreg.Key, d int) {
			if d > 10 {
				return
			}
			vals, _ := k.Values()
			for _, v := range vals {
				v.Data()
			}
			subs, _ := k.Subkeys()
			for _, s := range subs {
				walk(s, d+1)
			}
		}
		if k, err := h.Root(); err == nil {
			walk(k, 0)
		}
	}
}

// IMAGEN_REAL_HIVE=path runs the reader over a hive saved by Windows
// ("reg save HKCU\Software\X file.hiv"), as a check against the real format.
func TestRealHive(t *testing.T) {
	p := os.Getenv("IMAGEN_REAL_HIVE")
	if p == "" {
		t.Skip("set IMAGEN_REAL_HIVE")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	k := open(t, b)
	keys, vals := 0, 0
	var walk func(k *winreg.Key)
	walk = func(k *winreg.Key) {
		keys++
		vs, err := k.Values()
		if err != nil {
			t.Fatalf("%s: %v", k.Name, err)
		}
		for _, v := range vs {
			if _, err := v.Data(); err != nil {
				t.Fatalf("%s\\%s: %v", k.Name, v.Name, err)
			}
			vals++
		}
		subs, err := k.Subkeys()
		if err != nil {
			t.Fatalf("%s: %v", k.Name, err)
		}
		for _, s := range subs {
			walk(s)
		}
	}
	walk(k)
	t.Logf("root %q: %d keys, %d values read", k.Name, keys, vals)
	if want := os.Getenv("IMAGEN_REAL_HIVE_CHECK"); want != "" { // path|name|expected
		f := strings.Split(want, "|")
		if got := k.GetString(f[0], f[1]); got != f[2] {
			t.Fatalf("%s\\%s = %q, want %q", f[0], f[1], got, f[2])
		}
	}
}
