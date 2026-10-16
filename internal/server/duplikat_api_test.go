package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestDuplikatNazwyDaje409IPlikZostaje(t *testing.T) {
	przypadki := map[string]struct{ metoda, ścieżka, ciało string }{
		"rename usługi w grupie":               {"PATCH", "/api/services", `{"group":"Media","name":"Sonarr","newName":"Jellyfin","url":"http://x"}`},
		"przeniesienie + rename na istniejącą": {"PATCH", "/api/services", `{"group":"Media","name":"Sonarr","newGroup":"Sieć","newName":"Pi-hole","url":"http://x"}`},
		"rename grupy na istniejącą":           {"PATCH", "/api/groups", `{"name":"Sieć","newName":"Media"}`},
	}
	for nazwa, p := range przypadki {
		t.Run(nazwa, func(t *testing.T) {
			s, h, c := serwerKolejność(t)
			przed := układPliku(t, s)
			w := wyślij(h, c, p.metoda, p.ścieżka, p.ciało)
			if w.Code != http.StatusConflict {
				t.Fatalf("kod %d, chcę 409: %s", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), "taken") {
				t.Errorf("brak komunikatu i18n: %s", w.Body)
			}
			if po := układPliku(t, s); po != przed {
				t.Errorf("plik się zmienił: %q -> %q", przed, po)
			}
		})
	}
}
