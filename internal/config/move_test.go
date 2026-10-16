package config

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

// plik testowy: komentarze nad wpisami, pola nieznane panelowi, puste linie
// między usługami, pusta grupa i stopka w kolumnie 0.
const ruchSrc = `title: T

groups:
  # Media — pierwsza
  - name: Media
    services:
      # komentarz A
      - name: A
        url: http://a
        extra: "nieznane pole"

      - name: B   # koniec linii B
        url: http://b

      - name: C
        url: http://c

  - name: Sieć
    services:
      - name: D
        url: http://d
      - name: E
        url: http://e

  # Pusta
  - name: Pusta
    services: []

# stopka
`

func układ(t *testing.T, src []byte) string {
	t.Helper()
	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("wynik się nie parsuje: %v\n%s", err, src)
	}
	var parts []string
	for _, g := range cfg.Groups {
		var n []string
		for _, s := range g.Services {
			n = append(n, s.Name)
		}
		parts = append(parts, g.Name+":"+strings.Join(n, ","))
	}
	return strings.Join(parts, " | ")
}

func TestMoveServiceTo(t *testing.T) {
	cases := []struct {
		nazwa                string
		z, usł, do           string
		idx                  int
		chcę                 string
		komentarzPrzyUsłudze string // fragment, który musi stać zaraz nad wpisem po ruchu
	}{
		{"w grupie: w dół do środka", "Media", "A", "Media", 1, "Media:B,A,C | Sieć:D,E | Pusta:", ""},
		{"w grupie: na koniec", "Media", "A", "Media", 2, "Media:B,C,A | Sieć:D,E | Pusta:", ""},
		{"w grupie: ostatnia na początek", "Media", "C", "Media", 0, "Media:C,A,B | Sieć:D,E | Pusta:", ""},
		{"w grupie: w górę o jeden", "Media", "C", "Media", 1, "Media:A,C,B | Sieć:D,E | Pusta:", ""},
		{"w grupie bez pustych linii: w górę", "Sieć", "E", "Sieć", 0, "Media:A,B,C | Sieć:E,D | Pusta:", ""},
		{"między grupami: na początek", "Media", "B", "Sieć", 0, "Media:A,C | Sieć:B,D,E | Pusta:", ""},
		{"między grupami: środek", "Media", "A", "Sieć", 1, "Media:B,C | Sieć:D,A,E | Pusta:", "# komentarz A"},
		{"między grupami: koniec", "Sieć", "D", "Media", 3, "Media:A,B,C,D | Sieć:E | Pusta:", ""},
		{"do pustej grupy", "Media", "C", "Pusta", 0, "Media:A,B | Sieć:D,E | Pusta:C", ""},
		{"ostatnia usługa z grupy do innej", "Sieć", "D", "Pusta", 0, "Media:A,B,C | Sieć:E | Pusta:D", ""},
	}
	for _, c := range cases {
		t.Run(c.nazwa, func(t *testing.T) {
			d, err := Parse([]byte(ruchSrc))
			if err != nil {
				t.Fatal(err)
			}
			if err := d.MoveServiceTo(c.z, c.usł, c.do, c.idx); err != nil {
				t.Fatal(err)
			}
			out := d.Bytes()
			if got := układ(t, out); got != c.chcę {
				t.Fatalf("układ = %s\nchcę    = %s\n%s", got, c.chcę, out)
			}
			s := string(out)
			// nic nie ginie: komentarze, nieznane pole, stopka, komentarze grup
			for _, f := range []string{"# komentarz A", "# koniec linii B", `extra: "nieznane pole"`, "# stopka", "# Media — pierwsza", "# Pusta", "title: T"} {
				if strings.Count(s, f) != 1 {
					t.Errorf("fragment %q występuje %d razy\n%s", f, strings.Count(s, f), s)
				}
			}
			if c.komentarzPrzyUsłudze != "" {
				if !strings.Contains(s, "      "+c.komentarzPrzyUsłudze+"\n      - name: "+c.usł+"\n") {
					t.Errorf("komentarz nie stoi nad wpisem:\n%s", s)
				}
			}
			// żadnych podwójnych pustych linii
			if strings.Contains(s, "\n\n\n") {
				t.Errorf("podwójna pusta linia:\n%s", s)
			}
			// po ruchu w drugą stronę wracamy do oryginału (poza grupą, której styl się zmienił)
			if c.z == c.do {
				orygInd := indeksUsługi(t, []byte(ruchSrc), c.z, c.usł)
				if err := d.MoveServiceTo(c.z, c.usł, c.do, orygInd); err != nil {
					t.Fatal(err)
				}
				if string(d.Bytes()) != ruchSrc {
					t.Errorf("ruch tam i z powrotem zmienił plik:\n%s", d.Bytes())
				}
			}
		})
	}
}

func indeksUsługi(t *testing.T, src []byte, grupa, usł string) int {
	t.Helper()
	cfg, _ := Load(src)
	for _, g := range cfg.Groups {
		if g.Name != grupa {
			continue
		}
		for i, s := range g.Services {
			if s.Name == usł {
				return i
			}
		}
	}
	t.Fatalf("brak %s/%s", grupa, usł)
	return -1
}

func TestMoveServiceToWcięcieIStylPustychLinii(t *testing.T) {
	d, _ := Parse([]byte(ruchSrc))
	if err := d.MoveServiceTo("Sieć", "D", "Media", 1); err != nil {
		t.Fatal(err)
	}
	want := "      - name: A\n        url: http://a\n        extra: \"nieznane pole\"\n\n      - name: D\n        url: http://d\n\n      - name: B"
	if !strings.Contains(string(d.Bytes()), want) {
		t.Errorf("styl rozdzielania pustą linią nie przeszedł:\n%s", d.Bytes())
	}
	// do grupy bez pustych linii — wpis ma ich nie dostać
	d, _ = Parse([]byte(ruchSrc))
	if err := d.MoveServiceTo("Media", "B", "Sieć", 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "        url: http://d\n      - name: B   # koniec linii B\n        url: http://b\n      - name: E") {
		t.Errorf("wpis dostał pustą linię w grupie, która ich nie ma:\n%s", d.Bytes())
	}
}

func TestMoveServiceToWcięcieInnejGrupy(t *testing.T) {
	src := "groups:\n  - name: G1\n    services:\n      - name: A\n        url: a\n  - name: G2\n    services:\n    - name: B\n      url: b\n"
	d, _ := Parse([]byte(src))
	if err := d.MoveServiceTo("G1", "A", "G2", 0); err != nil {
		t.Fatal(err)
	}
	want := "  - name: G2\n    services:\n    - name: A\n      url: a\n    - name: B\n"
	if !strings.Contains(string(d.Bytes()), want) {
		t.Errorf("wcięcie nie przeliczone pod cel:\n%s", d.Bytes())
	}
	if got := układ(t, d.Bytes()); got != "G1: | G2:A,B" {
		t.Errorf("układ = %s", got)
	}
}

func TestMoveServiceToNoOpBajtIdentyczne(t *testing.T) {
	d, _ := Parse([]byte(ruchSrc))
	for _, c := range []struct {
		z, u string
		i    int
	}{{"Media", "A", 0}, {"Media", "B", 1}, {"Media", "C", 2}, {"Sieć", "E", 1}} {
		if err := d.MoveServiceTo(c.z, c.u, c.z, c.i); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(d.Bytes(), []byte(ruchSrc)) {
			t.Fatalf("no-op %v zmienił bajty", c)
		}
	}
}

func TestMoveServiceToBłędy(t *testing.T) {
	cases := []struct {
		nazwa    string
		z, u, do string
		idx      int
		chcęBłąd error // nil = dowolny
	}{
		{"indeks za duży (w grupie)", "Media", "A", "Media", 3, errIndeks},
		{"indeks za duży (do innej)", "Media", "A", "Sieć", 3, errIndeks},
		{"indeks ujemny", "Media", "A", "Sieć", -1, errIndeks},
		{"pusta grupa, indeks 1", "Media", "A", "Pusta", 1, errIndeks},
		{"brak usługi", "Media", "Zzz", "Sieć", 0, ErrServiceNotFound},
		{"brak grupy źródłowej", "Nope", "A", "Sieć", 0, ErrServiceNotFound},
		{"brak grupy docelowej", "Media", "A", "Nope", 0, ErrGroupNotFound},
	}
	for _, c := range cases {
		t.Run(c.nazwa, func(t *testing.T) {
			d, _ := Parse([]byte(ruchSrc))
			err := d.MoveServiceTo(c.z, c.u, c.do, c.idx)
			if err == nil {
				t.Fatal("chcę błąd")
			}
			if c.chcęBłąd != nil && !errors.Is(err, c.chcęBłąd) && !errors.Is(err, ErrGroupNotFound) {
				t.Errorf("err = %v", err)
			}
			if string(d.Bytes()) != ruchSrc {
				t.Error("błąd, a plik się zmienił")
			}
		})
	}
	// duplikat nazwy w grupie docelowej
	src := strings.Replace(ruchSrc, "name: D", "name: A", 1)
	d, _ := Parse([]byte(src))
	if err := d.MoveServiceTo("Media", "A", "Sieć", 0); err == nil {
		t.Error("duplikat powinien dać błąd")
	}
}

func TestMoveServiceDelegujeNaKoniec(t *testing.T) {
	d, _ := Parse([]byte(ruchSrc))
	if err := d.MoveService("Media", "A", "Sieć"); err != nil {
		t.Fatal(err)
	}
	if got := układ(t, d.Bytes()); got != "Media:B,C | Sieć:D,E,A | Pusta:" {
		t.Errorf("układ = %s", got)
	}
}

func TestMoveGroup(t *testing.T) {
	cases := []struct {
		nazwa, grupa string
		idx          int
		chcę         string
	}{
		{"pierwsza na koniec", "Media", 2, "Sieć:D,E | Pusta: | Media:A,B,C"},
		{"pierwsza o jeden w dół", "Media", 1, "Sieć:D,E | Media:A,B,C | Pusta:"},
		{"ostatnia na początek", "Pusta", 0, "Pusta: | Media:A,B,C | Sieć:D,E"},
		{"środkowa na początek", "Sieć", 0, "Sieć:D,E | Media:A,B,C | Pusta:"},
		{"środkowa na koniec", "Sieć", 2, "Media:A,B,C | Pusta: | Sieć:D,E"},
	}
	for _, c := range cases {
		t.Run(c.nazwa, func(t *testing.T) {
			d, _ := Parse([]byte(ruchSrc))
			if err := d.MoveGroup(c.grupa, c.idx); err != nil {
				t.Fatal(err)
			}
			out := string(d.Bytes())
			if got := układ(t, d.Bytes()); got != c.chcę {
				t.Fatalf("układ = %s\nchcę    = %s\n%s", got, c.chcę, out)
			}
			for _, f := range []string{"# komentarz A", "# koniec linii B", `extra: "nieznane pole"`, "# stopka", "# Media — pierwsza", "# Pusta", "title: T"} {
				if strings.Count(out, f) != 1 {
					t.Errorf("fragment %q występuje %d razy\n%s", f, strings.Count(out, f), out)
				}
			}
			// komentarz grupy jedzie z grupą
			if i := strings.Index(out, "# Media — pierwsza"); !strings.HasPrefix(out[i:], "# Media — pierwsza\n  - name: Media\n") {
				t.Errorf("komentarz oderwany od grupy Media:\n%s", out)
			}
			if i := strings.Index(out, "# Pusta"); !strings.HasPrefix(out[i:], "# Pusta\n  - name: Pusta\n") {
				t.Errorf("komentarz oderwany od grupy Pusta:\n%s", out)
			}
			if strings.Contains(out, "\n\n\n") || !strings.HasSuffix(out, "\n# stopka\n") {
				t.Errorf("odstępy albo stopka:\n%s", out)
			}
			// z powrotem na miejsce = oryginał
			orig := slices.Index([]string{"Media", "Sieć", "Pusta"}, c.grupa)
			if err := d.MoveGroup(c.grupa, orig); err != nil {
				t.Fatal(err)
			}
			if string(d.Bytes()) != ruchSrc {
				t.Errorf("tam i z powrotem zmieniło plik:\n%s", d.Bytes())
			}
		})
	}
}

func TestMoveGroupNoOpIBłędy(t *testing.T) {
	d, _ := Parse([]byte(ruchSrc))
	if err := d.MoveGroup("Sieć", 1); err != nil || string(d.Bytes()) != ruchSrc {
		t.Fatalf("no-op: err=%v, bajty zmienione=%v", err, string(d.Bytes()) != ruchSrc)
	}
	if err := d.MoveGroup("Sieć", 3); !errors.Is(err, errIndeks) {
		t.Errorf("err = %v", err)
	}
	if err := d.MoveGroup("Sieć", -1); !errors.Is(err, errIndeks) {
		t.Errorf("err = %v", err)
	}
	if err := d.MoveGroup("Nope", 0); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestMoveGroupBezPustychLinii(t *testing.T) {
	src := "groups:\n  - name: A\n    services:\n      - name: a\n        url: x\n  - name: B\n    services:\n      - name: b\n        url: y\n"
	d, _ := Parse([]byte(src))
	if err := d.MoveGroup("A", 1); err != nil {
		t.Fatal(err)
	}
	want := "groups:\n  - name: B\n    services:\n      - name: b\n        url: y\n  - name: A\n    services:\n      - name: a\n        url: x\n"
	if string(d.Bytes()) != want {
		t.Errorf("got:\n%s", d.Bytes())
	}
}

func TestMoveServiceToBezKońcowegoNowejLinii(t *testing.T) {
	src := strings.TrimRight(ruchSrc, "\n# stopka") // kończy się na `services: []`
	src = strings.TrimSuffix(strings.TrimSuffix(ruchSrc, "\n# stopka\n"), "\n")
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveServiceTo("Media", "A", "Pusta", 0); err != nil {
		t.Fatal(err)
	}
	if got := układ(t, d.Bytes()); got != "Media:B,C | Sieć:D,E | Pusta:A" {
		t.Errorf("układ = %s\n%s", got, d.Bytes())
	}
}

func TestUstawTytuł(t *testing.T) {
	d, _ := Parse(wczytajBasic(t))
	if err := d.UstawTytuł("  Nowy tytuł "); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "title: Nowy tytuł      # komentarz na końcu linii\n") {
		t.Errorf("komentarz albo styl zginął:\n%s", d.Bytes())
	}
	if cfg, _ := Load(d.Bytes()); cfg.Title != "Nowy tytuł" {
		t.Errorf("title = %q", cfg.Title)
	}
	assertOryginalneLinieNietknięte(t, strings.Replace(string(wczytajBasic(t)), "Homelab Demo", "Nowy tytuł", 1), string(d.Bytes()))
}

func TestUstawTytułCytowanie(t *testing.T) {
	for _, v := range []string{"yes", "no", "123", "null", "a: b", "# x", "*gwiazda", "&kotwica", "!tag", "- lista", "x #y", "it's", `say "hi"`, "back\\slash", "Zażółć gęślą 🧄", "~"} {
		d, _ := Parse(wczytajBasic(t))
		if err := d.UstawTytuł(v); err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		cfg, err := Load(d.Bytes())
		if err != nil {
			t.Fatalf("%q: plik się nie parsuje: %v\n%s", v, err, d.Bytes())
		}
		if cfg.Title != v {
			t.Errorf("zapisano %q, wczytano %q", v, cfg.Title)
		}
	}
}

func TestUstawTytułGdyKluczaNie(t *testing.T) {
	src := "# nagłówek\n\n# o grupach\ngroups:\n  - name: G\n    services: []\n"
	d, _ := Parse([]byte(src))
	if err := d.UstawTytuł("Panel: dom"); err != nil {
		t.Fatal(err)
	}
	want := "# nagłówek\n\ntitle: \"Panel: dom\"\n\n# o grupach\ngroups:\n  - name: G\n    services: []\n"
	if string(d.Bytes()) != want {
		t.Errorf("got:\n%s", d.Bytes())
	}
	if cfg, _ := Load(d.Bytes()); cfg.Title != "Panel: dom" {
		t.Errorf("title = %q", cfg.Title)
	}
}

func TestUstawTytułWalidacja(t *testing.T) {
	cases := []struct {
		v   string
		err error
	}{
		{"", ErrTytułPusty}, {"   ", ErrTytułPusty},
		{strings.Repeat("a", 81), ErrTytułDługi},
		{"a\nb", ErrTytułZnaki}, {"a\x00b", ErrTytułZnaki}, {"a\tb", ErrTytułZnaki}, {"a b", ErrTytułZnaki},
	}
	for _, c := range cases {
		d, _ := Parse(wczytajBasic(t))
		if err := d.UstawTytuł(c.v); !errors.Is(err, c.err) {
			t.Errorf("%q: err = %v, chcę %v", c.v, err, c.err)
		}
		if !bytes.Equal(d.Bytes(), wczytajBasic(t)) {
			t.Errorf("%q: plik się zmienił mimo błędu", c.v)
		}
	}
	// 80 znaków wielobajtowych mieści się (limit w znakach)
	d, _ := Parse(wczytajBasic(t))
	if err := d.UstawTytuł(strings.Repeat("ż", 80)); err != nil {
		t.Errorf("80 znaków: %v", err)
	}
}

// ogon poprzedniego wpisu (zakomentowana usługa wcięta głębiej niż myślnik
// następnego elementu) nie może wędrować razem z następnym elementem.
const ruchOgonSrc = `groups:
  - name: G
    services:
      - name: B
        url: http://b
      # - name: Wyłączona
      #   url: http://old
  # komentarz H
  - name: H
    services:
      - name: X
        url: http://x
        # - ogon X
      - name: Y
        url: http://y
`

func TestMoveGroupOgonPoprzedniejGrupyZostaje(t *testing.T) {
	d, err := Parse([]byte(ruchOgonSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveGroup("H", 0); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	iH := strings.Index(got, "  # komentarz H\n  - name: H")
	iG := strings.Index(got, "  - name: G")
	iOgon := strings.Index(got, "      # - name: Wyłączona")
	if iH < 0 || iH > iG {
		t.Fatalf("komentarz H nie stoi nad H, przed G:\n%s", got)
	}
	if iOgon < iG {
		t.Fatalf("ogon grupy G uciekł nad H:\n%s", got)
	}
}

func TestMoveServiceOgonPoprzedniejUsługiZostaje(t *testing.T) {
	d, err := Parse([]byte(ruchOgonSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveServiceTo("H", "Y", "H", 0); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	iY := strings.Index(got, "- name: Y")
	iX := strings.Index(got, "- name: X")
	iOgon := strings.Index(got, "# - ogon X")
	if iY > iX {
		t.Fatalf("Y nie jest pierwsze:\n%s", got)
	}
	if iOgon < iX {
		t.Fatalf("ogon X poszedł razem z Y:\n%s", got)
	}
}

func TestRemoveServiceNieZabieraOgonaPoprzedniej(t *testing.T) {
	d, err := Parse([]byte(ruchOgonSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveService("H", "Y"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "# - ogon X") {
		t.Fatalf("ogon X zniknął razem z Y:\n%s", d.Bytes())
	}
}
