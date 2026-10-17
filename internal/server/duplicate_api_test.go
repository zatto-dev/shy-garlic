package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestDuplicateNameGives409AndFileStays(t *testing.T) {
	cases := map[string]struct{ method, path, body string }{
		"rename a service within a group":     {"PATCH", "/api/services", `{"group":"Media","name":"Sonarr","newName":"Jellyfin","url":"http://x"}`},
		"move + rename onto an existing one":  {"PATCH", "/api/services", `{"group":"Media","name":"Sonarr","newGroup":"Sieć","newName":"Pi-hole","url":"http://x"}`},
		"rename a group onto an existing one": {"PATCH", "/api/groups", `{"name":"Sieć","newName":"Media"}`},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			s, h, c := orderServer(t)
			before := fileLayout(t, s)
			w := send(h, c, p.method, p.path, p.body)
			if w.Code != http.StatusConflict {
				t.Fatalf("code %d, want 409: %s", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), "taken") {
				t.Errorf("missing i18n message: %s", w.Body)
			}
			if after := fileLayout(t, s); after != before {
				t.Errorf("file changed: %q -> %q", before, after)
			}
		})
	}
}
