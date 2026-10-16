package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// GH #6: edycja usługi gubiła zmiany w sekcji „Sprawdzanie".

const źródłoSprawdzania = `defaults: &defaults
  check:
    insecure: true

groups:
  - name: G
    services:
      - name: Goła
        url: https://a.home.arpa
      - name: Blok
        url: https://b.home.arpa
        check:
          interval: 1m   # rzadziej, bo wolna
          expect:
            - 200
            - 302
          path: /health
      - name: Flow
        url: https://c.home.arpa
        check: {timeout: 10s, insecure: true}
      - name: Dziedziczy
        url: https://d.home.arpa
        <<: *defaults
      - name: Tylko
        url: https://e.home.arpa
        check:
          insecure: true
`

func tak() *bool                     { v := true; return &v }
func nie() *bool                     { v := false; return &v }
func czas(d time.Duration) *Duration { v := Duration(d); return &v }
func kody(k ...int) *[]int           { return &k }

func TestUstawSprawdzanieDopisujeBlokGdyGoNieMa(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	if err := d.UstawSprawdzanie("G", "Goła", ZmianaSprawdzania{Insecure: tak()}); err != nil {
		t.Fatal(err)
	}
	want := "        url: https://a.home.arpa\n        check:\n          insecure: true\n      - name: Blok"
	if !strings.Contains(string(d.Bytes()), want) {
		t.Fatalf("brak bloku check:\n%s", d.Bytes())
	}
	if !usługa(t, d.Bytes(), "Goła").Check.Insecure {
		t.Fatal("insecure nie wczytał się")
	}
}

func TestUstawSprawdzaniePodmieniaIDopisujeWBloku(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	err := d.UstawSprawdzanie("G", "Blok", ZmianaSprawdzania{
		Interval: czas(2 * time.Minute), Expect: kody(200, 401), Insecure: tak()})
	if err != nil {
		t.Fatal(err)
	}
	src := string(d.Bytes())
	for _, w := range []string{"interval: 2m0s   # rzadziej, bo wolna", "expect: [200, 401]",
		"insecure: true", "path: /health"} {
		if !strings.Contains(src, w) {
			t.Errorf("brak %q:\n%s", w, src)
		}
	}
	c := usługa(t, d.Bytes(), "Blok").Check
	if c.Interval != Duration(2*time.Minute) || !reflect.DeepEqual(c.Expect, []int{200, 401}) ||
		!c.Insecure || c.Path != "/health" {
		t.Fatalf("%+v", c)
	}
}

func TestUstawSprawdzanieOdznaczenieUsuwaKlucz(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	if err := d.UstawSprawdzanie("G", "Blok", ZmianaSprawdzania{Interval: czas(0), Expect: kody()}); err != nil {
		t.Fatal(err)
	}
	src := string(d.Bytes())
	if strings.Contains(src, "rzadziej") || strings.Contains(src, "- 302") {
		t.Fatalf("klucze zostały:\n%s", src)
	}
	if c := usługa(t, d.Bytes(), "Blok").Check; c.Path != "/health" || c.Interval != Duration(DefaultInterval) {
		t.Fatalf("%+v", c)
	}
}

func TestUstawSprawdzanieOstatniKluczZabieraBlok(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	if err := d.UstawSprawdzanie("G", "Tylko", ZmianaSprawdzania{Insecure: nie()}); err != nil {
		t.Fatal(err)
	}
	if want := "        url: https://e.home.arpa\n"; !strings.HasSuffix(string(d.Bytes()), want) {
		t.Fatalf("pusty check został:\n%s", d.Bytes())
	}
}

func TestUstawSprawdzanieFlowPrzepisujeBlok(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	if err := d.UstawSprawdzanie("G", "Flow", ZmianaSprawdzania{Insecure: nie()}); err != nil {
		t.Fatal(err)
	}
	c := usługa(t, d.Bytes(), "Flow").Check
	if c.Insecure || c.Timeout != Duration(10*time.Second) {
		t.Fatalf("%+v\n%s", c, d.Bytes())
	}
}

func TestUstawSprawdzanieDziedziczoneOdmawia(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	przed := string(d.Bytes())
	err := d.UstawSprawdzanie("G", "Dziedziczy", ZmianaSprawdzania{Interval: czas(time.Minute)})
	if !errors.Is(err, ErrPoleDziedziczone) {
		t.Fatalf("err = %v", err)
	}
	if string(d.Bytes()) != przed {
		t.Fatal("plik się zmienił")
	}
}

func TestUstawSprawdzanieBezZmianNicNieRusza(t *testing.T) {
	d := parsuj(t, źródłoSprawdzania)
	przed := string(d.Bytes())
	err := d.UstawSprawdzanie("G", "Blok", ZmianaSprawdzania{
		Interval: czas(time.Minute), Expect: kody(200, 302), Insecure: nie()})
	if err != nil {
		t.Fatal(err)
	}
	if string(d.Bytes()) != przed {
		t.Fatalf("zmiana bez zmiany:\n%s", d.Bytes())
	}
}

// `interval: 1m  # komentarz` wczytywał się jako 1s — goccy podawał bajty skalara
// razem z komentarzem.
func TestDurationZKomentarzemIGołąLiczbą(t *testing.T) {
	for wpis, want := range map[string]time.Duration{
		"interval: 1m   # x":   time.Minute,
		"interval: 1m":         time.Minute,
		`interval: "45s" # x`:  45 * time.Second,
		"interval: 30":         30 * time.Second,
		"interval: 1.5   # x":  1500 * time.Millisecond,
		"interval: 2h0m0s # x": 2 * time.Hour,
	} {
		src := "groups:\n  - name: G\n    services:\n      - name: A\n        url: http://a\n        check:\n          " + wpis + "\n"
		if got := usługa(t, []byte(src), "A").Check.Interval; got != Duration(want) {
			t.Errorf("%q: %v, chcę %v", wpis, got, want)
		}
	}
}
