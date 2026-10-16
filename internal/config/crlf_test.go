package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crlf zamienia LF na CRLF — tak wygląda config zapisany na Windowsie.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// czysteCRLF sprawdza, że w wyniku nie ma ani jednego gołego \n i że plik się wczytuje.
func czysteCRLF(t *testing.T, wynik []byte) {
	t.Helper()
	if bytes.Count(wynik, []byte("\n")) != bytes.Count(wynik, []byte("\r\n")) {
		t.Fatalf("mieszane końce linii:\n%q", wynik)
	}
	if bytes.Contains(wynik, []byte("\r\r")) {
		t.Fatalf("podwójny \\r:\n%q", wynik)
	}
	if _, err := Load(wynik); err != nil {
		t.Fatalf("wynik się nie wczytuje: %v\n%q", err, wynik)
	}
}

func TestCRLFNietkniętyRoundTripJestIdentyczny(t *testing.T) {
	src := crlf(ruchSrc)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Fatal("Bytes() bez zmian różni się od wejścia")
	}
}

func TestCRLFOperacje(t *testing.T) {
	cases := map[string]struct {
		op    func(d *Document) error
		układ string // oczekiwany układ po operacji ("" = nie sprawdzaj)
	}{
		"ruch usługi w grupie":        {func(d *Document) error { return d.MoveServiceTo("Media", "A", "Media", 2) }, "Media:B,C,A | Sieć:D,E | Pusta:"},
		"ruch usługi do innej grupy":  {func(d *Document) error { return d.MoveServiceTo("Media", "A", "Sieć", 1) }, "Media:B,C | Sieć:D,A,E | Pusta:"},
		"ruch usługi do pustej grupy": {func(d *Document) error { return d.MoveServiceTo("Media", "A", "Pusta", 0) }, "Media:B,C | Sieć:D,E | Pusta:A"},
		"ruch grupy":                  {func(d *Document) error { return d.MoveGroup("Pusta", 0) }, "Pusta: | Media:A,B,C | Sieć:D,E"},
		"tytuł istniejący":            {func(d *Document) error { return d.UstawTytuł("Nowy") }, ""},
		"opis":                        {func(d *Document) error { return d.UstawOpisUsługi("Media", "A", "opis A") }, ""},
		"tagi":                        {func(d *Document) error { return d.UstawTagiUsługi("Media", "A", []string{"x", "y"}) }, ""},
		"dodanie usługi": {func(d *Document) error {
			return d.AddService("Sieć", Service{Name: "F", URL: "http://f"})
		}, "Media:A,B,C | Sieć:D,E,F | Pusta:"},
		"usunięcie usługi": {func(d *Document) error { return d.RemoveService("Media", "B") }, "Media:A,C | Sieć:D,E | Pusta:"},
	}
	for nazwa, c := range cases {
		t.Run(nazwa, func(t *testing.T) {
			d, err := Parse(crlf(ruchSrc))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.op(d); err != nil {
				t.Fatal(err)
			}
			czysteCRLF(t, d.Bytes())
			if c.układ != "" {
				if got := układ(t, d.Bytes()); got != c.układ {
					t.Errorf("układ = %q, chcę %q", got, c.układ)
				}
			}
		})
	}
}

func TestCRLFTytułGdyBrakKlucza(t *testing.T) {
	d, err := Parse(crlf("groups:\n  - name: G\n    services:\n      - name: A\n        url: http://a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UstawTytuł("Nowy"); err != nil {
		t.Fatal(err)
	}
	czysteCRLF(t, d.Bytes())
	cfg, _ := Load(d.Bytes())
	if cfg.Title != "Nowy" {
		t.Errorf("title = %q", cfg.Title)
	}
}

func TestCRLFOpisGdyBrakKlucza(t *testing.T) {
	d, err := Parse(crlf(ruchSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UstawOpisUsługi("Sieć", "D", "nowy opis"); err != nil {
		t.Fatal(err)
	}
	czysteCRLF(t, d.Bytes())
}

func TestCRLFSaveZachowujeKońceLinii(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, crlf(ruchSrc), 0o600); err != nil {
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
	zapis, _ := os.ReadFile(p)
	czysteCRLF(t, zapis)
	// drugi zapis tego samego dokumentu: strażnik odcisku nie może protestować
	if err := d.MoveGroup("Media", 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatalf("drugi Save: %v", err)
	}
}

func TestMieszaneKońceLinii_NieNormalizujemy(t *testing.T) {
	src := []byte(strings.Replace(ruchSrc, "\n", "\r\n", 1)) // tylko pierwsza linia CRLF
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Fatal("plik z mieszanymi końcami zmienił się bez edycji")
	}
}

func TestReplaceAllZachowujeStylKońców(t *testing.T) {
	d, err := Parse(crlf(ruchSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceAll([]byte(ruchSrc)); err != nil { // LF z edytora
		t.Fatal(err)
	}
	czysteCRLF(t, d.Bytes())
}

func TestReplaceAllMieszaneKońceWPlikuCRLF(t *testing.T) {
	d, err := Parse(crlf(ruchSrc))
	if err != nil {
		t.Fatal(err)
	}
	// pierwsza linia z CRLF, reszta z LF — np. ręczny PUT przez API
	nowa := strings.Replace(ruchSrc, "\n", "\r\n", 1)
	if err := d.ReplaceAll([]byte(nowa)); err != nil {
		t.Fatal(err)
	}
	czysteCRLF(t, d.Bytes())
}
