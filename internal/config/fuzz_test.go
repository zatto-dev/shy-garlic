package config

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Fuzz tests guarding against regressions in goccy/go-yaml (SHYG-20).
//
// Every edit splices bytes at offsets taken from the library's AST, and the
// library is barely maintained (hundreds of open issues, no commits for
// months). A parser change that shifts token positions or mis-reads a
// construct would corrupt user files while the example-based tests keep
// passing. These targets pin the invariants the editor relies on:
//
//   - no input makes Load or Parse panic;
//   - an untouched document round-trips byte for byte;
//   - an edit that reports success leaves a file that still loads, and the
//     value written reads back as written.
//
// CI runs each target briefly (see .github/workflows/ci.yml); for a longer
// local run: go test ./internal/config -run '^$' -fuzz FuzzEdit -fuzztime 5m

func addFuzzSeeds(f *testing.F) {
	f.Helper()
	for _, p := range []string{"testdata/basic.yaml", "testdata/appearance.yaml", "../../examples/homelab.yaml"} {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
		f.Add(bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")))
	}
	f.Add([]byte(źródłoSprawdzania))
	f.Add([]byte(źródłoOpisów))
	f.Add([]byte("groups:\n  - name: G\n    services:\n      - name: A\n        url: http://a"))
	f.Add([]byte("groups: []\n"))
}

func FuzzLoadParse(f *testing.F) {
	addFuzzSeeds(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		_, _ = Load(src) // must not panic; errors are fine
		d, err := Parse(src)
		if err != nil {
			return
		}
		if !bytes.Equal(d.Bytes(), src) {
			t.Fatalf("untouched document changed:\n%q\n%q", src, d.Bytes())
		}
	})
}

// editValue turns fuzz input into a value the API would accept: valid UTF-8,
// no control characters, non-empty, at most 200 bytes.
func editValue(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "")
	}
	if s == "" {
		s = "x"
	}
	return s
}

func FuzzEdit(f *testing.F) {
	addFuzzSeeds(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		for op := range 8 {
			editOnce(t, src, op, "edited value: #1 'q' \"d\"")
		}
	})
}

func FuzzEditValue(f *testing.F) {
	for _, v := range []string{"plain", "with: colon", "# hash", "'quoted'", `"dq"`, "[flow]", "{m: 1}", "&anchor", "*alias", "- dash", "yes", "1e3", "żółć"} {
		f.Add(uint8(0), v)
	}
	f.Fuzz(func(t *testing.T, op uint8, raw string) {
		src, err := os.ReadFile("testdata/basic.yaml")
		if err != nil {
			t.Fatal(err)
		}
		editOnce(t, src, int(op%8), raw)
	})
}

// editOnce applies one editor operation to the first service that has a
// local (non-inherited) entry and checks the result.
func editOnce(t *testing.T, src []byte, op int, raw string) {
	t.Helper()
	cfg, err := Load(src)
	if err != nil || len(cfg.Groups) == 0 || len(cfg.Groups[0].Services) == 0 {
		return
	}
	d, err := Parse(src)
	if err != nil {
		return
	}
	g := cfg.Groups[0]
	s := g.Services[0]
	if !validName(g.Name) || !validName(s.Name) {
		return
	}
	v := editValue(raw)

	var check func(Service) bool
	switch op {
	case 0:
		err = d.SetServiceField(g.Name, s.Name, "url", "http://"+strings.ReplaceAll(v, " ", "-"))
		check = func(u Service) bool { return u.URL == "http://"+strings.ReplaceAll(v, " ", "-") }
	case 1:
		err = d.UstawOpisUsługi(g.Name, s.Name, v)
		want, _ := NormalizujOpis(v)
		check = func(u Service) bool { return u.Description == want }
	case 2:
		err = d.UstawTagiUsługi(g.Name, s.Name, []string{v[:min(len(v), 32)]})
		check = nil // normalisation may trim; loading is the invariant
	case 3:
		on := len(v)%2 == 0
		err = d.UstawSprawdzanie(g.Name, s.Name, ZmianaSprawdzania{Insecure: &on,
			Interval: ptr(Duration(time.Duration(len(v)+1) * time.Second)), Expect: &[]int{200, 401}})
		check = func(u Service) bool {
			return u.Check.Insecure == on && u.Check.Interval == Duration(time.Duration(len(v)+1)*time.Second) &&
				slices.Equal(u.Check.Expect, []int{200, 401})
		}
	case 4:
		err = d.AddService(g.Name, Service{Name: v, URL: "http://added"})
		if err == nil {
			assertLoads(t, src, d)
		}
		return
	case 5:
		err = d.RemoveService(g.Name, s.Name)
		if err == nil {
			assertLoads(t, src, d)
		}
		return
	case 6:
		err = d.RenameGroup(g.Name, v)
		if err == nil {
			assertLoads(t, src, d)
		}
		return
	default:
		err = d.SetServiceField(g.Name, s.Name, "name", v)
		check = func(u Service) bool { return u.Name == v }
	}
	if err != nil {
		return // refusing an edit is allowed; corrupting the file is not
	}
	after := assertLoads(t, src, d)
	if check == nil {
		return
	}
	if len(after.Groups) == 0 || len(after.Groups[0].Services) == 0 || !check(after.Groups[0].Services[0]) {
		t.Fatalf("op %d: value %q did not read back\nbefore:\n%s\nafter:\n%s", op, v, src, d.Bytes())
	}
}

func assertLoads(t *testing.T, src []byte, d *Document) *Config {
	t.Helper()
	after, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("edit produced a file that does not load: %v\nbefore:\n%s\nafter:\n%s", err, src, d.Bytes())
	}
	return after
}

func validName(v string) bool {
	if v == "" || len(v) > 200 || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func ptr[T any](v T) *T { return &v }
