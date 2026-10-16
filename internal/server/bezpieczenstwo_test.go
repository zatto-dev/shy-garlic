package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// ── SHYG-43b: open redirect ──────────────────────────────────────────────────

func TestBezpiecznyCel(t *testing.T) {
	for _, c := range []struct{ we, chcę string }{
		{"", "/"},
		{"/", "/"},
		{"/ok", "/ok"},
		{"/ok?a=b", "/ok?a=b"},
		{"/a/b?x=1&y=2", "/a/b?x=1&y=2"},
		{"//x", "/"},
		{"//evil.com/path", "/"},
		{`/\evil.com`, "/"},
		{`/\\evil.com`, "/"},
		{`/%5Cevil.com`, "/"},
		{`/%5cevil.com`, "/"},
		{"https://evil.com", "/"},
		{"http://evil.com/x", "/"},
		{"javascript:alert(1)", "/"},
		{"evil.com", "/"},
		{"/ok\r\nSet-Cookie: a=b", "/"},
		{"/ok\tx", "/"},
		{"/\x00x", "/"},
		{"/%0d%0aSet-Cookie:a=b", "/"},
		{"/ścieżka", "/ścieżka"},
	} {
		if got := bezpiecznyCel(c.we); got != c.chcę {
			t.Errorf("bezpiecznyCel(%q) = %q, chcę %q", c.we, got, c.chcę)
		}
	}
}

func postForm(h http.Handler, ścieżka string, dane url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", ścieżka, strings.NewReader(dane.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLoginNextNieOtwieraPrzekierowania(t *testing.T) {
	s := serwerTestowy(t)
	_ = ciasteczkoZalogowanego(t, s) // konto admin / haslo12345
	h := s.Handler()
	for _, next := range []string{`/\evil.com`, `/%5Cevil.com`, "//evil.com", "https://evil.com"} {
		w := postForm(h, "/login", url.Values{"login": {"admin"}, "password": {"haslo12345"}, "next": {next}})
		if w.Code != http.StatusSeeOther {
			t.Fatalf("next=%q: kod %d", next, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q → Location %q, chcę /", next, loc)
		}
	}
	w := postForm(h, "/login", url.Values{"login": {"admin"}, "password": {"haslo12345"}, "next": {"/ok?a=b"}})
	if loc := w.Header().Get("Location"); loc != "/ok?a=b" {
		t.Errorf("poprawny next zgubiony: %q", loc)
	}
}

func TestLangNieOtwieraPrzekierowania(t *testing.T) {
	s := serwerTestowy(t)
	h := s.Handler()
	for _, next := range []string{`/\evil.com`, "//evil.com", "https://evil.com"} {
		w := postForm(h, "/lang", url.Values{"lang": {"pl"}, "next": {next}})
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q → Location %q, chcę /", next, loc)
		}
	}
	w := postForm(h, "/lang", url.Values{"lang": {"pl"}, "next": {"/ok?a=b"}})
	if loc := w.Header().Get("Location"); loc != "/ok?a=b" {
		t.Errorf("poprawny next zgubiony: %q", loc)
	}
	// Referer z obcej strony (GET /lang?set=pl z linku) też nie może wyprowadzić
	r := httptest.NewRequest("GET", "/lang?set=pl", nil)
	r.Header.Set("Referer", "https://evil.com/phish")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if loc := rr.Header().Get("Location"); loc != "/" {
		t.Errorf("obcy Referer → Location %q, chcę /", loc)
	}
	// własny Referer wraca na stronę, z której przyszedł
	r = httptest.NewRequest("GET", "/lang?set=pl", nil)
	r.Host = "panel.test"
	r.Header.Set("Referer", "http://panel.test/ustawienia?x=1")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if loc := rr.Header().Get("Location"); loc != "/ustawienia?x=1" {
		t.Errorf("własny Referer → Location %q", loc)
	}
}

// ── SHYG-43a: schemat adresu usługi ──────────────────────────────────────────

func TestAPIOdrzucaNiebezpieczneAdresy(t *testing.T) {
	s, h, c := serwerKolejność(t)
	przed := string(s.plikConfigu(t))
	for _, u := range []string{"javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,<script>1</script>",
		"vbscript:x", "ftp://host/x", "file:///etc/passwd", "//host/x", "host:80", "http://", " javascript:x"} {
		body := `{"group":"Media","name":"Zły","url":` + jsonStr(u) + `}`
		if w := wyślij(h, c, "POST", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("POST url=%q → %d, chcę 400", u, w.Code)
		}
		body = `{"group":"Media","name":"Sonarr","url":` + jsonStr(u) + `}`
		if w := wyślij(h, c, "PATCH", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("PATCH url=%q → %d, chcę 400", u, w.Code)
		}
		body = `{"group":"Media","services":[{"name":"Zły","url":` + jsonStr(u) + `}]}`
		if w := wyślij(h, c, "POST", "/api/services/bulk", body); w.Code != http.StatusBadRequest {
			t.Errorf("bulk url=%q → %d, chcę 400", u, w.Code)
		}
	}
	if string(s.plikConfigu(t)) != przed {
		t.Error("odrzucone żądanie zmieniło plik")
	}
	// komunikat jest tłumaczony
	w := wyślij(h, c, "POST", "/api/services", `{"group":"Media","name":"Zły","url":"javascript:1"}`, "Accept-Language", "pl")
	if !strings.Contains(w.Body.String(), "http") {
		t.Errorf("komunikat bez wskazania dozwolonych schematów: %s", w.Body)
	}
}

func jsonStr(s string) string {
	b := new(strings.Builder)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestAPIPrzyjmujeDozwoloneAdresy(t *testing.T) {
	_, h, c := serwerKolejność(t)
	for i, u := range []string{"", "http://x:8080/p", "https://x.example", "tcp://10.0.0.5:22"} {
		body := `{"group":"Media","name":"Dobry` + string(rune('A'+i)) + `","url":` + jsonStr(u) + `}`
		if w := wyślij(h, c, "POST", "/api/services", body); w.Code != http.StatusCreated {
			t.Errorf("url=%q → %d %s, chcę 201", u, w.Code, w.Body)
		}
	}
}

// Plik wpisany ręcznie albo przywrócony z historii może mieć dowolny adres.
// Render nie może z niego zrobić klikalnego linku.
func TestRenderNieRobiLinkuZNiebezpiecznegoAdresu(t *testing.T) {
	s := serwerTestowy(t)
	yaml := "groups:\n  - name: G\n    services:\n" +
		"      - name: Zly\n        url: \"javascript:alert(1)\"\n" +
		"      - name: Dane\n        url: \"data:text/html,x\"\n" +
		"      - name: Tcp\n        url: \"tcp://10.0.0.5:22\"\n" +
		"      - name: Dobry\n        url: \"https://ok.example/\"\n"
	if err := os.WriteFile(s.path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := ciasteczkoZalogowanego(t, s)
	w := wyślij(s.Handler(), c, "GET", "/", "")
	html := w.Body.String()
	for _, zły := range []string{`href="javascript`, `href="data:`, `href="tcp:`, "ZgotmplZ"} {
		if strings.Contains(html, zły) {
			t.Errorf("w HTML jest %q", zły)
		}
	}
	if !strings.Contains(html, `href="https://ok.example/"`) {
		t.Error("dobry adres nie jest linkiem")
	}
	// ! data-url było filtrowane przez html/template jak URL: tcp:// zamieniało się
	// w "#ZgotmplZ", a formularz edycji zapisywał to z powrotem do pliku
	if !strings.Contains(html, `data-adres="tcp://10.0.0.5:22"`) {
		t.Error("adres tcp:// nie dociera do formularza edycji")
	}
	// wciąż widać, co sprawdzamy
	if !strings.Contains(html, "10.0.0.5:22") {
		t.Error("host usługi tcp zniknął z kafla")
	}
}

// ── SHYG-43c: limit ciała JSON ───────────────────────────────────────────────

func TestCiałoJSONMaLimit(t *testing.T) {
	s, h, c := serwerKolejność(t)
	przed := string(s.plikConfigu(t))
	duże := strings.Repeat("a", 3<<20)
	for _, ścieżka := range []struct{ metoda, url string }{
		{"POST", "/api/services"},
		{"PATCH", "/api/services"},
		{"POST", "/api/services/move"},
		{"PUT", "/api/title"},
		{"POST", "/api/groups"},
		{"POST", "/api/password"},
		{"PUT", "/api/config"},
	} {
		w := wyślij(h, c, ścieżka.metoda, ścieżka.url, `{"name":"`+duże+`","yaml":"`+duże+`"}`)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s z ciałem 3 MiB → %d, chcę 400/413", ścieżka.metoda, ścieżka.url, w.Code)
		}
	}
	if string(s.plikConfigu(t)) != przed {
		t.Error("zbyt duże ciało zmieniło plik")
	}
}

// Normalny config (kilkaset usług) musi się zmieścić w limicie PUT /api/config.
func TestConfigPutPrzyjmujeDużyPlik(t *testing.T) {
	_, h, c := serwerKolejność(t)
	var b strings.Builder
	b.WriteString("groups:\\n  - name: G\\n    services:\\n")
	for i := 0; i < 5000; i++ {
		b.WriteString("      - name: S")
		b.WriteString(strings.Repeat("x", 5))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(strings.Repeat("y", i%7))
		b.WriteString(url.QueryEscape(string(rune('0' + i%10))))
		b.WriteString(strings.Repeat("z", 20))
		b.WriteString("\\n")
	}
	if b.Len() < 150_000 {
		t.Fatalf("plik testowy za mały: %d", b.Len())
	}
	w := wyślij(h, c, "PUT", "/api/config", `{"yaml":"`+b.String()+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("duży, ale rozsądny config → %d %.200s", w.Code, w.Body)
	}
}

// ── SHYG-43d: zmiana hasła kończy pozostałe sesje ────────────────────────────

func TestZmianaHasłaUnieważniaInneSesje(t *testing.T) {
	s := serwerTestowy(t)
	ctx := context.Background()
	h := s.Handler()
	u, err := s.st.DodajUżytkownika(ctx, "admin", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	inny, err := s.st.DodajUżytkownika(ctx, "drugi", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	tok := func(id int64) *http.Cookie {
		v, _, err := s.st.NowaSesja(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: ciasteczkoSesji, Value: v}
	}
	obecna, skradziona, cudza := tok(u.ID), tok(u.ID), tok(inny.ID)

	w := wyślij(h, obecna, "POST", "/api/password", `{"old":"haslo12345","new":"nowehaslo99"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("zmiana hasła: %d %s", w.Code, w.Body)
	}
	if w := wyślij(h, obecna, "GET", "/api/users", ""); w.Code != http.StatusOK {
		t.Errorf("bieżąca sesja ma zostać: %d", w.Code)
	}
	if w := wyślij(h, skradziona, "GET", "/api/users", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("inna sesja tego użytkownika ma paść: %d", w.Code)
	}
	if w := wyślij(h, cudza, "GET", "/api/users", ""); w.Code != http.StatusOK {
		t.Errorf("sesja innego użytkownika nie może paść: %d", w.Code)
	}
}
