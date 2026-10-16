package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// SHYG-45: literówka w kluczu (`chek:`) była po cichu ignorowana. Nie może być
// błędem (plik z nieznanym polem ma się nadal wczytać), ale musi być widoczna.

func klucze(w []Ostrzeżenie) string {
	var out []string
	for _, o := range w {
		out = append(out, fmt.Sprintf("%d:%s@%s", o.Linia, o.Klucz, o.Ścieżka))
	}
	return strings.Join(out, "|")
}

func TestOstrzeżeniaNieznanyKlucz(t *testing.T) {
	src := `title: Dom
groups:
  - name: Media
    services:
      - name: Sonarr
        url: http://x
      - name: Radarr
        url: http://y
        chek:
          interval: 5s
`
	w := Ostrzeżenia([]byte(src))
	if got := klucze(w); got != "9:chek@groups[0].services[1]" {
		t.Fatalf("ostrzeżenia = %q", got)
	}
	if w[0].Sugestia != "check" {
		t.Errorf("sugestia = %q, chcę check", w[0].Sugestia)
	}
	// Load nadal przyjmuje plik — to ostrzeżenie, nie błąd
	if _, err := Load([]byte(src)); err != nil {
		t.Errorf("Load odrzucił plik z nieznanym kluczem: %v", err)
	}
}

func TestOstrzeżeniaWszystkiePoziomy(t *testing.T) {
	src := `titel: x
appearance:
  view: grid
  widok2: a
  background:
    preset: aurora
    blurr: 3
notify:
  treshold: 2
groups:
  - name: A
    nme: b
    services:
      - name: S
        check:
          intervall: 5s
          expect: [200]
`
	got := klucze(Ostrzeżenia([]byte(src)))
	chcę := "1:titel@|4:widok2@appearance|7:blurr@appearance.background|9:treshold@notify|12:nme@groups[0]|16:intervall@groups[0].services[0].check"
	if got != chcę {
		t.Fatalf("\n got %s\nchcę %s", got, chcę)
	}
}

// Wzorzec z README: `defaults: &defaults` + `<<: *defaults`, a także x-*.
func TestOstrzeżeniaBezFałszywychAlarmów(t *testing.T) {
	for _, p := range []string{"../../examples/homelab.yaml", "testdata/basic.yaml", "testdata/appearance.yaml"} {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if w := Ostrzeżenia(src); len(w) != 0 {
			t.Errorf("%s: fałszywe ostrzeżenia %s", p, klucze(w))
		}
	}
	src := `x-wspólne: &w
  interval: 10s
groups:
  - name: A
    services:
      - name: S
        check:
          <<: *w
          timeout: 2s
      - &szablon
        name: T
        url: http://t
      - <<: *szablon
        name: U
`
	if w := Ostrzeżenia([]byte(src)); len(w) != 0 {
		t.Errorf("anchory/merge keye: ostrzeżenia %s", klucze(w))
	}
}

// Literówka schowana w anchorze też jest widoczna — raz, mimo wielu merge'y.
func TestOstrzeżeniaWAnchorzePoMerge(t *testing.T) {
	src := `defaults: &d
  intervall: 5s
groups:
  - name: A
    services:
      - name: S1
        check: {<<: *d}
      - name: S2
        check: {<<: *d}
`
	w := Ostrzeżenia([]byte(src))
	if len(w) != 1 || w[0].Klucz != "intervall" || w[0].Linia != 2 {
		t.Fatalf("ostrzeżenia = %s, chcę jedno dla linii 2", klucze(w))
	}
}

func TestOstrzeżeniaNiepoprawnyPlikToPusteOstrzeżenia(t *testing.T) {
	if w := Ostrzeżenia([]byte("groups: [\n")); len(w) != 0 {
		t.Errorf("zepsuty YAML ma dać pustą listę (błąd zgłasza Load), mam %v", w)
	}
}

func TestOstrzeżeniaAdresów(t *testing.T) {
	cfg, err := Load([]byte(`groups:
  - name: G
    services:
      - name: Zly
        url: "javascript:alert(1)"
      - name: Ok
        url: https://ok.example
      - name: Tcp
        url: tcp://10.0.0.5:22
      - name: Pusty
`))
	if err != nil {
		t.Fatal(err)
	}
	w := OstrzeżeniaAdresów(cfg)
	if len(w) != 1 || w[0].Rodzaj != RodzajAdres || w[0].Usługa != "Zly" || w[0].Grupa != "G" {
		t.Fatalf("ostrzeżenia adresów = %+v", w)
	}
}

// ── numer linii błędu ────────────────────────────────────────────────────────

func TestRozbierzBłądPodajeLinię(t *testing.T) {
	for nazwa, c := range map[string]struct {
		src   string
		linia int
	}{
		"tabulator": {"title: x\ngroups:\n\t- name: A\n", 3},
		"cudzysłów": {"title: \"niezamknięty\ngroups: []\n", 1},
		"zły typ":   {"title: x\ngroups: 5\n", 2},
		"czas":      {"groups:\n  - name: A\n    services:\n      - name: S\n        check:\n          interval: abc\n", 6},
		"nawias":    {"groups: [\n  - a\n", 0}, // linia nieznana, ale komunikat jest
	} {
		t.Run(nazwa, func(t *testing.T) {
			_, err := Load([]byte(c.src))
			if err == nil {
				t.Fatal("Load przyjął zepsuty plik")
			}
			b := RozbierzBłąd(err)
			if b.Komunikat == "" {
				t.Error("pusty komunikat")
			}
			if strings.Contains(b.Komunikat, "\n") {
				t.Errorf("komunikat wielolinijkowy (wlazł fragment źródła): %q", b.Komunikat)
			}
			if c.linia > 0 && b.Linia != c.linia {
				t.Errorf("linia = %d, chcę %d (komunikat %q)", b.Linia, c.linia, b.Komunikat)
			}
		})
	}
}

// Każde pole modelu musi być w schemacie — inaczej poprawny klucz dostaje
// ostrzeżenie. Pełny plik ze wszystkimi polami nie może ich wywołać.
func TestOstrzeżeniaPokrywająModel(t *testing.T) {
	src := `title: T
appearance:
  view: grid
  background: {preset: photo, image: a.png, blur: 1, dim: 2, tone: dark}
notify: {threshold: 3}
discovery: {docker: false}
groups:
  - name: G
    services:
      - name: S
        url: http://s
        icon: i
        size: md
        check: {interval: 5s, timeout: 2s, expect: [200], insecure: true}
`
	if w := Ostrzeżenia([]byte(src)); len(w) != 0 {
		t.Fatalf("poprawne klucze zgłoszone jako nieznane: %s", klucze(w))
	}
}
