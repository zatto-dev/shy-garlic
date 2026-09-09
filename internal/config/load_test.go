package config

import (
	"os"
	"slices"
	"testing"
	"time"
)

func TestLoadCzytaGrupyIUsługi(t *testing.T) {
	src, err := os.ReadFile("testdata/basic.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Title != "Homelab Mariusza" {
		t.Errorf("Title = %q, chcę %q", cfg.Title, "Homelab Mariusza")
	}
	if len(cfg.Groups) != 2 {
		t.Fatalf("grup = %d, chcę 2", len(cfg.Groups))
	}
	if cfg.Groups[0].Name != "Media" || len(cfg.Groups[0].Services) != 2 {
		t.Errorf("grupa 0 = %+v", cfg.Groups[0])
	}

	// merge-key musi być rozwinięty przy odczycie
	sonarr := cfg.Groups[0].Services[1]
	if sonarr.Name != "Sonarr" {
		t.Errorf("nazwa = %q", sonarr.Name)
	}
	if sonarr.Check.Interval.Duration() != 30*time.Second {
		t.Errorf("interval z <<: *defaults = %v, chcę 30s", sonarr.Check.Interval)
	}
	if !slices.Equal(sonarr.Check.Expect, []int{200, 301, 302, 401, 403}) {
		t.Errorf("expect = %v", sonarr.Check.Expect)
	}

	// jawny check ma nadpisywać defaults
	pihole := cfg.Groups[1].Services[0]
	if pihole.Check.Interval.Duration() != 10*time.Second {
		t.Errorf("pihole interval = %v, chcę 10s", pihole.Check.Interval)
	}
	if !slices.Equal(pihole.Check.Expect, []int{200}) {
		t.Errorf("pihole expect = %v, chcę [200]", pihole.Check.Expect)
	}
}

func TestLoadUzupełniaDomyślne(t *testing.T) {
	cfg, err := Load([]byte("groups:\n  - name: X\n    services:\n      - name: Y\n        url: http://y\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Groups[0].Services[0].Check
	if c.Interval.Duration() != 30*time.Second {
		t.Errorf("domyślny interval = %v, chcę 30s", c.Interval)
	}
	if c.Timeout.Duration() != 5*time.Second {
		t.Errorf("domyślny timeout = %v, chcę 5s", c.Timeout)
	}
	// ! To jest cała teza produktu: 401/403 znaczy "żyje, wymaga logowania",
	// a nie "padło". Konkurencja myli te dwie rzeczy.
	if !slices.Equal(c.Expect, []int{200, 301, 302, 401, 403}) {
		t.Errorf("domyślny expect = %v", c.Expect)
	}
}

func TestDomyślnyPrógIDim(t *testing.T) {
	c, err := Load([]byte("title: t\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Notify.Threshold != DomyślnyPróg {
		t.Fatalf("chcę próg %d, mam %d", DomyślnyPróg, c.Notify.Threshold)
	}
	if c.Appearance.Background.Dim != DomyślnyDim {
		t.Fatalf("chcę dim %d, mam %d", DomyślnyDim, c.Appearance.Background.Dim)
	}
}

// dim: 0 to świadome „bez przyciemnienia", a nie brak klucza. Dokładnie ta
// sama pułapka co przy blur — zero jest wartością zerową typu.
func TestDimZeroToWybór(t *testing.T) {
	c, err := Load([]byte("appearance:\n  background:\n    preset: photo\n    image: t.jpg\n    dim: 0\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Appearance.Background.Dim != 0 {
		t.Fatalf("dim: 0 ma zostać zerem, mam %d", c.Appearance.Background.Dim)
	}
}

// Komentarz nie jest ustawieniem. Gdyby „podanie klucza" sprawdzać regexem po
// tekście pliku, ten przypadek dawałby dim: 0 zamiast domyślnego.
func TestDimWKomentarzuToNiePodanie(t *testing.T) {
	c, _ := Load([]byte("appearance:\n  background:\n    preset: aurora\n    # dim: 0\ngroups: []\n"))
	if c.Appearance.Background.Dim != DomyślnyDim {
		t.Fatalf("zakomentowany dim ma dać domyślny %d, mam %d", DomyślnyDim, c.Appearance.Background.Dim)
	}
}

func TestDimPozaZakresemProstuje(t *testing.T) {
	c, _ := Load([]byte("appearance:\n  background:\n    dim: 900\ngroups: []\n"))
	if c.Appearance.Background.Dim != DomyślnyDim {
		t.Fatalf("dim poza zakresem ma wrócić do domyślnego, mam %d", c.Appearance.Background.Dim)
	}
}

func TestPrógPoniżejJednegoProstuje(t *testing.T) {
	c, _ := Load([]byte("notify:\n  threshold: 0\ngroups: []\n"))
	if c.Notify.Threshold != DomyślnyPróg {
		t.Fatalf("próg 0 ma wrócić do domyślnego, mam %d", c.Notify.Threshold)
	}
}

// Brak klucza discovery.docker znaczy „zachowaj się jak dotąd", czyli
// podpowiadaj, jeśli gniazdo jest dostępne. Dlatego wskaźnik, nie bool.
func TestDiscoveryDomyślnieNieustawione(t *testing.T) {
	c, _ := Load([]byte("groups: []\n"))
	if c.Discovery.Docker != nil {
		t.Fatalf("bez klucza ma być nil, mam %v", *c.Discovery.Docker)
	}
	c2, _ := Load([]byte("discovery:\n  docker: false\ngroups: []\n"))
	if c2.Discovery.Docker == nil || *c2.Discovery.Docker {
		t.Fatal("docker: false ma dać wskaźnik na false")
	}
}
