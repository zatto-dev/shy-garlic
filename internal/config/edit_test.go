package config

import (
	"errors"
	"strings"
	"testing"
)

// diffLinie zwraca indeksy linii, które się różnią. Wymaga równej liczby linii.
func diffLinie(t *testing.T, before, after string) []int {
	t.Helper()
	bl, al := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(bl) != len(al) {
		t.Fatalf("zmieniła się liczba linii: %d → %d", len(bl), len(al))
	}
	var out []int
	for i := range bl {
		if bl[i] != al[i] {
			out = append(out, i)
		}
	}
	return out
}

func TestSetServiceFieldZmieniaTylkoJednąLinię(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://10.0.0.5:8990"); err != nil {
		t.Fatal(err)
	}

	zmienione := diffLinie(t, string(src), string(d.Bytes()))
	if len(zmienione) != 1 {
		t.Fatalf("zmienionych linii = %v, chcę dokładnie 1", zmienione)
	}
	after := strings.Split(string(d.Bytes()), "\n")
	if !strings.Contains(after[zmienione[0]], "8990") {
		t.Fatalf("zmieniona linia = %q", after[zmienione[0]])
	}
}

func TestSetServiceFieldNieRuszaCudzysłowówAniKomentarza(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "name", "Sonarr v4"); err != nil {
		t.Fatal(err)
	}
	if got := string(d.Bytes()); !strings.Contains(got, `name: "Sonarr v4"          # cudzysłowy celowo, nie zdejmuj`) {
		t.Fatalf("styl cytowania albo komentarz zginął:\n%s", got)
	}
}

func TestSetServiceFieldCytujeGdyTrzeba(t *testing.T) {
	cases := []struct{ wartość, chcę string }{
		{"Jellyfin 2", "Jellyfin 2"},        // bez zmian, cytowanie zbędne
		{"true", `"true"`},                  // inaczej stałoby się boolem
		{"123", `"123"`},                    // inaczej liczbą
		{"a: b", `"a: b"`},                  // rozwaliłoby mapping
		{"# hash", `"# hash"`},              // stałoby się komentarzem
		{"", `""`},                          // pusta wartość
	}
	for _, c := range cases {
		d, err := Parse(wczytajBasic(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetServiceField("Media", "Jellyfin", "name", c.wartość); err != nil {
			t.Fatalf("%q: %v", c.wartość, err)
		}
		if got := string(d.Bytes()); !strings.Contains(got, "name: "+c.chcę+"\n") {
			t.Errorf("dla %q chcę `name: %s`, dostałem linię inną:\n%s", c.wartość, c.chcę, wytnijLinięZ(got, "Jellyfin"))
		}
		// wynik musi się dalej parsować
		if _, err := Load(d.Bytes()); err != nil {
			t.Errorf("po ustawieniu %q plik się nie parsuje: %v", c.wartość, err)
		}
	}
}

func wytnijLinięZ(s, igła string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, igła) {
			return l
		}
	}
	return "(nie znaleziono)"
}

func TestSetServiceFieldNieznanaUsługa(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	err = d.SetServiceField("Media", "Nie Ma Takiej", "url", "http://x")
	if !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("err = %v, chcę ErrServiceNotFound", err)
	}
	err = d.SetServiceField("Nie Ma Grupy", "Jellyfin", "url", "http://x")
	if !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, chcę ErrGroupNotFound", err)
	}
}

// assertOryginalneLinieNietknięte sprawdza, że wynik zawiera wszystkie linie
// oryginału, w tej samej kolejności — dodania są dozwolone, zmiany nie.
func assertOryginalneLinieNietknięte(t *testing.T, before, after string) {
	t.Helper()
	bl, al := strings.Split(before, "\n"), strings.Split(after, "\n")
	j := 0
	for _, line := range bl {
		for j < len(al) && al[j] != line {
			j++
		}
		if j == len(al) {
			t.Fatalf("zginęła albo zmieniła się linia: %q", line)
		}
		j++
	}
}

func TestAddServiceDopisujeNaKoniecGrupy(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	err = d.AddService("Sieć", Service{Name: "AdGuard", URL: "http://10.0.0.2:3000", Icon: "adguard-home"})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("wynik nie parsuje się: %v\n%s", err, d.Bytes())
	}
	if n := len(cfg.Groups[1].Services); n != 2 {
		t.Fatalf("usług w grupie Sieć = %d, chcę 2\n%s", n, d.Bytes())
	}
	if cfg.Groups[1].Services[1].Name != "AdGuard" {
		t.Errorf("zła nazwa: %+v", cfg.Groups[1].Services[1])
	}
	assertOryginalneLinieNietknięte(t, string(src), string(d.Bytes()))
}

func TestAddServiceUżywaWcięciaSąsiada(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Media", Service{Name: "Radarr", URL: "http://x:7878"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "\n      - name: Radarr\n        url: http://x:7878\n") {
		t.Fatalf("złe wcięcie:\n%s", out)
	}
	// grupa Media rozdziela usługi pustą linią — nowa ma iść w tym samym stylu
	if !strings.Contains(out, "<<: *defaults\n\n      - name: Radarr") {
		t.Errorf("nie zachowano pustej linii między usługami:\n%s", out)
	}
	// komentarz na końcu pliku musi zostać na końcu
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "# Stopka: nic tu nie ma, ale komentarz ma zostać.") {
		t.Errorf("stopka przestała być stopką:\n%s", out)
	}
}

func TestAddServiceOdmawiaDuplikatu(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Media", Service{Name: "Jellyfin", URL: "http://x"}); err == nil {
		t.Fatal("chcę błędu przy duplikacie nazwy")
	}
}

func TestAddServiceDoGrupyBezUsług(t *testing.T) {
	src := []byte("groups:\n  - name: Puste\n")
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Puste", Service{Name: "Nowa", URL: "http://n"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
	if len(cfg.Groups[0].Services) != 1 || cfg.Groups[0].Services[0].Name != "Nowa" {
		t.Fatalf("źle dodane:\n%s", d.Bytes())
	}
}

func TestRemoveServiceZostawiaKomentarzGrupy(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveService("Media", "Jellyfin"); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
	if len(cfg.Groups[0].Services) != 1 || cfg.Groups[0].Services[0].Name != "Sonarr" {
		t.Fatalf("źle usunięte:\n%s", out)
	}
	if !strings.Contains(out, "# Grupa mediów — najczęściej używana, ma być pierwsza") {
		t.Errorf("zginął komentarz grupy:\n%s", out)
	}
	if !strings.Contains(out, "# Stopka: nic tu nie ma, ale komentarz ma zostać.") {
		t.Errorf("zginęła stopka:\n%s", out)
	}
	if strings.Contains(out, "Jellyfin") {
		t.Errorf("usługa nie zniknęła:\n%s", out)
	}
}

func TestRemoveOstatniejUsługiWGrupie(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveService("Sieć", "Pi-hole"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
	if len(cfg.Groups) != 2 {
		t.Fatalf("grupa zniknęła:\n%s", d.Bytes())
	}
	if len(cfg.Groups[1].Services) != 0 {
		t.Fatalf("chcę pustej listy usług, mam %d:\n%s", len(cfg.Groups[1].Services), d.Bytes())
	}
}

func TestAddIRemoveGroup(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddGroup("Testy"); err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Testy", Service{Name: "Coś", URL: "http://cos"}); err != nil {
		t.Fatalf("dodanie do nowej grupy: %v\n%s", err, d.Bytes())
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, d.Bytes())
	}
	if len(cfg.Groups) != 3 || cfg.Groups[2].Name != "Testy" || len(cfg.Groups[2].Services) != 1 {
		t.Fatalf("źle dodana grupa:\n%s", d.Bytes())
	}
	if err := d.RemoveGroup("Testy"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(d.Bytes())
	if len(cfg.Groups) != 2 {
		t.Fatalf("grupa nie zniknęła:\n%s", d.Bytes())
	}
}

func TestRenameGroup(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RenameGroup("Sieć", "Network"); err != nil {
		t.Fatal(err)
	}
	zmienione := diffLinie(t, string(src), string(d.Bytes()))
	if len(zmienione) != 1 {
		t.Fatalf("zmienionych linii = %v, chcę 1", zmienione)
	}
	cfg, _ := Load(d.Bytes())
	if cfg.Groups[1].Name != "Network" || len(cfg.Groups[1].Services) != 1 {
		t.Fatalf("źle:\n%s", d.Bytes())
	}
}

func TestMoveServiceZachowujeKomentarzIPola(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveService("Media", "Sonarr", "Sieć"); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("nie parsuje się: %v\n%s", err, out)
	}
	if len(cfg.Groups[0].Services) != 1 || len(cfg.Groups[1].Services) != 2 {
		t.Fatalf("złe liczby usług: %d / %d\n%s",
			len(cfg.Groups[0].Services), len(cfg.Groups[1].Services), out)
	}
	if cfg.Groups[1].Services[1].Name != "Sonarr" {
		t.Fatalf("nie ta usługa:\n%s", out)
	}
	// komentarz z linii wpisu jedzie razem z usługą
	if !strings.Contains(out, "cudzysłowy celowo, nie zdejmuj") {
		t.Errorf("zginął komentarz usługi:\n%s", out)
	}
	// merge-key też
	if strings.Count(out, "<<: *defaults") != 2 {
		t.Errorf("zginął merge-key przy przenoszeniu:\n%s", out)
	}
	// wcięcie w nowej grupie zgodne z sąsiadem
	if !strings.Contains(out, "\n      - name: \"Sonarr\"") {
		t.Errorf("złe wcięcie po przeniesieniu:\n%s", out)
	}
}

func TestUstawWyglądDim(t *testing.T) {
	src := "appearance:\n  background:\n    preset: photo\n    image: t.jpg\n    blur: 10\ngroups: []\n"
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := d.UstawWygląd(map[string]string{"dim": "70"}); err != nil {
		t.Fatalf("UstawWygląd: %v", err)
	}
	c, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("Load po edycji: %v", err)
	}
	if c.Appearance.Background.Dim != 70 {
		t.Fatalf("chcę dim 70, mam %d", c.Appearance.Background.Dim)
	}
	// Splice ma ruszyć jedną linię, nie przepisać bloku.
	if !strings.Contains(string(d.Bytes()), "blur: 10") {
		t.Fatalf("sąsiednie klucze mają zostać nietknięte:\n%s", d.Bytes())
	}
}

// Blok appearance powstający od zera ma nieść dim, inaczej pierwszy zapis
// wyglądu gubiłby przyciemnienie ustawione w tym samym kliknięciu.
func TestDodajWyglądNiesieDim(t *testing.T) {
	d, err := Parse([]byte("title: t\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := d.UstawWygląd(map[string]string{"preset": "photo", "image": "t.jpg", "dim": "20"}); err != nil {
		t.Fatalf("UstawWygląd: %v", err)
	}
	c, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("Load po edycji: %v", err)
	}
	if c.Appearance.Background.Dim != 20 {
		t.Fatalf("chcę dim 20, mam %d — plik:\n%s", c.Appearance.Background.Dim, d.Bytes())
	}
}

func TestSetServiceFieldNazwaOdrzucaDuplikat(t *testing.T) {
	d, err := Parse([]byte(ruchSrc))
	if err != nil {
		t.Fatal(err)
	}
	przed := string(d.Bytes())
	if err := d.SetServiceField("Media", "A", "name", "B"); !errors.Is(err, ErrDuplikat) {
		t.Fatalf("err = %v, chcę ErrDuplikat", err)
	}
	if string(d.Bytes()) != przed {
		t.Error("odrzucona zmiana nazwy zmieniła plik")
	}
	// ta sama nazwa w INNEJ grupie jest dozwolona, a rename na własną nazwę to no-op
	if err := d.SetServiceField("Media", "A", "name", "D"); err != nil {
		t.Errorf("nazwa z innej grupy: %v", err)
	}
	if err := d.SetServiceField("Media", "B", "name", "B"); err != nil {
		t.Errorf("rename na siebie: %v", err)
	}
}

func TestRenameGroupDuplikatToErrDuplikat(t *testing.T) {
	d, _ := Parse([]byte(ruchSrc))
	if err := d.RenameGroup("Media", "Sieć"); !errors.Is(err, ErrDuplikat) {
		t.Fatalf("err = %v, chcę ErrDuplikat", err)
	}
}
