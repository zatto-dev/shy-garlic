package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

// Cała ścieżka: YAML → Load → monitor → wynik → /api/service. Sprawdza, że pola
// check.path/contains dojechały do health.Target (zastosuj), a nie tylko że
// każda warstwa osobno działa.
func TestKontrolaTreściZConfiguDoWyniku(t *testing.T) {
	// monitor sprawdza też sam w tle, więc zapis ścieżki idzie pod mutexem
	var (
		mu      sync.Mutex
		ścieżka string
	)
	cel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ścieżka = r.URL.Path
		mu.Unlock()
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"starting"}`))
			return
		}
		w.WriteHeader(500)
	}))
	defer cel.Close()

	s := serwerTestowy(t)
	cfg := "groups:\n  - name: G\n    services:\n      - name: S\n        url: " + cel.URL + "/ui\n" +
		"        check:\n          path: /health\n          contains: '\"status\":\"ok\"'\n"
	if err := os.WriteFile(s.path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}

	r, err := s.mon.SprawdźTeraz(s.ctx, "G/S")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	ostatnia := ścieżka
	mu.Unlock()
	if ostatnia != "/health" {
		t.Errorf("sprawdzanie poszło na %q, chcę /health", ostatnia)
	}
	if r.State != health.StateDegraded || !strings.HasPrefix(r.Err, health.PrefiksTreści) {
		t.Fatalf("%+v", r)
	}

	// szczegóły usługi pokazują regułę i powód
	c := ciasteczkoZalogowanego(t, s)
	w := wyślij(s.Handler(), c, "GET", "/api/service?key=G%2FS", "")
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	var odp struct {
		Path     string `json:"path"`
		Contains string `json:"contains"`
		Now      struct {
			State string `json:"state"`
			Err   string `json:"err"`
		} `json:"now"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &odp); err != nil {
		t.Fatal(err)
	}
	if odp.Path != "/health" || odp.Contains != `"status":"ok"` {
		t.Errorf("reguła w szczegółach: %+v", odp)
	}
	if odp.Now.State != "degraded" || !strings.Contains(odp.Now.Err, "status") {
		t.Errorf("powód w szczegółach: %+v", odp.Now)
	}
}

func TestTekstyKontroliTreściMająObaJęzyki(t *testing.T) {
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Mapa(l)
		for _, k := range []string{"chip_tresc", "powod_stanu", "kontrola_get", "kontrola_zawiera", "kontrola_nie_zawiera"} {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: brak tekstu %q", l, k)
			}
		}
	}
}
