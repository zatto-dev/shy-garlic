package config

import (
	"os"
	"strings"
	"testing"
)

func wczytajWygląd(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/appearance.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// domyślnyWygląd — to, co DodajWygląd wpisuje, gdy nikt nie prosił o konkret.
func domyślnyWygląd() Appearance {
	return Appearance{
		Background: Background{Preset: DomyślnyPreset, Blur: DomyślnyBlur, Tone: DomyślnyTon},
		View:       DomyślnyWidok,
	}
}

func TestDodajWyglądWstawiaPrzedGroups(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DodajWygląd(domyślnyWygląd()); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())

	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "# Stopka: nic tu nie ma, ale komentarz ma zostać.") {
		t.Fatalf("blok wylądował pod stopką:\n%s", out)
	}
	if strings.Index(out, "appearance:") > strings.Index(out, "\ngroups:") {
		t.Fatalf("chcę bloku PRZED groups:\n%s", out)
	}
	assertOryginalneLinieNietknięte(t, string(src), out)

	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("wynik się nie parsuje: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Preset != DomyślnyPreset || cfg.Appearance.View != DomyślnyWidok {
		t.Fatalf("wczytany wygląd = %+v", cfg.Appearance)
	}
}

func TestDodajWyglądNieDubluje(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := d.DodajWygląd(domyślnyWygląd()); err != nil {
			t.Fatalf("wywołanie %d: %v", i+1, err)
		}
	}
	if n := strings.Count(string(d.Bytes()), "appearance:"); n != 1 {
		t.Fatalf("kluczy appearance = %d, chcę dokładnie 1:\n%s", n, d.Bytes())
	}
}

func TestDodajWyglądNieRuszaKomentarzaNadGroups(t *testing.T) {
	src := []byte("title: X\n\n# Tu zaczynają się grupy.\ngroups:\n  - name: A\n    services: []\n")
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DodajWygląd(domyślnyWygląd()); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "# Tu zaczynają się grupy.\ngroups:") {
		t.Fatalf("komentarz odkleił się od groups:\n%s", out)
	}
	if strings.Contains(out, "# Tu zaczynają się grupy.\nappearance:") {
		t.Fatalf("komentarz przykleił się do appearance:\n%s", out)
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
}

func TestDodajWyglądNaKońcuGdyBrakGroups(t *testing.T) {
	for _, src := range []string{"title: X\n", "title: X"} {
		d, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if err := d.DodajWygląd(domyślnyWygląd()); err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		out := string(d.Bytes())
		if strings.Contains(out, "Xappearance") || strings.Contains(out, "X\nappearance") {
			t.Fatalf("blok skleił się z ostatnią linią:\n%s", out)
		}
		cfg, err := Load(d.Bytes())
		if err != nil {
			t.Fatalf("%q nie parsuje się: %v\n%s", src, err, out)
		}
		if cfg.Title != "X" || cfg.Appearance.Background.Preset != DomyślnyPreset {
			t.Fatalf("%q dało %+v\n%s", src, cfg.Appearance, out)
		}
	}
}

func TestUstawWyglądZmieniaTylkoJednąLinię(t *testing.T) {
	src := wczytajWygląd(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UstawWygląd(map[string]string{"preset": "fog"}); err != nil {
		t.Fatal(err)
	}
	zmienione := diffLinie(t, string(src), string(d.Bytes()))
	if len(zmienione) != 1 {
		t.Fatalf("zmienionych linii = %v, chcę dokładnie 1:\n%s", zmienione, d.Bytes())
	}
	linia := strings.Split(string(d.Bytes()), "\n")[zmienione[0]]
	if !strings.Contains(linia, "fog") || !strings.Contains(linia, "# komentarz końca linii") {
		t.Fatalf("zmieniona linia = %q", linia)
	}
}

func TestUstawWyglądZapisujeBlurBezCudzysłowów(t *testing.T) {
	d, err := Parse(wczytajWygląd(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UstawWygląd(map[string]string{"blur": "24"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "blur: 24") || strings.Contains(out, `blur: "24"`) {
		t.Fatalf("chcę gołej liczby w `blur:`:\n%s", out)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Blur != 24 {
		t.Fatalf("Blur = %d, chcę 24", cfg.Appearance.Background.Blur)
	}
}

func TestSetPathRawOdmawiaWartościSpozaAllowlisty(t *testing.T) {
	cases := []struct {
		wartość string
		chcęOK  bool
	}{
		{"18", true}, {"true", true}, {"false", true}, {"0", true}, {"-3", true},
		{"1e9", false}, {"18 # x", false}, {"}", false}, {"", false}, {"a", false},
		{"1234567", false}, {"0x10", false},
	}
	for _, c := range cases {
		src := wczytajWygląd(t)
		d, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		err = d.SetPathRaw(c.wartość, "appearance", "background", "blur")
		if c.chcęOK && err != nil {
			t.Errorf("%q: %v, chcę zapisu", c.wartość, err)
			continue
		}
		if !c.chcęOK {
			if err == nil {
				t.Errorf("%q: chcę odmowy", c.wartość)
			}
			if string(d.Bytes()) != string(src) {
				t.Errorf("%q: odrzucona wartość ruszyła plik", c.wartość)
			}
		}
	}
}

func TestUstawWyglądCytujeGdyTrzeba(t *testing.T) {
	d, err := Parse(wczytajWygląd(t))
	if err != nil {
		t.Fatal(err)
	}
	// bez cudzysłowu YAML zrobiłby z tego komentarz, czyli wartość null
	if err := d.UstawWygląd(map[string]string{"preset": "#101014"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), `preset: "#101014"`) {
		t.Fatalf("chcę zacytowanej wartości:\n%s", d.Bytes())
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
	// druga edycja tej samej linii widzi cudzysłów w oryginale i przy nim zostaje
	if err := d.UstawWygląd(map[string]string{"preset": "fog"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), `preset: "fog"`) {
		t.Fatalf("styl cytowania zamigotał:\n%s", d.Bytes())
	}
}

func TestWyglądRoundTripZachowujeAnchoryIMergeKeye(t *testing.T) {
	src := wczytajWygląd(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UstawWygląd(map[string]string{"preset": "graphite", "blur": "0", "view": "list"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	for _, chcę := range []string{
		"# Nagłówek — musi przetrwać.",
		`title: "Homelab"        # cudzysłów celowo`,
		"defaults: &defaults",
		"<<: *defaults",
		"  - name: Sieć",
		"# Stopka: nic tu nie ma, ale komentarz ma zostać.",
		"# komentarz końca linii",
	} {
		if !strings.Contains(out, chcę) {
			t.Errorf("zginęło %q:\n%s", chcę, out)
		}
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Preset != "graphite" || cfg.Appearance.View != "list" {
		t.Fatalf("wygląd = %+v\n%s", cfg.Appearance, out)
	}
	// `blur: 0` to świadomy wybór (ostra tapeta), nie brak wartości
	if cfg.Appearance.Background.Blur != 0 {
		t.Fatalf("Blur = %d, chcę 0 — zero nie może być traktowane jak brak", cfg.Appearance.Background.Blur)
	}
	if len(cfg.Groups) != 1 || len(cfg.Groups[0].Services) != 1 {
		t.Fatalf("grupy się posypały: %+v", cfg.Groups)
	}
}

func TestUstawWyglądDopisujeBrakującyKlucz(t *testing.T) {
	src := wczytajWygląd(t) // nie ma w nim `image:`
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UstawWygląd(map[string]string{"preset": "photo", "image": "moje-tlo.jpg"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Image != "moje-tlo.jpg" || cfg.Appearance.Background.Preset != "photo" {
		t.Fatalf("wygląd = %+v\n%s", cfg.Appearance, out)
	}
	assertOryginalneLinieNietknięte(t, strings.ReplaceAll(string(src), "preset: nebula", "preset: photo"), out)

	// powrót do presetu kasuje linię z nazwą pliku
	if err := d.UstawWygląd(map[string]string{"preset": "fog", "image": ""}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Bytes()), "image:") {
		t.Fatalf("linia `image:` została:\n%s", d.Bytes())
	}
}

func TestUstawWyglądTolerujeMergeKey(t *testing.T) {
	src := []byte("wspolne: &w\n  preset: fog\n\nappearance:\n  <<: *w\n\ngroups: []\n")
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	// merge-key jest niewidoczny dla nawigacji po kluczach — brak ścieżki nie
	// jest błędem zapisu, ma tylko nie wywrócić serwera
	if err := d.UstawWygląd(map[string]string{"preset": "dawn"}); err != nil {
		t.Fatalf("chcę tolerancji, dostałem: %v", err)
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
}

func TestUsuńWyglądKasujeCałyBlok(t *testing.T) {
	d, err := Parse(wczytajWygląd(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UsuńWygląd(); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	for _, niechcę := range []string{"appearance:", "background:", "view: bento"} {
		if strings.Contains(out, niechcę) {
			t.Errorf("%q zostało po usunięciu:\n%s", niechcę, out)
		}
	}
	for _, chcę := range []string{"# Nagłówek — musi przetrwać.", "defaults: &defaults", "groups:", "# Stopka"} {
		if !strings.Contains(out, chcę) {
			t.Errorf("usuwanie zabrało za dużo — zginęło %q:\n%s", chcę, out)
		}
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
}

func TestLoadUzupełniaIProstujeWygląd(t *testing.T) {
	cfg, err := Load([]byte("title: X\n"))
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Appearance
	if w.Background.Preset != "aurora" || w.Background.Blur != 10 ||
		w.Background.Tone != "dark" || w.View != "grid" || w.Background.Image != "" {
		t.Fatalf("domyślny wygląd = %+v", w)
	}

	śmieci := []byte("appearance:\n  background:\n    preset: <script>\n    image: ../../etc/passwd\n" +
		"    blur: 999\n    tone: xx\n  view: hocus\n")
	cfg, err = Load(śmieci)
	if err != nil {
		t.Fatalf("Load nie ma prawa się wywrócić na bzdurze w pliku: %v", err)
	}
	w = cfg.Appearance
	if !Dozwolone(w.Background.Preset, PresetyTła) || !Dozwolone(w.Background.Tone, TonyTła) ||
		!Dozwolone(w.View, Widoki) {
		t.Fatalf("wartość spoza allowlisty wyszła z Load: %+v", w)
	}
	if w.Background.Blur < 0 || w.Background.Blur > MaxBlur {
		t.Errorf("Blur = %d, chcę zakresu 0..%d", w.Background.Blur, MaxBlur)
	}
	if w.Background.Image != "" {
		t.Errorf("Image = %q, chcę pustej — nazwa nie przeszła regexu", w.Background.Image)
	}
}

func TestRenderServiceZapisujeRozmiarTylkoGdyPodany(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Sieć", Service{Name: "Bez", URL: "http://bez"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Bytes()), "size:") {
		t.Fatalf("pusty rozmiar nie ma trafiać do pliku:\n%s", d.Bytes())
	}
	if err := d.AddService("Sieć", Service{Name: "Duży", URL: "http://duzy", Size: "lg"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(d.Bytes()), "size:"); n != 1 {
		t.Fatalf("linii `size:` = %d, chcę 1:\n%s", n, d.Bytes())
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
	usługi := cfg.Groups[1].Services
	if usługi[1].Size != DomyślnyRozmiar {
		t.Errorf("bez rozmiaru chcę %q, mam %q", DomyślnyRozmiar, usługi[1].Size)
	}
	if usługi[2].Size != "lg" {
		t.Errorf("rozmiar = %q, chcę lg", usługi[2].Size)
	}
}

func TestSetServiceFieldDopisujeBrakującePole(t *testing.T) {
	src := wczytajWygląd(t) // Router nie ma ani ikony, ani rozmiaru
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Sieć", "Router", "size", "sm"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
	if cfg.Groups[0].Services[0].Size != "sm" {
		t.Fatalf("rozmiar = %q, chcę sm:\n%s", cfg.Groups[0].Services[0].Size, d.Bytes())
	}
	assertOryginalneLinieNietknięte(t, string(src), string(d.Bytes()))
}

// Regresja: jasny preset z brakującym albo bzdurnym tonem musi dostać ton
// jasny. Wcześniej wpadał na stałą "dark" — biały tekst na kremowym tle.
func TestLoadBierzeTonZPresetu(t *testing.T) {
	przypadki := []struct {
		nazwa    string
		yaml     string
		chcęTon  string
		chcęPre  string
	}{
		{"dawn bez tonu", "appearance:\n  background:\n    preset: dawn\n", "light", "dawn"},
		{"fog bez tonu", "appearance:\n  background:\n    preset: fog\n", "light", "fog"},
		{"aurora bez tonu", "appearance:\n  background:\n    preset: aurora\n", "dark", "aurora"},
		{"dawn z bzdurą", "appearance:\n  background:\n    preset: dawn\n    tone: różowy\n", "light", "dawn"},
		{"jawny ton przegrywa z presetem", "appearance:\n  background:\n    preset: dawn\n    tone: dark\n", "light", "dawn"},
		{"photo bez pliku wraca do presetu i jego tonu", "appearance:\n  background:\n    preset: photo\n    tone: light\n", "dark", DomyślnyPreset},
		{"pusty blok", "appearance:\n  background: {}\n", "dark", DomyślnyPreset},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			cfg, err := Load([]byte(p.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Appearance.Background.Tone; got != p.chcęTon {
				t.Errorf("ton = %q, chcę %q", got, p.chcęTon)
			}
			if got := cfg.Appearance.Background.Preset; got != p.chcęPre {
				t.Errorf("preset = %q, chcę %q", got, p.chcęPre)
			}
		})
	}
}

// Każdy preset poza `photo` musi mieć ton — inaczej TonDla cicho zwróci
// domyślny i regresja wróci przy dopisaniu nowego tła.
func TestKażdyPresetMaTon(t *testing.T) {
	for _, p := range PresetyTła {
		if p == PresetPhoto {
			continue
		}
		if _, ok := TonPresetu[p]; !ok {
			t.Errorf("preset %q nie ma tonu w TonPresetu", p)
		}
	}
	for p := range TonPresetu {
		if !Dozwolone(p, PresetyTła) {
			t.Errorf("TonPresetu zna preset %q, którego nie ma w PresetyTła", p)
		}
	}
}
