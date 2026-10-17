package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/config"
)

const orderConfig = `# nagłówek — ma przetrwać
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

// orderServer sets the panel up on a config with comments and returns the handler
// together with the session cookie — requests go the whole way (login, CSRF).
func orderServer(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s := testServer(t)
	if err := os.WriteFile(s.path, []byte(orderConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), loggedInCookie(t, s)
}

func send(h http.Handler, c *http.Cookie, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(c)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func fileLayout(t *testing.T, s *Server) string {
	t.Helper()
	cfg, err := config.Load(s.configFile(t))
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

func TestServiceMoveWithinGroupAndBetweenGroups(t *testing.T) {
	s, h, c := orderServer(t)

	w := send(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","toGroup":"Media","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if got := fileLayout(t, s); got != "Media:Sonarr,Jellyfin|Sieć:Pi-hole" {
		t.Fatalf("layout = %s", got)
	}

	// toGroup omitted = the same group
	w = send(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","index":1}`)
	if w.Code != http.StatusOK || fileLayout(t, s) != "Media:Jellyfin,Sonarr|Sieć:Pi-hole" {
		t.Fatalf("code %d, layout %s", w.Code, fileLayout(t, s))
	}

	w = send(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Jellyfin","toGroup":"Sieć","index":1}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if got := fileLayout(t, s); got != "Media:Sonarr|Sieć:Pi-hole,Jellyfin" {
		t.Fatalf("layout = %s", got)
	}
	file := string(s.configFile(t))
	for _, f := range []string{"# nagłówek — ma przetrwać", "# komentarz tytułu", "# komentarz Jellyfin", "extra: zostaje", "# grupa pierwsza", "# stopka"} {
		if strings.Count(file, f) != 1 {
			t.Errorf("%q: %d occurrences\n%s", f, strings.Count(file, f), file)
		}
	}
	// the comment travels with the service, it does not stay in the old group
	if !strings.Contains(file, "      # komentarz Jellyfin\n      - name: Jellyfin\n        extra: zostaje") {
		t.Errorf("comment detached from the service:\n%s", file)
	}

	// every move leaves an entry in the history (newest first), with a description
	versions, err := s.st.Versions(s.ctx, 10)
	if err != nil || len(versions) != 3 {
		t.Fatalf("history: %v, %d entries, want 3", err, len(versions))
	}
	if !strings.Contains(versions[0].Description, "Jellyfin") || !strings.Contains(versions[1].Description, "position 2") {
		t.Errorf("history descriptions: %q / %q", versions[0].Description, versions[1].Description)
	}
}

func TestServiceMoveRejects(t *testing.T) {
	s, h, c := orderServer(t)
	before := string(s.configFile(t))
	cases := []struct {
		name, body string
		code       int
	}{
		{"bad JSON", `{`, 400},
		{"missing index", `{"group":"Media","name":"Sonarr"}`, 400},
		{"empty name", `{"group":"Media","name":"","index":0}`, 400},
		{"control character", "{\"group\":\"Media\",\"name\":\"a\\nb\",\"index\":0}", 400},
		{"index too large", `{"group":"Media","name":"Sonarr","index":5}`, 400},
		{"index ujemny", `{"group":"Media","name":"Sonarr","index":-1}`, 400},
		{"missing service", `{"group":"Media","name":"Nie ma","index":0}`, 404},
		{"missing target group", `{"group":"Media","name":"Sonarr","toGroup":"Nie ma","index":0}`, 404},
	}
	for _, tc := range cases {
		w := send(h, c, "POST", "/api/services/move", tc.body)
		if w.Code != tc.code {
			t.Errorf("%s: code %d (want %d): %s", tc.name, w.Code, tc.code, w.Body)
		}
	}
	if string(s.configFile(t)) != before {
		t.Error("rejected requests changed the file")
	}
}

func TestGroupMove(t *testing.T) {
	s, h, c := orderServer(t)
	w := send(h, c, "POST", "/api/groups/move", `{"name":"Sieć","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if got := fileLayout(t, s); got != "Sieć:Pi-hole|Media:Jellyfin,Sonarr" {
		t.Fatalf("layout = %s", got)
	}
	file := string(s.configFile(t))
	if !strings.Contains(file, "  # grupa pierwsza\n  - name: Media") || !strings.HasSuffix(file, "# stopka\n") {
		t.Errorf("group comment or footer:\n%s", file)
	}
	for _, tc := range []string{`{"name":"Sieć"}`, `{"name":"","index":0}`, `{"name":"Sieć","index":2}`, `{"name":"Sieć","index":-1}`} {
		if w := send(h, c, "POST", "/api/groups/move", tc); w.Code != 400 {
			t.Errorf("%s: code %d", tc, w.Code)
		}
	}
	if w := send(h, c, "POST", "/api/groups/move", `{"name":"Nie ma","index":0}`); w.Code != 404 {
		t.Errorf("missing group: code %d", w.Code)
	}
}

func TestTitlePutSavesAndKeepsComment(t *testing.T) {
	s, h, c := orderServer(t)
	w := send(h, c, "PUT", "/api/title", `{"title":"  Laboratorium: dom  "}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"title":"Laboratorium: dom"`) {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	file := string(s.configFile(t))
	if !strings.Contains(file, `title: "Laboratorium: dom"   # komentarz tytułu`) {
		t.Errorf("title or comment:\n%s", file)
	}
	cfg, err := config.Load([]byte(file))
	if err != nil || cfg.Title != "Laboratorium: dom" {
		t.Fatalf("wczytano %v, %v", cfg, err)
	}
	// the server refreshed its state — the home page has the new title in <title> and <h1>
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if !strings.Contains(rr.Body.String(), "<title>Laboratorium: dom</title>") {
		t.Errorf("the page does not show the new title")
	}
}

func TestTitlePutValidation(t *testing.T) {
	s, h, c := orderServer(t)
	before := string(s.configFile(t))
	cases := []struct{ name, body, lang, fragment string }{
		{"pusty", `{"title":""}`, "pl", "pusty"},
		{"same spacje", `{"title":"   "}`, "en", "empty"},
		{"too long", `{"title":"` + strings.Repeat("x", 81) + `"}`, "pl", "za długi"},
		{"nowa linia", `{"title":"a\nb"}`, "en", "single line"},
		{"control character", `{"title":"a\u0000b"}`, "pl", "sterując"},
		{"bad JSON", `nie json`, "en", ""},
	}
	for _, tc := range cases {
		w := send(h, c, "PUT", "/api/title", tc.body, "Accept-Language", tc.lang)
		if w.Code != 400 {
			t.Errorf("%s: code %d", tc.name, w.Code)
		}
		if !strings.Contains(w.Body.String(), tc.fragment) {
			t.Errorf("%s: message %q does not contain %q", tc.name, w.Body, tc.fragment)
		}
	}
	if string(s.configFile(t)) != before {
		t.Error("rejected requests changed the file")
	}
}

func TestTitlePutAddsKeyWhenMissing(t *testing.T) {
	s, h, c := orderServer(t)
	without := strings.Replace(orderConfig, "title: Dom   # komentarz tytułu\n\n", "", 1)
	if err := os.WriteFile(s.path, []byte(without), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	if w := send(h, c, "PUT", "/api/title", `{"title":"yes"}`); w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	cfg, err := config.Load(s.configFile(t))
	if err != nil || cfg.Title != "yes" {
		t.Fatalf("title = %q, %v\n%s", cfg.Title, err, s.configFile(t))
	}
	if !strings.Contains(string(s.configFile(t)), "# nagłówek — ma przetrwać") {
		t.Error("the header comment got lost")
	}
}

// The new mutating routes sit behind the same gating as the rest of the API.
func TestNewRoutesRequireSameOriginAndLogin(t *testing.T) {
	_, h, c := orderServer(t)
	for _, tr := range []struct{ method, path, body string }{
		{"POST", "/api/services/move", `{"group":"Media","name":"Sonarr","index":0}`},
		{"POST", "/api/groups/move", `{"name":"Sieć","index":0}`},
		{"PUT", "/api/title", `{"title":"x"}`},
	} {
		if w := send(h, c, tr.method, tr.path, tr.body, "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
			t.Errorf("%s %s cross-site: code %d, want 403", tr.method, tr.path, w.Code)
		}
		r := httptest.NewRequest(tr.method, tr.path, strings.NewReader(tr.body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code == http.StatusOK {
			t.Errorf("%s %s without a session: code 200", tr.method, tr.path)
		}
	}
}

// A move to the place where the element already stands is a success without a trace: file
// untouched, no new version in the history.
func TestMoveToSamePlaceCreatesNoVersion(t *testing.T) {
	s, h, c := orderServer(t)
	before := string(s.configFile(t))
	versionsBefore, err := s.st.Versions(s.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]struct{ path, body string }{
		"service": {"/api/services/move", `{"group":"Media","name":"Jellyfin","toGroup":"Media","index":0}`},
		"group":   {"/api/groups/move", `{"name":"Media","index":0}`},
	} {
		w := send(h, c, "POST", p.path, p.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code %d: %s", name, w.Code, w.Body)
		}
	}
	if after := string(s.configFile(t)); after != before {
		t.Error("the file changed on a move to the same place")
	}
	versionsAfter, _ := s.st.Versions(s.ctx, 100)
	if len(versionsAfter) != len(versionsBefore) {
		t.Errorf("versions in history: %d -> %d, want no change", len(versionsBefore), len(versionsAfter))
	}
}
