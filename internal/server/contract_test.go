package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// jsonKeys fetches an API route as the logged-in user and returns the top-level keys.
func jsonKeys(t *testing.T, h http.Handler, c *http.Cookie, path string) map[string]any {
	t.Helper()
	w := send(h, c, "GET", path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: code %d: %s", path, w.Code, w.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return m
}

func requireKeys(t *testing.T, route string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: missing key %q in %v", route, k, m)
		}
	}
}

// The public API speaks English: these are the keys documented in the README and read
// by app.js. Renaming one is a breaking change and has to show up in the CHANGELOG.
func TestAPIContractKeys(t *testing.T) {
	s := testServer(t)
	h, c := s.Handler(), loggedInCookie(t, s)
	requireKeys(t, "/api/info", jsonKeys(t, h, c, "/api/info"),
		"version", "notify", "git", "icons", "config", "docker", "dockerDisabled", "dockerNew",
		"backgrounds", "appearance")
	requireKeys(t, "/api/notify", jsonKeys(t, h, c, "/api/notify"),
		"enabled", "webhook", "source", "format", "formatSource", "formats", "threshold")
	requireKeys(t, "/api/icons", jsonKeys(t, h, c, "/api/icons?q=a"), "icons", "variants", "total", "mode")
	requireKeys(t, "/api/instance", jsonKeys(t, h, c, "/api/instance"), "options")
	requireKeys(t, "/api/service", jsonKeys(t, h, c, "/api/service?key=Media/Jellyfin"),
		"key", "name", "iconSrc", "iconVariantDark", "iconVariantLight")
}

// The pieces the page injects for app.js: window globals and the appearance object.
func TestPageGlobals(t *testing.T) {
	s := testServer(t)
	w := send(s.Handler(), loggedInCookie(t, s), "GET", "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"window.INITIAL_STATE = ", "window.APPEARANCE = ", `"writable":`, `"limitMB":`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

// app.js draws wallpapers' brightness from a blob: URL (URL.createObjectURL), so img-src
// has to allow it. Nothing else may: blob: anywhere but img-src would widen the policy.
func TestCSPAllowsBlobImagesOnly(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy header")
	}
	for _, directive := range strings.Split(csp, ";") {
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			continue
		}
		has := strings.Contains(directive, "blob:")
		if fields[0] == "img-src" && !has {
			t.Errorf("img-src does not allow blob: %q", csp)
		}
		if fields[0] != "img-src" && has {
			t.Errorf("%s allows blob: %q", fields[0], csp)
		}
	}
}

// Browser preferences used to live under Polish localStorage keys. The block between the
// markers in app.js moves them to the new names on start; here it runs against a stub.
func TestLocalStorageMigration(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	src, err := os.ReadFile(filepath.Join("assets", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)// migrate-storage:begin\n(.*?)// migrate-storage:end`).FindSubmatch(src)
	if m == nil {
		t.Fatal("migrate-storage markers not found in app.js")
	}
	script := `
const store = new Map(JSON.parse(process.argv[1]));
const storage = {
  read: (k) => (store.has(k) ? store.get(k) : null),
  write: (k, v) => { store.set(k, v); },
  remove: (k) => { store.delete(k); },
};
const APPEARANCE_KEY = "shyg.appearance", EDIT_KEY = "shyg.edit";
` + string(m[1]) + `
console.log(JSON.stringify([...store.entries()].sort()));
`
	cases := []struct{ name, in, want string }{
		{"old keys move over",
			`[["shyg.wyglad","{\"tone\":\"light\"}"],["shyg-edycja","1"]]`,
			`[["shyg.appearance","{\"tone\":\"light\"}"],["shyg.edit","1"]]`},
		{"an existing new key wins and the old one is dropped",
			`[["shyg.wyglad","old"],["shyg.appearance","new"]]`,
			`[["shyg.appearance","new"]]`},
		{"nothing to migrate", `[["shyg.edit","0"]]`, `[["shyg.edit","0"]]`},
	}
	for _, c := range cases {
		out, err := exec.Command(node, "-e", script, c.in).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
