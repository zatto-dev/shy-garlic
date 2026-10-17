package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

const testConfig = `# Nagłówek testowy — ma przetrwać.

title: Test

appearance:
  background:
    preset: aurora
    blur: 10
    tone: dark
  view: grid

groups:
  - name: Media
    services:
      - name: Jellyfin

# Stopka testowa: ma zostać na dole.
`

// testServer sets the panel up on files in a temporary directory. The services deliberately
// have no addresses — otherwise the monitor would start knocking on non-existent ports.
func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "shygarlic.yaml")
	if err := os.WriteFile(cfg, []byte(testConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, st, Options{BackgroundDir: filepath.Join(dir, "bg")})
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_ = st.Close()
	})
	return s
}

func dirBackgrounds(s *Server) string { return s.backgrounds.dir }

// loggedInCookie creates an account and returns its session cookie.
func loggedInCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	u, err := s.st.AddUser(context.Background(), "admin", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.st.NewSession(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: token}
}

// ── format samples ───────────────────────────────────────────────────────────

func fill(magic []byte) []byte {
	return append(magic, bytes.Repeat([]byte{0x20}, 600)...)
}

func pngBytes() []byte  { return fill([]byte("\x89PNG\r\n\x1a\n")) }
func jpegBytes() []byte { return fill([]byte{0xff, 0xd8, 0xff, 0xe0}) }
func gifBytes() []byte  { return fill([]byte("GIF89a")) }
func webpBytes() []byte { return fill([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")) }
func avifBytes() []byte {
	return fill([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f', 0, 0, 0, 0})
}
func svgBytes() []byte {
	return fill([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
}

// uploadRequest builds a multipart with ONE "file" part.
func uploadRequest(t *testing.T, name string, data []byte, partType string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	if partType != "" {
		h.Set("Content-Type", partType)
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/background", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func upload(t *testing.T, s *Server, name string, data []byte, partType string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleBackgroundUpload(w, uploadRequest(t, name, data, partType))
	return w
}

// pathPayloads — names someone would use to try to leave the directory or
// inject something into CSS or a header.
var pathPayloads = []string{
	"../x", `..\x`, "..%2f", "%2e%2e%2f", "....//x", "/etc/passwd", "../../shygarlic.db",
	"shygarlic.db", "shygarlic.db-wal", ".ukryty", "-x", "x\x00y", strings.Repeat("a", 300),
	"Logo.PNG", "CON", `a") ; } body{display:none} .x{a:url("`, `x\`, `x\29 `, "x/*",
	"x</style><script>", "tło ze spacją.png", "", ".", "..",
}

func TestSanitizeBackgroundName(t *testing.T) {
	for _, payload := range pathPayloads {
		name, ok := sanitizeName(payload, "png")
		if !ok {
			continue // a refusal is fine
		}
		if !config.BackgroundName.MatchString(name) {
			t.Errorf("%q gave %q — outside the alphabet", payload, name)
		}
		if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			t.Errorf("%q gave %q — it still looks like a path", payload, name)
		}
	}
	// the extension comes from sniffing, not from the name
	if n, _ := sanitizeName("obrazek.jpeg", "png"); n != "obrazek.png" {
		t.Errorf("name = %q, want obrazek.png", n)
	}
	if n, _ := sanitizeName("....", "png"); n != "background.png" {
		t.Errorf("empty body = %q, want background.png", n)
	}
	if _, ok := sanitizeName("x", "svg"); ok {
		t.Error("a format outside the allowlist must not pass")
	}
}

func TestBackgroundUploadStaysInsideDir(t *testing.T) {
	s := testServer(t)
	stub := filepath.Join(filepath.Dir(dirBackgrounds(s)), "atrapa")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stub, "nie-ruszaj.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, payload := range pathPayloads {
		w := upload(t, s, payload, pngBytes(), "image/png")
		if w.Code != http.StatusCreated && w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Errorf("%q → %d", payload, w.Code)
		}
	}
	entries, err := os.ReadDir(stub)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "nie-ruszaj.txt" {
		t.Fatalf("the directory next to the backgrounds was touched: %+v", entries)
	}
	for _, e := range mustReadDir(t, dirBackgrounds(s)) {
		if !config.BackgroundName.MatchString(e.Name()) {
			t.Errorf("a file %q outside the alphabet appeared in the backgrounds directory", e.Name())
		}
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestBackgroundUploadRejectsSVG(t *testing.T) {
	s := testServer(t)
	// the name and the part header lie — only the content counts
	w := upload(t, s, "x.png", svgBytes(), "image/png")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (SVG executes a script as a document)", w.Code)
	}
	if entries := mustReadDir(t, dirBackgrounds(s)); len(entries) != 0 {
		t.Fatalf("something was saved: %+v", entries)
	}
}

func TestBackgroundUploadKindFromContentNotName(t *testing.T) {
	s := testServer(t)
	cases := []struct {
		name   string
		data   []byte
		want   string
		wantMI string
	}{
		{"x.png", jpegBytes(), "x.jpg", "image/jpeg"},
		{"x.exe", pngBytes(), "x.png", "image/png"},
		{"y.txt", gifBytes(), "y.gif", "image/gif"},
		{"z", webpBytes(), "z.webp", "image/webp"},
		{"w.png", avifBytes(), "w.avif", "image/avif"},
	}
	for _, c := range cases {
		w := upload(t, s, c.name, c.data, "application/octet-stream")
		if w.Code != http.StatusCreated {
			t.Errorf("%s → %d (%s)", c.name, w.Code, w.Body.String())
			continue
		}
		var entry BackgroundEntry
		if err := json.Unmarshal(w.Body.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Name != c.want || entry.Kind != c.wantMI {
			t.Errorf("%s → %+v, want %s / %s", c.name, entry, c.want, c.wantMI)
		}
	}
}

func TestBackgroundUploadRejectsTooLargeFile(t *testing.T) {
	s := testServer(t)
	before := multipartTempCount(t)

	large := append(pngBytes(), bytes.Repeat([]byte{0x41}, 9<<20)...)
	w := upload(t, s, "duzy.png", large, "image/png")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", w.Code)
	}
	if entries := mustReadDir(t, dirBackgrounds(s)); len(entries) != 0 {
		t.Fatalf("a temp file was left after the rejection: %+v", entries)
	}
	// proof that there was no ParseMultipartForm — it spills the excess to os.TempDir()
	if after := multipartTempCount(t); after != before {
		t.Errorf("multipart-* files in the temp dir: %d → %d", before, after)
	}
}

func multipartTempCount(t *testing.T) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(os.TempDir(), "multipart-*"))
	if err != nil {
		return 0
	}
	return len(files)
}

func TestBackgroundUploadCollisionGivesSuffix(t *testing.T) {
	s := testServer(t)
	want := []string{"background.png", "background-2.png", "background-3.png"}
	for i, name := range want {
		w := upload(t, s, "background.png", pngBytes(), "image/png")
		if w.Code != http.StatusCreated {
			t.Fatalf("upload %d → %d (%s)", i+1, w.Code, w.Body.String())
		}
		var entry BackgroundEntry
		_ = json.Unmarshal(w.Body.Bytes(), &entry)
		if entry.Name != name {
			t.Fatalf("upload %d gave %q, want %q", i+1, entry.Name, name)
		}
	}
	if entries := mustReadDir(t, dirBackgrounds(s)); len(entries) != 3 {
		t.Fatalf("files = %d, want 3 — one overwrote another", len(entries))
	}
}

func TestBackgroundUploadFilesLimit(t *testing.T) {
	s := testServer(t)
	for i := range backgroundFilesLimit {
		name := filepath.Join(dirBackgrounds(s), "stare"+string(rune('a'+i%26))+string(rune('a'+i/26))+".png")
		if err := os.WriteFile(name, pngBytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := upload(t, s, "jeszcze.png", pngBytes(), "image/png")
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 with %d files", w.Code, backgroundFilesLimit)
	}
}

func TestBgServesSandboxHeaders(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s)
	if w := upload(t, s, "x.png", pngBytes(), "image/png"); w.Code != http.StatusCreated {
		t.Fatalf("upload → %d", w.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "/bg/x.png", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", w.Code, w.Body.String())
	}
	want := map[string]string{
		"Content-Type":            "image/png",
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"Content-Disposition":     `inline; filename="x.png"`,
		"Cache-Control":           "private, max-age=0, must-revalidate",
		"X-Content-Type-Options":  "nosniff",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestBgRequiresLogin(t *testing.T) {
	s := testServer(t)
	_ = loggedInCookie(t, s) // so that /setup does not take over the request
	if w := upload(t, s, "x.png", pngBytes(), "image/png"); w.Code != http.StatusCreated {
		t.Fatalf("upload → %d", w.Code)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/bg/x.png", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want 303 to /login", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("PNG")) {
		t.Fatal("a logged-out user got the image bytes")
	}
}

func TestRemoveBackgroundStaysInsideDir(t *testing.T) {
	s := testServer(t)
	beside := filepath.Join(filepath.Dir(dirBackgrounds(s)), "waznyplik.txt")
	if err := os.WriteFile(beside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, payload := range append(pathPayloads, "../waznyplik.txt", "x.png") {
		r := httptest.NewRequest(http.MethodDelete, "/api/background/x", nil)
		r.SetPathValue("name", payload)
		w := httptest.NewRecorder()
		s.handleBackgroundRemove(w, r)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Errorf("%q → %d, want 400 or 404", payload, w.Code)
		}
	}
	if _, err := os.Stat(beside); err != nil {
		t.Fatalf("the file next to the backgrounds directory disappeared: %v", err)
	}
}

func TestRemoveUsedBackgroundRefuses409(t *testing.T) {
	s := testServer(t)
	if w := upload(t, s, "uzywane.png", pngBytes(), "image/png"); w.Code != http.StatusCreated {
		t.Fatalf("upload → %d", w.Code)
	}
	if w := putAppearance(t, s, `{"preset":"photo","image":"uzywane.png"}`, false); w.Code != http.StatusOK {
		t.Fatalf("appearance save → %d (%s)", w.Code, w.Body.String())
	}

	r := httptest.NewRequest(http.MethodDelete, "/api/background/uzywane.png", nil)
	r.SetPathValue("name", "uzywane.png")
	w := httptest.NewRecorder()
	s.handleBackgroundRemove(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 — change the appearance first, then delete", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dirBackgrounds(s), "uzywane.png")); err != nil {
		t.Fatalf("the file disappeared despite 409: %v", err)
	}
}

// ── appearance ───────────────────────────────────────────────────────────────

func putAppearance(t *testing.T, s *Server, body string, dry bool) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/appearance"
	if dry {
		url += "?dry=1"
	}
	r := httptest.NewRequest(http.MethodPut, url, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAppearancePut(w, r)
	return w
}

func TestAppearancePutValidatesEveryField(t *testing.T) {
	cases := []struct {
		body string
		code int
	}{
		{`{"preset":"hocus"}`, http.StatusBadRequest},
		{`{"tone":"x"}`, http.StatusBadRequest},
		{`{"view":"x"}`, http.StatusBadRequest},
		{`{"blur":-1}`, http.StatusBadRequest},
		{`{"blur":31}`, http.StatusBadRequest},
		{`{"preset":"photo","image":"../x"}`, http.StatusBadRequest},
		{`{"image":"x.png"}`, http.StatusBadRequest}, // the preset is not photo
		{`{"preset":"photo","image":"nie-ma-mnie.png"}`, http.StatusNotFound},
		{`{"preset":"photo"}`, http.StatusBadRequest}, // a photo without a file
		{`nie json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		s := testServer(t)
		before, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatal(err)
		}
		w := putAppearance(t, s, c.body, false)
		if w.Code != c.code {
			t.Errorf("%s → %d, want %d (%s)", c.body, w.Code, c.code, w.Body.String())
		}
		after, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("%s touched the file despite the refusal", c.body)
		}
	}
}

func TestAppearancePutGoesThroughDiff(t *testing.T) {
	s := testServer(t)
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}

	w := putAppearance(t, s, `{"preset":"fog","blur":0,"view":"list"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("dry → %d (%s)", w.Code, w.Body.String())
	}
	var reply map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dry", "diff", "added", "removed", "summary"} {
		if _, is := reply[key]; !is {
			t.Errorf("w odpowiedzi brakuje %q: %v", key, reply)
		}
	}
	if after, _ := os.ReadFile(s.path); !bytes.Equal(before, after) {
		t.Fatal("the preview changed the file")
	}

	if w := putAppearance(t, s, `{"preset":"fog","blur":0,"view":"list"}`, false); w.Code != http.StatusOK {
		t.Fatalf("save → %d (%s)", w.Code, w.Body.String())
	}
	cfg, err := config.Load(mustRead(t, s.path))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Appearance.Background.Preset != "fog" || cfg.Appearance.View != "list" ||
		cfg.Appearance.Background.Blur != 0 {
		t.Fatalf("saved appearance = %+v", cfg.Appearance)
	}
	if !strings.Contains(string(mustRead(t, s.path)), "# Stopka testowa: ma zostać na dole.") {
		t.Fatal("the appearance save ate the footer comment")
	}
	versions, err := s.st.Versions(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions in history = %d, want 1", len(versions))
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAppearanceGetAndInfoDoNotLeakJunk(t *testing.T) {
	s := testServer(t)
	s.mu.Lock()
	s.cfg.Appearance.Background.Preset = "<script>"
	s.cfg.Appearance.Background.Image = `x") ; }`
	s.cfg.Appearance.View = "hocus"
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleAppearanceGet(w, httptest.NewRequest(http.MethodGet, "/api/appearance", nil))
	var got2 appearanceAPI
	if err := json.Unmarshal(w.Body.Bytes(), &got2); err != nil {
		t.Fatal(err)
	}
	if !config.Allowed(got2.Preset, config.BackgroundPresets) || !config.Allowed(got2.View, config.Views) {
		t.Fatalf("a value outside the allowlist came out of the API: %+v", got2)
	}
	if got2.Image != "" {
		t.Fatalf("a file name outside the alphabet came out of the API: %q", got2.Image)
	}
}

// TestIconProxyDoesNotReturnTextHTML makes sure a foreign server cannot slip in
// an HTML document under our origin through `icon: https://…`.
func TestIconProxyDoesNotReturnTextHTML(t *testing.T) {
	s := testServer(t)
	kinds := map[string]string{
		"/html": "text/html; charset=utf-8",
		"/json": "application/json",
		"/nic":  "",
		"/png":  "image/png",
		"/svg":  "image/svg+xml",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if kind := kinds[r.URL.Path]; kind != "" {
			w.Header().Set("Content-Type", kind)
		} else {
			w.Header()["Content-Type"] = nil // force a missing header
		}
		_, _ = io.WriteString(w, "<html>x</html>")
	}))
	defer upstream.Close()

	// a client without the local address block — the test server stands on 127.0.0.1
	s.proxyClient = upstream.Client()

	for _, c := range []struct {
		path string
		code int
	}{
		{"/html", http.StatusBadGateway},
		{"/json", http.StatusBadGateway},
		{"/nic", http.StatusBadGateway},
		{"/png", http.StatusOK},
		{"/svg", http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, "/icon/proxy?u="+url.QueryEscape(upstream.URL+c.path), nil)
		r.SetPathValue("name", "proxy")
		w := httptest.NewRecorder()
		s.handleIcon(w, r)
		if w.Code != c.code {
			t.Errorf("%s → %d, want %d (%s)", c.path, w.Code, c.code, w.Body.String())
		}
		if w.Code == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "text/") {
			t.Errorf("%s passed as %q", c.path, w.Header().Get("Content-Type"))
		}
	}

	// the default client must not touch the local network
	s.proxyClient = externalIconClient()
	r := httptest.NewRequest(http.MethodGet, "/icon/proxy?u="+url.QueryEscape(upstream.URL+"/png"), nil)
	r.SetPathValue("name", "proxy")
	w := httptest.NewRecorder()
	s.handleIcon(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("local address → %d, want 502", w.Code)
	}
}
