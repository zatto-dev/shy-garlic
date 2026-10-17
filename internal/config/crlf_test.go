package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crlf turns LF into CRLF — this is what a config saved on Windows looks like.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// pureCRLF checks that the result has not a single bare \n and that the file loads.
func pureCRLF(t *testing.T, result []byte) {
	t.Helper()
	if bytes.Count(result, []byte("\n")) != bytes.Count(result, []byte("\r\n")) {
		t.Fatalf("mixed line endings:\n%q", result)
	}
	if bytes.Contains(result, []byte("\r\r")) {
		t.Fatalf("double \\r:\n%q", result)
	}
	if _, err := Load(result); err != nil {
		t.Fatalf("result does not load: %v\n%q", err, result)
	}
}

func TestCRLFUntouchedRoundTripIsIdentical(t *testing.T) {
	src := crlf(moveSrc)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Fatal("Bytes() without changes differs from the input")
	}
}

func TestCRLFOperations(t *testing.T) {
	cases := map[string]struct {
		op     func(d *Document) error
		layout string // expected layout after the operation ("" = do not check)
	}{
		"move service within group":      {func(d *Document) error { return d.MoveServiceTo("Media", "A", "Media", 2) }, "Media:B,C,A | Sieć:D,E | Pusta:"},
		"move service to another group":  {func(d *Document) error { return d.MoveServiceTo("Media", "A", "Sieć", 1) }, "Media:B,C | Sieć:D,A,E | Pusta:"},
		"move service to an empty group": {func(d *Document) error { return d.MoveServiceTo("Media", "A", "Pusta", 0) }, "Media:B,C | Sieć:D,E | Pusta:A"},
		"move group":                     {func(d *Document) error { return d.MoveGroup("Pusta", 0) }, "Pusta: | Media:A,B,C | Sieć:D,E"},
		"existing title":                 {func(d *Document) error { return d.SetTitle("Nowy") }, ""},
		"description":                    {func(d *Document) error { return d.SetServiceDescription("Media", "A", "opis A") }, ""},
		"tags":                           {func(d *Document) error { return d.SetServiceTags("Media", "A", []string{"x", "y"}) }, ""},
		"add service": {func(d *Document) error {
			return d.AddService("Sieć", Service{Name: "F", URL: "http://f"})
		}, "Media:A,B,C | Sieć:D,E,F | Pusta:"},
		"remove service": {func(d *Document) error { return d.RemoveService("Media", "B") }, "Media:A,C | Sieć:D,E | Pusta:"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := Parse(crlf(moveSrc))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.op(d); err != nil {
				t.Fatal(err)
			}
			pureCRLF(t, d.Bytes())
			if c.layout != "" {
				if got := layoutOf(t, d.Bytes()); got != c.layout {
					t.Errorf("layout = %q, want %q", got, c.layout)
				}
			}
		})
	}
}

func TestCRLFTitleWhenKeyMissing(t *testing.T) {
	d, err := Parse(crlf("groups:\n  - name: G\n    services:\n      - name: A\n        url: http://a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetTitle("Nowy"); err != nil {
		t.Fatal(err)
	}
	pureCRLF(t, d.Bytes())
	cfg, _ := Load(d.Bytes())
	if cfg.Title != "Nowy" {
		t.Errorf("title = %q", cfg.Title)
	}
}

func TestCRLFDescriptionWhenKeyMissing(t *testing.T) {
	d, err := Parse(crlf(moveSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceDescription("Sieć", "D", "nowy opis"); err != nil {
		t.Fatal(err)
	}
	pureCRLF(t, d.Bytes())
}

func TestCRLFSavePreservesLineEndings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, crlf(moveSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveGroup("Pusta", 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	write, _ := os.ReadFile(p)
	pureCRLF(t, write)
	// second save of the same document: the fingerprint guard must not complain
	if err := d.MoveGroup("Media", 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}
}

func TestMixedLineEndings_NotNormalized(t *testing.T) {
	src := []byte(strings.Replace(moveSrc, "\n", "\r\n", 1)) // only the first line CRLF
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Fatal("file with mixed endings changed without an edit")
	}
}

func TestReplaceAllPreservesEndingStyle(t *testing.T) {
	d, err := Parse(crlf(moveSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceAll([]byte(moveSrc)); err != nil { // LF from the editor
		t.Fatal(err)
	}
	pureCRLF(t, d.Bytes())
}

func TestReplaceAllMixedEndingsInCRLFFile(t *testing.T) {
	d, err := Parse(crlf(moveSrc))
	if err != nil {
		t.Fatal(err)
	}
	// first line with CRLF, the rest LF — e.g. a manual PUT through the API
	newName := strings.Replace(moveSrc, "\n", "\r\n", 1)
	if err := d.ReplaceAll([]byte(newName)); err != nil {
		t.Fatal(err)
	}
	pureCRLF(t, d.Bytes())
}
