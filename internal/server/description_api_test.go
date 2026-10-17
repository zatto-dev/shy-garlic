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

const descriptionsConfig = `# nagłówek
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

func descriptionsServer(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s := testServer(t)
	if err := os.WriteFile(s.path, []byte(descriptionsConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), loggedInCookie(t, s)
}

func serviceFromFile(t *testing.T, s *Server, name string) config.Service {
	t.Helper()
	cfg, err := config.Load(s.configFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range cfg.Groups {
		for _, u := range g.Services {
			if u.Name == name {
				return u
			}
		}
	}
	t.Fatalf("missing service %s", name)
	return config.Service{}
}

func TestPatchServiceSetsDescriptionAndTags(t *testing.T) {
	s, h, c := descriptionsServer(t)
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Jellyfin","url":"http://jellyfin.home.arpa","description":"Filmy i muzyka","tags":["media","Media","  4k "]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	u := serviceFromFile(t, s, "Jellyfin")
	if u.Description != "Filmy i muzyka" || !reflect.DeepEqual([]string(u.Tags), []string{"media", "4k"}) {
		t.Fatalf("%+v", u)
	}
	// history entry with the change description
	if v, _ := s.st.Versions(s.ctx, 1); len(v) != 1 {
		t.Error("no entry in history")
	}
}

func TestPatchServiceWithoutDescriptionFieldsKeepsDescriptionAndTags(t *testing.T) {
	s, h, c := descriptionsServer(t)
	// a change of the address alone — description and tags (and the comment) must stay byte for byte
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Sonarr","url":"http://sonarr2.home.arpa"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(string(s.configFile(t)), `description: "Seriale"   # zostaje`) {
		t.Errorf("description touched:\n%s", s.configFile(t))
	}
	if u := serviceFromFile(t, s, "Sonarr"); len(u.Tags) != 2 {
		t.Errorf("tags touched: %v", u.Tags)
	}
}

func TestPatchServiceEmptyDescriptionAndTagsRemove(t *testing.T) {
	s, h, c := descriptionsServer(t)
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Sonarr","url":"http://sonarr.home.arpa","description":"","tags":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if u := serviceFromFile(t, s, "Sonarr"); u.Description != "" || len(u.Tags) != 0 {
		t.Errorf("%+v\n%s", u, s.configFile(t))
	}
}

func TestPatchServiceRejectsBadDescriptionAndTagsHumanly(t *testing.T) {
	s, h, c := descriptionsServer(t)
	before := string(s.configFile(t))
	long := strings.Repeat("a", config.MaxDescription+1)
	cases := []struct{ name, body, want string }{
		{"description too long", `{"description":"` + long + `"}`, "error_description_long"},
		{"description with a control character", `{"description":"a\nb"}`, "error_description_chars"},
		{"tag too long", `{"tags":["` + strings.Repeat("t", config.MaxTag+1) + `"]}`, "error_tags"},
		{"too many tags", `{"tags":["a","b","c","d","e","f","g","h","i","j","k","l","m"]}`, "error_tags"},
	}
	for _, tc := range cases {
		body := strings.Replace(tc.body, "{", `{"group":"Media","name":"Jellyfin","url":"http://jellyfin.home.arpa",`, 1)
		w := send(h, c, "PATCH", "/api/services", body, "Accept-Language", "pl")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d", tc.name, w.Code)
			continue
		}
		var reply struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &reply)
		if reply.Error != i18n.T(i18n.PL, tc.want) {
			t.Errorf("%s: message %q, want %q", tc.name, reply.Error, i18n.T(i18n.PL, tc.want))
		}
	}
	if string(s.configFile(t)) != before {
		t.Error("file changed despite rejected requests")
	}
}

func TestPatchServiceDescriptionWithMergeGives400WithExplanation(t *testing.T) {
	s, h, c := descriptionsServer(t)
	before := string(s.configFile(t))
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Dziedziczy","url":"http://d.home.arpa","description":"Mój"}`,
		"Accept-Language", "en")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if reply.Error != i18n.T(i18n.EN, "error_field_inherited") {
		t.Errorf("message: %s", w.Body)
	}
	if string(s.configFile(t)) != before {
		t.Error("file changed despite the refusal")
	}
}

func TestPostServiceSavesDescriptionAndTags(t *testing.T) {
	s, h, c := descriptionsServer(t)
	w := send(h, c, "POST", "/api/services",
		`{"group":"Media","name":"Radarr","url":"http://radarr.home.arpa","description":"Filmy: kolekcja","tags":["film","4k"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	u := serviceFromFile(t, s, "Radarr")
	if u.Description != "Filmy: kolekcja" || !reflect.DeepEqual([]string(u.Tags), []string{"film", "4k"}) {
		t.Errorf("%+v", u)
	}
}

func TestHomePageShowsDescriptionAndTagsAndSearchesByThem(t *testing.T) {
	s, _, _ := descriptionsServer(t)
	s.mu.Lock()
	s.cfg.Groups[0].Services[1].Description = `Seriale <b>"x"</b>`
	s.mu.Unlock()
	rec := httptest.NewRecorder()
	s.handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	html := rec.Body.String()

	if strings.Contains(html, `Seriale <b>`) {
		t.Error("the description was not escaped")
	}
	// description and tags are in the search index, next to the name and host
	re := `data-search="Sonarr sonarr.home.arpa Seriale`
	if !strings.Contains(html, re) {
		t.Errorf("no description in data-search:\n%s", html[strings.Index(html, `data-name="Sonarr"`):][:400])
	}
	for _, want := range []string{`class="service-description"`, `class="tags"`, `class="tag"`, `>4k<`} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %s", want)
		}
	}
	// a service without a description and tags does not get empty elements
	i := strings.Index(html, `data-name="Jellyfin"`)
	j := strings.Index(html[i:], `</li>`)
	if strings.Contains(html[i:i+j], `class="service-description"`) || strings.Contains(html[i:i+j], `class="tags"`) {
		t.Error("empty description/tags rendered for Jellyfin")
	}
}

func TestAPIServiceReturnsDescriptionAndTags(t *testing.T) {
	_, h, c := descriptionsServer(t)
	w := send(h, c, "GET", "/api/service?key="+"Media%2FSonarr", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	var reply struct {
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Description != "Seriale" || !reflect.DeepEqual(reply.Tags, []string{"tv", "4k"}) {
		t.Errorf("%+v", reply)
	}
}

func TestDescriptionAndTagsHaveTranslations(t *testing.T) {
	keys := []string{"service_description", "service_description_placeholder", "service_tags", "service_tags_placeholder",
		"error_description_long", "error_description_chars", "error_tags", "error_field_inherited"}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range keys {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: missing text %q", l, k)
			}
		}
	}
}
