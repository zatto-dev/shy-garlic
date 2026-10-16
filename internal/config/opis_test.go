package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const źródłoOpisów = `# nagłówek
defaults: &defaults
  description: Wspólny opis
  tags: [wspólny]

groups:
  - name: G
    services:
      - name: A
        url: http://a
      - name: B
        url: http://b
        description: "Cytowany opis"   # komentarz zostaje
        tags: [x, y]   # i ten też
      - name: C
        url: http://c
        tags:
          - jeden
          - dwa
        check:
          interval: 5s
      - description: opis jako pierwszy klucz
        name: D
        url: http://d
      - name: E
        url: http://e
        <<: *defaults
`

func usługa(t *testing.T, src []byte, nazwa string) Service {
	t.Helper()
	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, src)
	}
	for _, g := range cfg.Groups {
		for _, s := range g.Services {
			if s.Name == nazwa {
				return s
			}
		}
	}
	t.Fatalf("brak usługi %q", nazwa)
	return Service{}
}

func parsuj(t *testing.T, src string) *Document {
	t.Helper()
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLoadCzytaOpisITagi(t *testing.T) {
	src := []byte(źródłoOpisów)
	if s := usługa(t, src, "A"); s.Description != "" || len(s.Tags) != 0 {
		t.Errorf("A bez pól: %+v", s)
	}
	b := usługa(t, src, "B")
	if b.Description != "Cytowany opis" || !reflect.DeepEqual([]string(b.Tags), []string{"x", "y"}) {
		t.Errorf("B: %+v", b)
	}
	if c := usługa(t, src, "C"); !reflect.DeepEqual([]string(c.Tags), []string{"jeden", "dwa"}) {
		t.Errorf("C (lista blokowa): %v", c.Tags)
	}
	// dziedziczenie przez merge-key działa tak jak dla `check`
	e := usługa(t, src, "E")
	if e.Description != "Wspólny opis" || !reflect.DeepEqual([]string(e.Tags), []string{"wspólny"}) {
		t.Errorf("E (merge): %+v", e)
	}
}

func TestLoadTagiJakoPojedynczyTekstINormalizacja(t *testing.T) {
	src := []byte(`groups:
  - name: G
    services:
      - name: Jeden
        url: http://a
        tags: media
      - name: Brudne
        url: http://b
        description: "  dwa   wiersze
          w jednym  "
        tags: ["  a ", "", "A", b]
`)
	if s := usługa(t, src, "Jeden"); !reflect.DeepEqual([]string(s.Tags), []string{"media"}) {
		t.Errorf("tags: media → %v", s.Tags)
	}
	b := usługa(t, src, "Brudne")
	if b.Description != "dwa wiersze w jednym" {
		t.Errorf("opis nieznormalizowany: %q", b.Description)
	}
	if !reflect.DeepEqual([]string(b.Tags), []string{"a", "b"}) {
		t.Errorf("tagi nieznormalizowane (trim, puste, duplikaty bez względu na wielkość): %v", b.Tags)
	}
}

func TestNormalizujOpis(t *testing.T) {
	if v, err := NormalizujOpis("  Serwer mediów  "); err != nil || v != "Serwer mediów" {
		t.Errorf("trim: %q %v", v, err)
	}
	if v, err := NormalizujOpis(""); err != nil || v != "" {
		t.Errorf("pusty to prawidłowe „usuń”: %q %v", v, err)
	}
	if _, err := NormalizujOpis(strings.Repeat("ż", MaksOpisu+1)); !errors.Is(err, ErrOpisDługi) {
		t.Errorf("za długi: %v", err)
	}
	if _, err := NormalizujOpis(strings.Repeat("ż", MaksOpisu)); err != nil {
		t.Errorf("limit liczony w znakach, nie bajtach: %v", err)
	}
	for _, zły := range []string{"a\nb", "a\x00b", "a\u2028b", "a\tb"} {
		if _, err := NormalizujOpis(zły); !errors.Is(err, ErrOpisZnaki) {
			t.Errorf("%q: %v, chcę ErrOpisZnaki", zły, err)
		}
	}
}

func TestNormalizujTagi(t *testing.T) {
	got, err := NormalizujTagi([]string{" media ", "", "Media", "4k"})
	if err != nil || !reflect.DeepEqual(got, []string{"media", "4k"}) {
		t.Errorf("%v %v", got, err)
	}
	if _, err := NormalizujTagi([]string{strings.Repeat("a", MaksTagu+1)}); !errors.Is(err, ErrTagiZły) {
		t.Errorf("za długi tag: %v", err)
	}
	if _, err := NormalizujTagi([]string{"a\nb"}); !errors.Is(err, ErrTagiZły) {
		t.Errorf("znak sterujący: %v", err)
	}
	wiele := make([]string, MaksTagów+1)
	for i := range wiele {
		wiele[i] = string(rune('a' + i))
	}
	if _, err := NormalizujTagi(wiele); !errors.Is(err, ErrTagiZły) {
		t.Errorf("za dużo tagów: %v", err)
	}
}

// ── opis: splice ─────────────────────────────────────────────────────────────

func TestUstawOpisDopisujeKluczNaKoniecBloku(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawOpisUsługi("G", "C", "Nowy opis"); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	// C kończy się blokiem `check:` — klucz ma wylądować za nim, na wcięciu usługi
	if !strings.Contains(got, "          interval: 5s\n        description: Nowy opis\n      - description: opis jako") {
		t.Fatalf("opis nie stoi na końcu bloku C:\n%s", got)
	}
	if usługa(t, d.Bytes(), "C").Description != "Nowy opis" {
		t.Error("Load nie widzi opisu")
	}
}

func TestUstawOpisPodmieniaTylkoWartośćZachowujeCytowanieIKomentarz(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawOpisUsługi("G", "B", "Inny"); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, `description: "Inny"   # komentarz zostaje`) {
		t.Fatalf("cytowanie albo komentarz zginął:\n%s", got)
	}
	zmienione := diffLinie(t, źródłoOpisów, got)
	if len(zmienione) != 1 {
		t.Errorf("zmienionych linii = %v, chcę 1", zmienione)
	}
}

func TestUstawOpisCytujeGdyTrzeba(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawOpisUsługi("G", "A", "Uwaga: to jest # test"); err != nil {
		t.Fatal(err)
	}
	if got := usługa(t, d.Bytes(), "A").Description; got != "Uwaga: to jest # test" {
		t.Errorf("round-trip opisu ze znakami specjalnymi: %q", got)
	}
}

func TestUstawOpisPustyUsuwaLinię(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawOpisUsługi("G", "B", ""); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if strings.Contains(got, "Cytowany opis") || strings.Contains(got, "komentarz zostaje") {
		t.Errorf("linia opisu została:\n%s", got)
	}
	if !strings.Contains(got, "        tags: [x, y]   # i ten też\n") {
		t.Errorf("sąsiednia linia ucierpiała:\n%s", got)
	}
}

func TestUstawOpisPustyGdyOpisJestPierwszymKluczemNieKasujeMyślnika(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawOpisUsługi("G", "D", ""); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, "      - name: D\n        url: http://d\n      - name: E") {
		t.Fatalf("usługa D się rozjechała:\n%s", got)
	}
	if s := usługa(t, d.Bytes(), "D"); s.Description != "" || s.URL != "http://d" {
		t.Errorf("D: %+v", s)
	}
}

// goccy odrzuca klucz lokalny obok tego samego klucza wniesionego przez merge
// („duplicate key"), więc nadpisanie dziedziczonej wartości nie jest możliwe —
// panel ma to powiedzieć wprost zamiast zapisać plik, którego nie da się wczytać.
func TestUstawOpisOdmawiaGdyOpisPrzychodziZMerge(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	for _, opis := range []string{"", "Mój opis"} {
		if err := d.UstawOpisUsługi("G", "E", opis); !errors.Is(err, ErrPoleDziedziczone) {
			t.Errorf("opis %q: err = %v, chcę ErrPoleDziedziczone", opis, err)
		}
	}
	if string(d.Bytes()) != źródłoOpisów {
		t.Error("plik zmieniony mimo odmowy")
	}
}

func TestUstawOpisNieMaUsługi(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawOpisUsługi("G", "Nie ma", "x"); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("err = %v", err)
	}
}

// ── tagi: splice ─────────────────────────────────────────────────────────────

func TestUstawTagiNowyKluczDomyślnieFlow(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawTagiUsługi("G", "A", []string{"media", "4k"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "        url: http://a\n        tags: [media, 4k]\n") {
		t.Fatalf("lista flow nie wylądowała po url:\n%s", d.Bytes())
	}
	if got := usługa(t, d.Bytes(), "A").Tags; !reflect.DeepEqual([]string(got), []string{"media", "4k"}) {
		t.Errorf("Load: %v", got)
	}
}

func TestUstawTagiNowyKluczBlokowoGdyPlikTakPisze(t *testing.T) {
	// w pliku jedyną listą tagów jest blokowa (usługa C) — nowa też ma być blokowa
	d := parsuj(t, strings.ReplaceAll(źródłoOpisów, "        tags: [x, y]   # i ten też\n", ""))
	if err := d.UstawTagiUsługi("G", "A", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "        url: http://a\n        tags:\n          - a\n          - b\n") {
		t.Fatalf("lista blokowa:\n%s", d.Bytes())
	}
	if got := usługa(t, d.Bytes(), "A").Tags; !reflect.DeepEqual([]string(got), []string{"a", "b"}) {
		t.Errorf("Load: %v", got)
	}
}

func TestUstawTagiPodmieniaFlowZachowujeKomentarz(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawTagiUsługi("G", "B", []string{"z", "do not: panic"}); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, `tags: [z, "do not: panic"]   # i ten też`) {
		t.Fatalf("flow nie podmieniony w miejscu:\n%s", got)
	}
	if usługa(t, d.Bytes(), "B").Tags[1] != "do not: panic" {
		t.Error("round-trip tagu ze znakami specjalnymi")
	}
}

func TestUstawTagiCytujeZnakiFlow(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	tagi := []string{"a,b", "c]d", "{e}", "yes", "123"}
	if err := d.UstawTagiUsługi("G", "A", tagi); err != nil {
		t.Fatal(err)
	}
	if got := usługa(t, d.Bytes(), "A").Tags; !reflect.DeepEqual([]string(got), tagi) {
		t.Errorf("round-trip: %v\n%s", got, d.Bytes())
	}
}

func TestUstawTagiPodmieniaListęBlokową(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawTagiUsługi("G", "C", []string{"trzy"}); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, "        tags:\n          - trzy\n        check:\n          interval: 5s\n") {
		t.Fatalf("lista blokowa:\n%s", got)
	}
	if strings.Contains(got, "jeden") || strings.Contains(got, "dwa") {
		t.Error("stare pozycje zostały")
	}
}

func TestUstawTagiPustaUsuwaKlucz(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	for _, n := range []string{"B", "C"} {
		if err := d.UstawTagiUsługi("G", n, nil); err != nil {
			t.Fatal(err)
		}
		if s := usługa(t, d.Bytes(), n); len(s.Tags) != 0 {
			t.Errorf("%s: tagi zostały: %v\n%s", n, s.Tags, d.Bytes())
		}
	}
	if !strings.Contains(string(d.Bytes()), "        check:\n          interval: 5s\n") {
		t.Errorf("C straciła check:\n%s", d.Bytes())
	}
}

func TestUstawTagiOdmawiaGdyTagiPrzychodząZMerge(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	for _, tagi := range [][]string{nil, {"moje"}} {
		if err := d.UstawTagiUsługi("G", "E", tagi); !errors.Is(err, ErrPoleDziedziczone) {
			t.Errorf("tagi %v: err = %v, chcę ErrPoleDziedziczone", tagi, err)
		}
	}
	if string(d.Bytes()) != źródłoOpisów {
		t.Error("plik zmieniony mimo odmowy")
	}
}

func TestUsługaBezMergeMożeDostaćOpisIUsunąćGo(t *testing.T) {
	// merge niesie TYLKO `check`, więc opis i tagi są lokalne — bez odmowy
	src := `defaults: &d
  check: {interval: 5s}
groups:
  - name: G
    services:
      - name: S
        url: http://s
        <<: *d
`
	d := parsuj(t, src)
	if err := d.UstawOpisUsługi("G", "S", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := d.UstawTagiUsługi("G", "S", []string{"t"}); err != nil {
		t.Fatal(err)
	}
	s := usługa(t, d.Bytes(), "S")
	if s.Description != "ok" || len(s.Tags) != 1 || s.Check.Interval == 0 {
		t.Errorf("%+v\n%s", s, d.Bytes())
	}
	if err := d.UstawOpisUsługi("G", "S", ""); err != nil {
		t.Fatal(err)
	}
	if usługa(t, d.Bytes(), "S").Description != "" {
		t.Error("opis nie usunięty")
	}
}

func TestUstawTagiOdrzucaZłeDane(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	if err := d.UstawTagiUsługi("G", "A", []string{"a\nb"}); !errors.Is(err, ErrTagiZły) {
		t.Errorf("err = %v", err)
	}
	if string(d.Bytes()) != źródłoOpisów {
		t.Error("plik zmieniony mimo błędu")
	}
}

// ── pozostałe miejsca, które muszą znać nowe pola ────────────────────────────

func TestAddServiceZapisujeOpisITagi(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	err := d.AddService("G", Service{Name: "Nowa", URL: "http://n", Description: "Opis: x", Tags: Tagi{"a", "b c"}})
	if err != nil {
		t.Fatal(err)
	}
	s := usługa(t, d.Bytes(), "Nowa")
	if s.Description != "Opis: x" || !reflect.DeepEqual([]string(s.Tags), []string{"a", "b c"}) {
		t.Errorf("%+v\n%s", s, d.Bytes())
	}
}

func TestPrzenoszenieUsługiBierzeOpisITagiZSobą(t *testing.T) {
	src := źródłoOpisów + `  - name: H
    services: []
`
	d := parsuj(t, src)
	if err := d.MoveService("G", "B", "H"); err != nil {
		t.Fatal(err)
	}
	s := usługa(t, d.Bytes(), "B")
	if s.Description != "Cytowany opis" || len(s.Tags) != 2 {
		t.Errorf("%+v\n%s", s, d.Bytes())
	}
}
