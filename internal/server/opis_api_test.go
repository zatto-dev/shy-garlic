package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

const configOpisy = `# nagłówek
defaults: &defaults
  description: Z defaults
  tags: [wspólny]

groups:
  - name: Media
    services:
      - name: Jellyfin
        url: http://jellyfin.home.arpa
      - name: Sonarr
        url: http://sonarr.home.arpa
        description: "Seriale"   # zostaje
        tags: [tv, "4k"]
      - name: Dziedziczy
        url: http://d.home.arpa
        <<: *defaults
`

func serwerOpisy(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s := serwerTestowy(t)
	if err := os.WriteFile(s.path, []byte(configOpisy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), ciasteczkoZalogowanego(t, s)
}

func usługaZPliku(t *testing.T, s *Server, nazwa string) config.Service {
	t.Helper()
	cfg, err := config.Load(s.plikConfigu(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range cfg.Groups {
		for _, u := range g.Services {
			if u.Name == nazwa {
				return u
			}
		}
	}
	t.Fatalf("brak usługi %s", nazwa)
	return config.Service{}
}

func TestPatchUsługiUstawiaOpisITagi(t *testing.T) {
	s, h, c := serwerOpisy(t)
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Jellyfin","url":"http://jellyfin.home.arpa","description":"Filmy i muzyka","tags":["media","Media","  4k "]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	u := usługaZPliku(t, s, "Jellyfin")
	if u.Description != "Filmy i muzyka" || !reflect.DeepEqual([]string(u.Tags), []string{"media", "4k"}) {
		t.Fatalf("%+v", u)
	}
	// wpis w historii z opisem zmiany
	if v, _ := s.st.Wersje(s.ctx, 1); len(v) != 1 {
		t.Error("brak wpisu w historii")
	}
}

func TestPatchUsługiBezPólOpisuNieRuszaOpisuITagów(t *testing.T) {
	s, h, c := serwerOpisy(t)
	// zmiana samego adresu — opis i tagi (i komentarz) mają zostać co do bajta
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Sonarr","url":"http://sonarr2.home.arpa"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(string(s.plikConfigu(t)), `description: "Seriale"   # zostaje`) {
		t.Errorf("opis ruszony:\n%s", s.plikConfigu(t))
	}
	if u := usługaZPliku(t, s, "Sonarr"); len(u.Tags) != 2 {
		t.Errorf("tagi ruszone: %v", u.Tags)
	}
}

func TestPatchUsługiPustyOpisITagiUsuwają(t *testing.T) {
	s, h, c := serwerOpisy(t)
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Sonarr","url":"http://sonarr.home.arpa","description":"","tags":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if u := usługaZPliku(t, s, "Sonarr"); u.Description != "" || len(u.Tags) != 0 {
		t.Errorf("%+v\n%s", u, s.plikConfigu(t))
	}
}

func TestPatchUsługiOdrzucaZłyOpisITagiPoLudzku(t *testing.T) {
	s, h, c := serwerOpisy(t)
	przed := string(s.plikConfigu(t))
	długi := strings.Repeat("a", config.MaksOpisu+1)
	cases := []struct{ nazwa, ciało, chcę string }{
		{"opis za długi", `{"description":"` + długi + `"}`, "blad_opis_dlugi"},
		{"opis ze znakiem sterującym", `{"description":"a\nb"}`, "blad_opis_znaki"},
		{"tag za długi", `{"tags":["` + strings.Repeat("t", config.MaksTagu+1) + `"]}`, "blad_tagi"},
		{"za dużo tagów", `{"tags":["a","b","c","d","e","f","g","h","i","j","k","l","m"]}`, "blad_tagi"},
	}
	for _, tc := range cases {
		ciało := strings.Replace(tc.ciało, "{", `{"group":"Media","name":"Jellyfin","url":"http://jellyfin.home.arpa",`, 1)
		w := wyślij(h, c, "PATCH", "/api/services", ciało, "Accept-Language", "pl")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: kod %d", tc.nazwa, w.Code)
			continue
		}
		var odp struct{ Błąd string `json:"error"` }
		_ = json.Unmarshal(w.Body.Bytes(), &odp)
		if odp.Błąd != i18n.T(i18n.PL, tc.chcę) {
			t.Errorf("%s: komunikat %q, chcę %q", tc.nazwa, odp.Błąd, i18n.T(i18n.PL, tc.chcę))
		}
	}
	if string(s.plikConfigu(t)) != przed {
		t.Error("plik zmieniony mimo odrzuconych żądań")
	}
}

func TestPatchUsługiOpisZMergeDaje400ZWyjaśnieniem(t *testing.T) {
	s, h, c := serwerOpisy(t)
	przed := string(s.plikConfigu(t))
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Dziedziczy","url":"http://d.home.arpa","description":"Mój"}`,
		"Accept-Language", "en")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	var odp struct{ Błąd string `json:"error"` }
	_ = json.Unmarshal(w.Body.Bytes(), &odp)
	if odp.Błąd != i18n.T(i18n.EN, "blad_pole_dziedziczone") {
		t.Errorf("komunikat: %s", w.Body)
	}
	if string(s.plikConfigu(t)) != przed {
		t.Error("plik zmieniony mimo odmowy")
	}
}

func TestPostUsługiZapisujeOpisITagi(t *testing.T) {
	s, h, c := serwerOpisy(t)
	w := wyślij(h, c, "POST", "/api/services",
		`{"group":"Media","name":"Radarr","url":"http://radarr.home.arpa","description":"Filmy: kolekcja","tags":["film","4k"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	u := usługaZPliku(t, s, "Radarr")
	if u.Description != "Filmy: kolekcja" || !reflect.DeepEqual([]string(u.Tags), []string{"film", "4k"}) {
		t.Errorf("%+v", u)
	}
}

func TestStronaGłównaPokazujeOpisITagiIWyszukujePoNich(t *testing.T) {
	s, _, _ := serwerOpisy(t)
	s.mu.Lock()
	s.cfg.Groups[0].Services[1].Description = `Seriale <b>"x"</b>`
	s.mu.Unlock()
	rec := httptest.NewRecorder()
	s.handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	html := rec.Body.String()

	if strings.Contains(html, `Seriale <b>`) {
		t.Error("opis nie został zescape'owany")
	}
	// opis i tagi są w indeksie wyszukiwania, obok nazwy i hosta
	re := `data-szukaj="Sonarr sonarr.home.arpa Seriale`
	if !strings.Contains(html, re) {
		t.Errorf("brak opisu w data-szukaj:\n%s", html[strings.Index(html, `data-nazwa="Sonarr"`):][:400])
	}
	for _, chcę := range []string{`class="opis-uslugi"`, `class="tagi"`, `class="tag"`, `>4k<`} {
		if !strings.Contains(html, chcę) {
			t.Errorf("w HTML-u brak %s", chcę)
		}
	}
	// usługa bez opisu i tagów nie dostaje pustych elementów
	i := strings.Index(html, `data-nazwa="Jellyfin"`)
	j := strings.Index(html[i:], `</li>`)
	if strings.Contains(html[i:i+j], `class="opis-uslugi"`) || strings.Contains(html[i:i+j], `class="tagi"`) {
		t.Error("pusty opis/tagi wyrenderowane dla Jellyfin")
	}
}

func TestApiServiceZwracaOpisITagi(t *testing.T) {
	_, h, c := serwerOpisy(t)
	w := wyślij(h, c, "GET", "/api/service?key="+"Media%2FSonarr", "")
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	var odp struct {
		Opis string   `json:"description"`
		Tagi []string `json:"tags"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &odp); err != nil {
		t.Fatal(err)
	}
	if odp.Opis != "Seriale" || !reflect.DeepEqual(odp.Tagi, []string{"tv", "4k"}) {
		t.Errorf("%+v", odp)
	}
}

func TestOpisITagiMajaTłumaczenia(t *testing.T) {
	klucze := []string{"opis_uslugi", "opis_uslugi_placeholder", "tagi_uslugi", "tagi_placeholder",
		"blad_opis_dlugi", "blad_opis_znaki", "blad_tagi", "blad_pole_dziedziczone"}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Mapa(l)
		for _, k := range klucze {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: brak tekstu %q", l, k)
			}
		}
	}
}
