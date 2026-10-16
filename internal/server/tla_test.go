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

const configTestowy = `# Nagłówek testowy — ma przetrwać.

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

// serwerTestowy stawia panel na plikach w katalogu tymczasowym. Usługi celowo
// nie mają adresów — inaczej monitor zacząłby pukać w nieistniejące porty.
func serwerTestowy(t *testing.T) *Server {
	t.Helper()
	katalog := t.TempDir()
	cfg := filepath.Join(katalog, "shygarlic.yaml")
	if err := os.WriteFile(cfg, []byte(configTestowy), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(katalog, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, st, Opcje{BackgroundDir: filepath.Join(katalog, "bg")})
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

func katalogTeł(s *Server) string { return s.tła.dir }

// ciasteczkoZalogowanego zakłada konto i zwraca ciasteczko jego sesji.
func ciasteczkoZalogowanego(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	u, err := s.st.AddUser(context.Background(), "admin", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.st.NewSession(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: ciasteczkoSesji, Value: token}
}

// ── próbki formatów ──────────────────────────────────────────────────────────

func wypełnij(magia []byte) []byte {
	return append(magia, bytes.Repeat([]byte{0x20}, 600)...)
}

func bajtyPNG() []byte  { return wypełnij([]byte("\x89PNG\r\n\x1a\n")) }
func bajtyJPEG() []byte { return wypełnij([]byte{0xff, 0xd8, 0xff, 0xe0}) }
func bajtyGIF() []byte  { return wypełnij([]byte("GIF89a")) }
func bajtyWEBP() []byte { return wypełnij([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")) }
func bajtyAVIF() []byte {
	return wypełnij([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f', 0, 0, 0, 0})
}
func bajtySVG() []byte {
	return wypełnij([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
}

// żądanieUploadu składa multipart z JEDNĄ częścią "file".
func żądanieUploadu(t *testing.T, nazwa string, dane []byte, typCzęści string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+nazwa+`"`)
	if typCzęści != "" {
		h.Set("Content-Type", typCzęści)
	}
	cz, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cz.Write(dane); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/background", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func wgraj(t *testing.T, s *Server, nazwa string, dane []byte, typCzęści string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleTłoUpload(w, żądanieUploadu(t, nazwa, dane, typCzęści))
	return w
}

// ładunkiNaŚcieżkę — nazwy, którymi ktoś próbowałby wyjść z katalogu albo
// wstrzyknąć coś do CSS-a czy nagłówka.
var ładunkiNaŚcieżkę = []string{
	"../x", `..\x`, "..%2f", "%2e%2e%2f", "....//x", "/etc/passwd", "../../shygarlic.db",
	"shygarlic.db", "shygarlic.db-wal", ".ukryty", "-x", "x\x00y", strings.Repeat("a", 300),
	"Logo.PNG", "CON", `a") ; } body{display:none} .x{a:url("`, `x\`, `x\29 `, "x/*",
	"x</style><script>", "tło ze spacją.png", "", ".", "..",
}

func TestSanityzujNazwęTła(t *testing.T) {
	for _, ładunek := range ładunkiNaŚcieżkę {
		nazwa, ok := sanityzujNazwę(ładunek, "png")
		if !ok {
			continue // odmowa jest w porządku
		}
		if !config.NazwaTła.MatchString(nazwa) {
			t.Errorf("%q dało %q — poza alfabetem", ładunek, nazwa)
		}
		if strings.ContainsAny(nazwa, `/\`) || strings.Contains(nazwa, "..") {
			t.Errorf("%q dało %q — to nadal wygląda jak ścieżka", ładunek, nazwa)
		}
	}
	// rozszerzenie bierze się ze sniffa, nie z nazwy
	if n, _ := sanityzujNazwę("obrazek.jpeg", "png"); n != "obrazek.png" {
		t.Errorf("nazwa = %q, chcę obrazek.png", n)
	}
	if n, _ := sanityzujNazwę("....", "png"); n != "tlo.png" {
		t.Errorf("pusty trzon = %q, chcę tlo.png", n)
	}
	if _, ok := sanityzujNazwę("x", "svg"); ok {
		t.Error("format spoza allowlisty nie ma prawa przejść")
	}
}

func TestUploadTłaNieWychodziPozaKatalog(t *testing.T) {
	s := serwerTestowy(t)
	atrapa := filepath.Join(filepath.Dir(katalogTeł(s)), "atrapa")
	if err := os.MkdirAll(atrapa, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(atrapa, "nie-ruszaj.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, ładunek := range ładunkiNaŚcieżkę {
		w := wgraj(t, s, ładunek, bajtyPNG(), "image/png")
		if w.Code != http.StatusCreated && w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Errorf("%q → %d", ładunek, w.Code)
		}
	}
	wpisy, err := os.ReadDir(atrapa)
	if err != nil {
		t.Fatal(err)
	}
	if len(wpisy) != 1 || wpisy[0].Name() != "nie-ruszaj.txt" {
		t.Fatalf("katalog obok teł został ruszony: %+v", wpisy)
	}
	for _, e := range mustReadDir(t, katalogTeł(s)) {
		if !config.NazwaTła.MatchString(e.Name()) {
			t.Errorf("w katalogu teł powstał plik %q spoza alfabetu", e.Name())
		}
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	wpisy, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return wpisy
}

func TestUploadTłaOdrzucaSVG(t *testing.T) {
	s := serwerTestowy(t)
	// nazwa i nagłówek części kłamią — liczy się wyłącznie zawartość
	w := wgraj(t, s, "x.png", bajtySVG(), "image/png")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("kod = %d, chcę 400 (SVG wykonuje skrypt jako dokument)", w.Code)
	}
	if wpisy := mustReadDir(t, katalogTeł(s)); len(wpisy) != 0 {
		t.Fatalf("coś się zapisało: %+v", wpisy)
	}
}

func TestUploadTłaTypZZawartościNieZNazwy(t *testing.T) {
	s := serwerTestowy(t)
	cases := []struct {
		nazwa  string
		dane   []byte
		chcę   string
		chcęMI string
	}{
		{"x.png", bajtyJPEG(), "x.jpg", "image/jpeg"},
		{"x.exe", bajtyPNG(), "x.png", "image/png"},
		{"y.txt", bajtyGIF(), "y.gif", "image/gif"},
		{"z", bajtyWEBP(), "z.webp", "image/webp"},
		{"w.png", bajtyAVIF(), "w.avif", "image/avif"},
	}
	for _, c := range cases {
		w := wgraj(t, s, c.nazwa, c.dane, "application/octet-stream")
		if w.Code != http.StatusCreated {
			t.Errorf("%s → %d (%s)", c.nazwa, w.Code, w.Body.String())
			continue
		}
		var wpis WpisTła
		if err := json.Unmarshal(w.Body.Bytes(), &wpis); err != nil {
			t.Fatal(err)
		}
		if wpis.Nazwa != c.chcę || wpis.Typ != c.chcęMI {
			t.Errorf("%s → %+v, chcę %s / %s", c.nazwa, wpis, c.chcę, c.chcęMI)
		}
	}
}

func TestUploadTłaOdrzucaZaDużyPlik(t *testing.T) {
	s := serwerTestowy(t)
	przed := ileTymczasowychMultipart(t)

	duży := append(bajtyPNG(), bytes.Repeat([]byte{0x41}, 9<<20)...)
	w := wgraj(t, s, "duzy.png", duży, "image/png")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("kod = %d, chcę 413", w.Code)
	}
	if wpisy := mustReadDir(t, katalogTeł(s)); len(wpisy) != 0 {
		t.Fatalf("po odrzuceniu została tymczasówka: %+v", wpisy)
	}
	// dowód, że nie było ParseMultipartForm — ten przelewa nadmiar do os.TempDir()
	if po := ileTymczasowychMultipart(t); po != przed {
		t.Errorf("plików multipart-* w tempdirze: %d → %d", przed, po)
	}
}

func ileTymczasowychMultipart(t *testing.T) int {
	t.Helper()
	pliki, err := filepath.Glob(filepath.Join(os.TempDir(), "multipart-*"))
	if err != nil {
		return 0
	}
	return len(pliki)
}

func TestUploadTłaKolizjaDajeSufiks(t *testing.T) {
	s := serwerTestowy(t)
	chcę := []string{"tlo.png", "tlo-2.png", "tlo-3.png"}
	for i, nazwa := range chcę {
		w := wgraj(t, s, "tlo.png", bajtyPNG(), "image/png")
		if w.Code != http.StatusCreated {
			t.Fatalf("upload %d → %d (%s)", i+1, w.Code, w.Body.String())
		}
		var wpis WpisTła
		_ = json.Unmarshal(w.Body.Bytes(), &wpis)
		if wpis.Nazwa != nazwa {
			t.Fatalf("upload %d dał %q, chcę %q", i+1, wpis.Nazwa, nazwa)
		}
	}
	if wpisy := mustReadDir(t, katalogTeł(s)); len(wpisy) != 3 {
		t.Fatalf("plików = %d, chcę 3 — któryś nadpisał poprzedni", len(wpisy))
	}
}

func TestUploadTłaLimitPlików(t *testing.T) {
	s := serwerTestowy(t)
	for i := range limitPlikówTła {
		nazwa := filepath.Join(katalogTeł(s), "stare"+string(rune('a'+i%26))+string(rune('a'+i/26))+".png")
		if err := os.WriteFile(nazwa, bajtyPNG(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := wgraj(t, s, "jeszcze.png", bajtyPNG(), "image/png")
	if w.Code != http.StatusConflict {
		t.Fatalf("kod = %d, chcę 409 przy %d plikach", w.Code, limitPlikówTła)
	}
}

func TestBgSerwujeNagłówkiSandbox(t *testing.T) {
	s := serwerTestowy(t)
	ciastko := ciasteczkoZalogowanego(t, s)
	if w := wgraj(t, s, "x.png", bajtyPNG(), "image/png"); w.Code != http.StatusCreated {
		t.Fatalf("upload → %d", w.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "/bg/x.png", nil)
	r.AddCookie(ciastko)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("kod = %d (%s)", w.Code, w.Body.String())
	}
	chcę := map[string]string{
		"Content-Type":            "image/png",
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"Content-Disposition":     `inline; filename="x.png"`,
		"Cache-Control":           "private, max-age=0, must-revalidate",
		"X-Content-Type-Options":  "nosniff",
	}
	for k, v := range chcę {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, chcę %q", k, got, v)
		}
	}
}

func TestBgWymagaLogowania(t *testing.T) {
	s := serwerTestowy(t)
	_ = ciasteczkoZalogowanego(t, s) // żeby /setup nie przejął żądania
	if w := wgraj(t, s, "x.png", bajtyPNG(), "image/png"); w.Code != http.StatusCreated {
		t.Fatalf("upload → %d", w.Code)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/bg/x.png", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("kod = %d, chcę 303 na /login", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("PNG")) {
		t.Fatal("niezalogowany dostał bajty obrazu")
	}
}

func TestUsuńTłoNieWychodziPozaKatalog(t *testing.T) {
	s := serwerTestowy(t)
	obok := filepath.Join(filepath.Dir(katalogTeł(s)), "waznyplik.txt")
	if err := os.WriteFile(obok, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ładunek := range append(ładunkiNaŚcieżkę, "../waznyplik.txt", "x.png") {
		r := httptest.NewRequest(http.MethodDelete, "/api/background/x", nil)
		r.SetPathValue("nazwa", ładunek)
		w := httptest.NewRecorder()
		s.handleTłoUsuń(w, r)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Errorf("%q → %d, chcę 400 albo 404", ładunek, w.Code)
		}
	}
	if _, err := os.Stat(obok); err != nil {
		t.Fatalf("plik obok katalogu teł zniknął: %v", err)
	}
}

func TestUsuńTłoUżywaneOdmawia409(t *testing.T) {
	s := serwerTestowy(t)
	if w := wgraj(t, s, "uzywane.png", bajtyPNG(), "image/png"); w.Code != http.StatusCreated {
		t.Fatalf("upload → %d", w.Code)
	}
	if w := putWyglądu(t, s, `{"preset":"photo","image":"uzywane.png"}`, false); w.Code != http.StatusOK {
		t.Fatalf("zapis wyglądu → %d (%s)", w.Code, w.Body.String())
	}

	r := httptest.NewRequest(http.MethodDelete, "/api/background/uzywane.png", nil)
	r.SetPathValue("nazwa", "uzywane.png")
	w := httptest.NewRecorder()
	s.handleTłoUsuń(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("kod = %d, chcę 409 — najpierw zmiana wyglądu, potem kasowanie", w.Code)
	}
	if _, err := os.Stat(filepath.Join(katalogTeł(s), "uzywane.png")); err != nil {
		t.Fatalf("plik zniknął mimo 409: %v", err)
	}
}

// ── wygląd ───────────────────────────────────────────────────────────────────

func putWyglądu(t *testing.T, s *Server, ciało string, dry bool) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/appearance"
	if dry {
		url += "?dry=1"
	}
	r := httptest.NewRequest(http.MethodPut, url, strings.NewReader(ciało))
	w := httptest.NewRecorder()
	s.handleWyglądPut(w, r)
	return w
}

func TestWyglądPutWalidujeKażdePole(t *testing.T) {
	cases := []struct {
		ciało string
		kod   int
	}{
		{`{"preset":"hocus"}`, http.StatusBadRequest},
		{`{"tone":"x"}`, http.StatusBadRequest},
		{`{"view":"x"}`, http.StatusBadRequest},
		{`{"blur":-1}`, http.StatusBadRequest},
		{`{"blur":31}`, http.StatusBadRequest},
		{`{"preset":"photo","image":"../x"}`, http.StatusBadRequest},
		{`{"image":"x.png"}`, http.StatusBadRequest}, // preset nie jest photo
		{`{"preset":"photo","image":"nie-ma-mnie.png"}`, http.StatusNotFound},
		{`{"preset":"photo"}`, http.StatusBadRequest}, // zdjęcie bez pliku
		{`nie json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		s := serwerTestowy(t)
		przed, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatal(err)
		}
		w := putWyglądu(t, s, c.ciało, false)
		if w.Code != c.kod {
			t.Errorf("%s → %d, chcę %d (%s)", c.ciało, w.Code, c.kod, w.Body.String())
		}
		po, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(przed, po) {
			t.Errorf("%s ruszyło plik mimo odmowy", c.ciało)
		}
	}
}

func TestWyglądPutIdzieProzezDiff(t *testing.T) {
	s := serwerTestowy(t)
	przed, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}

	w := putWyglądu(t, s, `{"preset":"fog","blur":0,"view":"list"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("dry → %d (%s)", w.Code, w.Body.String())
	}
	var odp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &odp); err != nil {
		t.Fatal(err)
	}
	for _, klucz := range []string{"dry", "diff", "added", "removed", "summary"} {
		if _, jest := odp[klucz]; !jest {
			t.Errorf("w odpowiedzi brakuje %q: %v", klucz, odp)
		}
	}
	if po, _ := os.ReadFile(s.path); !bytes.Equal(przed, po) {
		t.Fatal("podgląd zmienił plik")
	}

	if w := putWyglądu(t, s, `{"preset":"fog","blur":0,"view":"list"}`, false); w.Code != http.StatusOK {
		t.Fatalf("zapis → %d (%s)", w.Code, w.Body.String())
	}
	cfg, err := config.Load(mustRead(t, s.path))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Appearance.Background.Preset != "fog" || cfg.Appearance.View != "list" ||
		cfg.Appearance.Background.Blur != 0 {
		t.Fatalf("zapisany wygląd = %+v", cfg.Appearance)
	}
	if !strings.Contains(string(mustRead(t, s.path)), "# Stopka testowa: ma zostać na dole.") {
		t.Fatal("zapis wyglądu zjadł komentarz stopki")
	}
	wersje, err := s.st.Versions(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(wersje) != 1 {
		t.Fatalf("wersji w historii = %d, chcę 1", len(wersje))
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

func TestWyglądGetIInfoNieWypuszczająŚmieci(t *testing.T) {
	s := serwerTestowy(t)
	s.mu.Lock()
	s.cfg.Appearance.Background.Preset = "<script>"
	s.cfg.Appearance.Background.Image = `x") ; }`
	s.cfg.Appearance.View = "hocus"
	s.mu.Unlock()

	w := httptest.NewRecorder()
	s.handleWyglądGet(w, httptest.NewRequest(http.MethodGet, "/api/appearance", nil))
	var wy wyglądAPI
	if err := json.Unmarshal(w.Body.Bytes(), &wy); err != nil {
		t.Fatal(err)
	}
	if !config.Dozwolone(wy.Preset, config.PresetyTła) || !config.Dozwolone(wy.Widok, config.Widoki) {
		t.Fatalf("z API wyszła wartość spoza allowlisty: %+v", wy)
	}
	if wy.Obraz != "" {
		t.Fatalf("nazwa pliku spoza alfabetu wyszła z API: %q", wy.Obraz)
	}
}

// TestIkonaProxyNieOddajeTextHtml pilnuje, żeby obcy serwer nie mógł wstawić
// dokumentu HTML pod nasz origin przez `icon: https://…`.
func TestIkonaProxyNieOddajeTextHtml(t *testing.T) {
	s := serwerTestowy(t)
	typy := map[string]string{
		"/html": "text/html; charset=utf-8",
		"/json": "application/json",
		"/nic":  "",
		"/png":  "image/png",
		"/svg":  "image/svg+xml",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if typ := typy[r.URL.Path]; typ != "" {
			w.Header().Set("Content-Type", typ)
		} else {
			w.Header()["Content-Type"] = nil // wymuś brak nagłówka
		}
		_, _ = io.WriteString(w, "<html>x</html>")
	}))
	defer upstream.Close()

	// klient bez blokady adresów lokalnych — serwer testowy stoi na 127.0.0.1
	s.klientProxy = upstream.Client()

	for _, c := range []struct {
		ścieżka string
		kod     int
	}{
		{"/html", http.StatusBadGateway},
		{"/json", http.StatusBadGateway},
		{"/nic", http.StatusBadGateway},
		{"/png", http.StatusOK},
		{"/svg", http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, "/icon/proxy?u="+url.QueryEscape(upstream.URL+c.ścieżka), nil)
		r.SetPathValue("name", "proxy")
		w := httptest.NewRecorder()
		s.handleIcon(w, r)
		if w.Code != c.kod {
			t.Errorf("%s → %d, chcę %d (%s)", c.ścieżka, w.Code, c.kod, w.Body.String())
		}
		if w.Code == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "text/") {
			t.Errorf("%s przeszedł jako %q", c.ścieżka, w.Header().Get("Content-Type"))
		}
	}

	// domyślny klient nie ma prawa dotknąć sieci lokalnej
	s.klientProxy = klientIkonZewnętrznych()
	r := httptest.NewRequest(http.MethodGet, "/icon/proxy?u="+url.QueryEscape(upstream.URL+"/png"), nil)
	r.SetPathValue("name", "proxy")
	w := httptest.NewRecorder()
	s.handleIcon(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("adres lokalny → %d, chcę 502", w.Code)
	}
}
