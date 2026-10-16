package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/config"
)

const configKolejność = `# nagłówek — ma przetrwać
title: Dom   # komentarz tytułu

groups:
  # grupa pierwsza
  - name: Media
    services:
      # komentarz Jellyfin
      - name: Jellyfin
        extra: zostaje
      - name: Sonarr
  - name: Sieć
    services:
      - name: Pi-hole

# stopka
`

// serwerKolejność stawia panel na konfigu z komentarzami i zwraca handler
// razem z ciasteczkiem sesji — żądania idą całą ścieżką (logowanie, CSRF).
func serwerKolejność(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s := serwerTestowy(t)
	if err := os.WriteFile(s.path, []byte(configKolejność), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), ciasteczkoZalogowanego(t, s)
}

func wyślij(h http.Handler, c *http.Cookie, metoda, ścieżka, ciało string, nagłówki ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(metoda, ścieżka, strings.NewReader(ciało))
	r.AddCookie(c)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(nagłówki); i += 2 {
		r.Header.Set(nagłówki[i], nagłówki[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func układPliku(t *testing.T, s *Server) string {
	t.Helper()
	cfg, err := config.Load(s.plikConfigu(t))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range cfg.Groups {
		var n []string
		for _, u := range g.Services {
			n = append(n, u.Name)
		}
		out = append(out, g.Name+":"+strings.Join(n, ","))
	}
	return strings.Join(out, "|")
}

func TestServiceMoveWGrupieIMiędzyGrupami(t *testing.T) {
	s, h, c := serwerKolejność(t)

	w := wyślij(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","toGroup":"Media","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if got := układPliku(t, s); got != "Media:Sonarr,Jellyfin|Sieć:Pi-hole" {
		t.Fatalf("układ = %s", got)
	}

	// toGroup pominięte = ta sama grupa
	w = wyślij(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","index":1}`)
	if w.Code != http.StatusOK || układPliku(t, s) != "Media:Jellyfin,Sonarr|Sieć:Pi-hole" {
		t.Fatalf("kod %d, układ %s", w.Code, układPliku(t, s))
	}

	w = wyślij(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Jellyfin","toGroup":"Sieć","index":1}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if got := układPliku(t, s); got != "Media:Sonarr|Sieć:Pi-hole,Jellyfin" {
		t.Fatalf("układ = %s", got)
	}
	plik := string(s.plikConfigu(t))
	for _, f := range []string{"# nagłówek — ma przetrwać", "# komentarz tytułu", "# komentarz Jellyfin", "extra: zostaje", "# grupa pierwsza", "# stopka"} {
		if strings.Count(plik, f) != 1 {
			t.Errorf("%q: %d wystąpień\n%s", f, strings.Count(plik, f), plik)
		}
	}
	// komentarz jedzie z usługą, nie zostaje w starej grupie
	if !strings.Contains(plik, "      # komentarz Jellyfin\n      - name: Jellyfin\n        extra: zostaje") {
		t.Errorf("komentarz oderwany od usługi:\n%s", plik)
	}

	// każdy ruch zostawia wpis w historii (najnowszy pierwszy), z opisem
	wersje, err := s.st.Versions(s.ctx, 10)
	if err != nil || len(wersje) != 3 {
		t.Fatalf("historia: %v, %d wpisów, chcę 3", err, len(wersje))
	}
	if !strings.Contains(wersje[0].Description, "Jellyfin") || !strings.Contains(wersje[1].Description, "position 2") {
		t.Errorf("opisy historii: %q / %q", wersje[0].Description, wersje[1].Description)
	}
}

func TestServiceMoveOdrzuca(t *testing.T) {
	s, h, c := serwerKolejność(t)
	przed := string(s.plikConfigu(t))
	cases := []struct {
		nazwa, ciało string
		kod          int
	}{
		{"zły JSON", `{`, 400},
		{"brak index", `{"group":"Media","name":"Sonarr"}`, 400},
		{"pusta nazwa", `{"group":"Media","name":"","index":0}`, 400},
		{"znak sterujący", "{\"group\":\"Media\",\"name\":\"a\\nb\",\"index\":0}", 400},
		{"index za duży", `{"group":"Media","name":"Sonarr","index":5}`, 400},
		{"index ujemny", `{"group":"Media","name":"Sonarr","index":-1}`, 400},
		{"brak usługi", `{"group":"Media","name":"Nie ma","index":0}`, 404},
		{"brak grupy docelowej", `{"group":"Media","name":"Sonarr","toGroup":"Nie ma","index":0}`, 404},
	}
	for _, tc := range cases {
		w := wyślij(h, c, "POST", "/api/services/move", tc.ciało)
		if w.Code != tc.kod {
			t.Errorf("%s: kod %d (chcę %d): %s", tc.nazwa, w.Code, tc.kod, w.Body)
		}
	}
	if string(s.plikConfigu(t)) != przed {
		t.Error("odrzucone żądania zmieniły plik")
	}
}

func TestGroupMove(t *testing.T) {
	s, h, c := serwerKolejność(t)
	w := wyślij(h, c, "POST", "/api/groups/move", `{"name":"Sieć","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if got := układPliku(t, s); got != "Sieć:Pi-hole|Media:Jellyfin,Sonarr" {
		t.Fatalf("układ = %s", got)
	}
	plik := string(s.plikConfigu(t))
	if !strings.Contains(plik, "  # grupa pierwsza\n  - name: Media") || !strings.HasSuffix(plik, "# stopka\n") {
		t.Errorf("komentarz grupy albo stopka:\n%s", plik)
	}
	for _, tc := range []string{`{"name":"Sieć"}`, `{"name":"","index":0}`, `{"name":"Sieć","index":2}`, `{"name":"Sieć","index":-1}`} {
		if w := wyślij(h, c, "POST", "/api/groups/move", tc); w.Code != 400 {
			t.Errorf("%s: kod %d", tc, w.Code)
		}
	}
	if w := wyślij(h, c, "POST", "/api/groups/move", `{"name":"Nie ma","index":0}`); w.Code != 404 {
		t.Errorf("brak grupy: kod %d", w.Code)
	}
}

func TestTytułPutZapisujeIZachowujeKomentarz(t *testing.T) {
	s, h, c := serwerKolejność(t)
	w := wyślij(h, c, "PUT", "/api/title", `{"title":"  Laboratorium: dom  "}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"title":"Laboratorium: dom"`) {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	plik := string(s.plikConfigu(t))
	if !strings.Contains(plik, `title: "Laboratorium: dom"   # komentarz tytułu`) {
		t.Errorf("tytuł albo komentarz:\n%s", plik)
	}
	cfg, err := config.Load([]byte(plik))
	if err != nil || cfg.Title != "Laboratorium: dom" {
		t.Fatalf("wczytano %v, %v", cfg, err)
	}
	// serwer odświeżył stan — strona główna ma nowy tytuł w <title> i <h1>
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if !strings.Contains(rr.Body.String(), "<title>Laboratorium: dom</title>") {
		t.Errorf("strona nie pokazuje nowego tytułu")
	}
}

func TestTytułPutWalidacja(t *testing.T) {
	s, h, c := serwerKolejność(t)
	przed := string(s.plikConfigu(t))
	cases := []struct{ nazwa, ciało, lang, fragment string }{
		{"pusty", `{"title":""}`, "pl", "pusty"},
		{"same spacje", `{"title":"   "}`, "en", "empty"},
		{"za długi", `{"title":"` + strings.Repeat("x", 81) + `"}`, "pl", "za długi"},
		{"nowa linia", `{"title":"a\nb"}`, "en", "single line"},
		{"znak sterujący", `{"title":"a\u0000b"}`, "pl", "sterując"},
		{"zły JSON", `nie json`, "en", ""},
	}
	for _, tc := range cases {
		w := wyślij(h, c, "PUT", "/api/title", tc.ciało, "Accept-Language", tc.lang)
		if w.Code != 400 {
			t.Errorf("%s: kod %d", tc.nazwa, w.Code)
		}
		if !strings.Contains(w.Body.String(), tc.fragment) {
			t.Errorf("%s: komunikat %q nie zawiera %q", tc.nazwa, w.Body, tc.fragment)
		}
	}
	if string(s.plikConfigu(t)) != przed {
		t.Error("odrzucone żądania zmieniły plik")
	}
}

func TestTytułPutDodajeKluczGdyBrak(t *testing.T) {
	s, h, c := serwerKolejność(t)
	bez := strings.Replace(configKolejność, "title: Dom   # komentarz tytułu\n\n", "", 1)
	if err := os.WriteFile(s.path, []byte(bez), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	if w := wyślij(h, c, "PUT", "/api/title", `{"title":"yes"}`); w.Code != 200 {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	cfg, err := config.Load(s.plikConfigu(t))
	if err != nil || cfg.Title != "yes" {
		t.Fatalf("title = %q, %v\n%s", cfg.Title, err, s.plikConfigu(t))
	}
	if !strings.Contains(string(s.plikConfigu(t)), "# nagłówek — ma przetrwać") {
		t.Error("zginął komentarz nagłówka")
	}
}

// Nowe trasy mutujące siedzą za tym samym bramkowaniem co reszta API.
func TestNoweTrasyWymagająSameOriginIZalogowania(t *testing.T) {
	_, h, c := serwerKolejność(t)
	for _, tr := range []struct{ metoda, ścieżka, ciało string }{
		{"POST", "/api/services/move", `{"group":"Media","name":"Sonarr","index":0}`},
		{"POST", "/api/groups/move", `{"name":"Sieć","index":0}`},
		{"PUT", "/api/title", `{"title":"x"}`},
	} {
		if w := wyślij(h, c, tr.metoda, tr.ścieżka, tr.ciało, "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-site: kod %d, chcę 403", tr.metoda, tr.ścieżka, w.Code)
		}
		r := httptest.NewRequest(tr.metoda, tr.ścieżka, strings.NewReader(tr.ciało))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code == http.StatusOK {
			t.Errorf("%s %s bez sesji: kod 200", tr.metoda, tr.ścieżka)
		}
	}
}

// Ruch w miejsce, w którym element już stoi, jest sukcesem bez śladu: plik
// nietknięty, żadnej nowej wersji w historii.
func TestRuchWToSamoMiejsceNieTworzyWersji(t *testing.T) {
	s, h, c := serwerKolejność(t)
	przed := string(s.plikConfigu(t))
	wersjePrzed, err := s.st.Versions(s.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for nazwa, p := range map[string]struct{ ścieżka, ciało string }{
		"usługa": {"/api/services/move", `{"group":"Media","name":"Jellyfin","toGroup":"Media","index":0}`},
		"grupa":  {"/api/groups/move", `{"name":"Media","index":0}`},
	} {
		w := wyślij(h, c, "POST", p.ścieżka, p.ciało)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: kod %d: %s", nazwa, w.Code, w.Body)
		}
	}
	if po := string(s.plikConfigu(t)); po != przed {
		t.Error("plik się zmienił przy ruchu w to samo miejsce")
	}
	wersjePo, _ := s.st.Versions(s.ctx, 100)
	if len(wersjePo) != len(wersjePrzed) {
		t.Errorf("wersji w historii: %d -> %d, chcę bez zmiany", len(wersjePrzed), len(wersjePo))
	}
}
