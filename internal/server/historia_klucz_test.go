package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/store"
)

// SHYG-41: klucz usługi to grupa/nazwa. Przeniesienie albo zmiana nazwy nie może
// gubić historii — karta ma od razu mieć swój 24-godzinny pasek.

func zasiejHistorię(t *testing.T, s *Server, klucz string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := s.st.ZapiszWynik(context.Background(), store.Wynik{
			Klucz: klucz, Stan: "up", Kod: 200, MS: 3,
			Kiedy: time.Now().Add(-time.Duration(n-i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// punktyUsługi woła /api/service dla klucza i zwraca liczbę punktów historii.
func punktyUsługi(t *testing.T, h http.Handler, c *http.Cookie, klucz string) int {
	t.Helper()
	w := wyślij(h, c, "GET", "/api/service?key="+url.QueryEscape(klucz), "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/service %q: %d %s", klucz, w.Code, w.Body)
	}
	var odp struct {
		Points []json.RawMessage `json:"points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &odp); err != nil {
		t.Fatal(err)
	}
	return len(odp.Points)
}

func TestPrzeniesienieMiedzyGrupamiZachowujeHistorię(t *testing.T) {
	s, h, c := serwerKolejność(t)
	zasiejHistorię(t, s, "Media/Sonarr", 4)

	w := wyślij(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","toGroup":"Sieć","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if n := punktyUsługi(t, h, c, "Sieć/Sonarr"); n != 4 {
		t.Errorf("pod nowym kluczem %d punktów, chcę 4", n)
	}
	if hist, _ := s.st.Historia(context.Background(), "Media/Sonarr", 10); len(hist) != 0 {
		t.Errorf("stary klucz zachował %d wierszy", len(hist))
	}
}

func TestZmianaNazwyIPrzeniesienieWPatchuZachowujeHistorię(t *testing.T) {
	s, h, c := serwerKolejność(t)
	zasiejHistorię(t, s, "Media/Sonarr", 3)

	w := wyślij(h, c, "PATCH", "/api/services", `{"group":"Media","name":"Sonarr","newName":"Sonarr 4K"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	if n := punktyUsługi(t, h, c, "Media/Sonarr 4K"); n != 3 {
		t.Errorf("po zmianie nazwy %d punktów, chcę 3", n)
	}

	// przeniesienie + zmiana nazwy jednym żądaniem
	w = wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Sonarr 4K","newGroup":"Sieć","newName":"Sonarr"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move+rename: %d %s", w.Code, w.Body)
	}
	if n := punktyUsługi(t, h, c, "Sieć/Sonarr"); n != 3 {
		t.Errorf("po przeniesieniu+zmianie nazwy %d punktów, chcę 3", n)
	}
}

func TestZmianaNazwyGrupyZachowujeHistorięWszystkichUsług(t *testing.T) {
	s, h, c := serwerKolejność(t)
	zasiejHistorię(t, s, "Media/Jellyfin", 2)
	zasiejHistorię(t, s, "Media/Sonarr", 5)
	zasiejHistorię(t, s, "Sieć/Pi-hole", 1)

	w := wyślij(h, c, "PATCH", "/api/groups", `{"name":"Media","newName":"Filmy"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("group rename: %d %s", w.Code, w.Body)
	}
	for klucz, want := range map[string]int{"Filmy/Jellyfin": 2, "Filmy/Sonarr": 5, "Sieć/Pi-hole": 1} {
		if n := punktyUsługi(t, h, c, klucz); n != want {
			t.Errorf("%s: %d punktów, chcę %d", klucz, n, want)
		}
	}
}

// ?dry=1 niczego nie zapisuje, więc niczego nie przenosi.
func TestDryRunNieRuszaHistorii(t *testing.T) {
	s, h, c := serwerKolejność(t)
	zasiejHistorię(t, s, "Media/Sonarr", 2)
	w := wyślij(h, c, "POST", "/api/services/move?dry=1", `{"group":"Media","name":"Sonarr","toGroup":"Sieć","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("dry: %d %s", w.Code, w.Body)
	}
	if hist, _ := s.st.Historia(context.Background(), "Media/Sonarr", 10); len(hist) != 2 {
		t.Errorf("dry-run przeniósł historię: %d wierszy", len(hist))
	}
}

// Stan powiadamiacza i „ostatnio zapisany" jadą razem z kluczem — inaczej po
// przeciągnięciu ten sam wynik trafiłby do bazy drugi raz.
func TestMigracjaPrzepinaStanPowiadamiaczaIZapisu(t *testing.T) {
	s, _, _ := serwerKolejność(t)
	at := time.Now()
	s.muOst.Lock()
	s.ostatnie["Media/Sonarr"] = at
	s.muOst.Unlock()

	s.migrujKlucze(context.Background(), map[string]string{"Media/Sonarr": "Sieć/Sonarr"})

	s.muOst.Lock()
	defer s.muOst.Unlock()
	if !s.ostatnie["Sieć/Sonarr"].Equal(at) {
		t.Errorf("ostatnie[nowy] = %v, chcę %v", s.ostatnie["Sieć/Sonarr"], at)
	}
	if _, ma := s.ostatnie["Media/Sonarr"]; ma {
		t.Error("stary klucz został w ostatnie")
	}
}
