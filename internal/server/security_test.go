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

func TestSafeTarget(t *testing.T) {
	for _, c := range []struct{ input, want string }{
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
		if got := safeTarget(c.input); got != c.want {
			t.Errorf("safeTarget(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func postForm(h http.Handler, path string, data url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(data.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLoginNextDoesNotOpenRedirect(t *testing.T) {
	s := testServer(t)
	_ = loggedInCookie(t, s) // account admin / haslo12345
	h := s.Handler()
	for _, next := range []string{`/\evil.com`, `/%5Cevil.com`, "//evil.com", "https://evil.com"} {
		w := postForm(h, "/login", url.Values{"login": {"admin"}, "password": {"haslo12345"}, "next": {next}})
		if w.Code != http.StatusSeeOther {
			t.Fatalf("next=%q: code %d", next, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q → Location %q, want /", next, loc)
		}
	}
	w := postForm(h, "/login", url.Values{"login": {"admin"}, "password": {"haslo12345"}, "next": {"/ok?a=b"}})
	if loc := w.Header().Get("Location"); loc != "/ok?a=b" {
		t.Errorf("valid next lost: %q", loc)
	}
}

func TestLangDoesNotOpenRedirect(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	for _, next := range []string{`/\evil.com`, "//evil.com", "https://evil.com"} {
		w := postForm(h, "/lang", url.Values{"lang": {"pl"}, "next": {next}})
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q → Location %q, want /", next, loc)
		}
	}
	w := postForm(h, "/lang", url.Values{"lang": {"pl"}, "next": {"/ok?a=b"}})
	if loc := w.Header().Get("Location"); loc != "/ok?a=b" {
		t.Errorf("valid next lost: %q", loc)
	}
	// A Referer from a foreign page (GET /lang?set=pl from a link) also must not lead out
	r := httptest.NewRequest("GET", "/lang?set=pl", nil)
	r.Header.Set("Referer", "https://evil.com/phish")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if loc := rr.Header().Get("Location"); loc != "/" {
		t.Errorf("foreign Referer → Location %q, want /", loc)
	}
	// our own Referer goes back to the page it came from
	r = httptest.NewRequest("GET", "/lang?set=pl", nil)
	r.Host = "panel.test"
	r.Header.Set("Referer", "http://panel.test/ustawienia?x=1")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if loc := rr.Header().Get("Location"); loc != "/ustawienia?x=1" {
		t.Errorf("own Referer → Location %q", loc)
	}
}

// ── SHYG-43a: service address scheme ─────────────────────────────────────────

func TestAPIRejectsUnsafeAddresses(t *testing.T) {
	s, h, c := orderServer(t)
	before := string(s.configFile(t))
	for _, u := range []string{"javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,<script>1</script>",
		"vbscript:x", "ftp://host/x", "file:///etc/passwd", "//host/x", "host:80", "http://", " javascript:x"} {
		body := `{"group":"Media","name":"Zły","url":` + jsonStr(u) + `}`
		if w := send(h, c, "POST", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("POST url=%q → %d, want 400", u, w.Code)
		}
		body = `{"group":"Media","name":"Sonarr","url":` + jsonStr(u) + `}`
		if w := send(h, c, "PATCH", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("PATCH url=%q → %d, want 400", u, w.Code)
		}
		body = `{"group":"Media","services":[{"name":"Zły","url":` + jsonStr(u) + `}]}`
		if w := send(h, c, "POST", "/api/services/bulk", body); w.Code != http.StatusBadRequest {
			t.Errorf("bulk url=%q → %d, want 400", u, w.Code)
		}
	}
	if string(s.configFile(t)) != before {
		t.Error("a rejected request changed the file")
	}
	// the message is translated
	w := send(h, c, "POST", "/api/services", `{"group":"Media","name":"Zły","url":"javascript:1"}`, "Accept-Language", "pl")
	if !strings.Contains(w.Body.String(), "http") {
		t.Errorf("the message does not name the allowed schemes: %s", w.Body)
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

func TestAPIAcceptsAllowedAddresses(t *testing.T) {
	_, h, c := orderServer(t)
	for i, u := range []string{"", "http://x:8080/p", "https://x.example", "tcp://10.0.0.5:22"} {
		body := `{"group":"Media","name":"Dobry` + string(rune('A'+i)) + `","url":` + jsonStr(u) + `}`
		if w := send(h, c, "POST", "/api/services", body); w.Code != http.StatusCreated {
			t.Errorf("url=%q → %d %s, want 201", u, w.Code, w.Body)
		}
	}
}

// A file typed by hand or restored from history can have any address.
// The render must not make a clickable link out of it.
func TestRenderDoesNotLinkUnsafeAddress(t *testing.T) {
	s := testServer(t)
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
	c := loggedInCookie(t, s)
	w := send(s.Handler(), c, "GET", "/", "")
	html := w.Body.String()
	for _, bad := range []string{`href="javascript`, `href="data:`, `href="tcp:`, "ZgotmplZ"} {
		if strings.Contains(html, bad) {
			t.Errorf("HTML contains %q", bad)
		}
	}
	if !strings.Contains(html, `href="https://ok.example/"`) {
		t.Error("a good address is not a link")
	}
	// ! data-url was filtered by html/template as a URL: tcp:// turned into
	// "#ZgotmplZ", and the edit form saved that back to the file
	if !strings.Contains(html, `data-adres="tcp://10.0.0.5:22"`) {
		t.Error("a tcp:// address does not reach the edit form")
	}
	// you can still see what we are checking
	if !strings.Contains(html, "10.0.0.5:22") {
		t.Error("the tcp service host vanished from the tile")
	}
}

// ── SHYG-43c: JSON body limit ────────────────────────────────────────────────

func TestJSONBodyHasLimit(t *testing.T) {
	s, h, c := orderServer(t)
	before := string(s.configFile(t))
	large := strings.Repeat("a", 3<<20)
	for _, path := range []struct{ method, url string }{
		{"POST", "/api/services"},
		{"PATCH", "/api/services"},
		{"POST", "/api/services/move"},
		{"PUT", "/api/title"},
		{"POST", "/api/groups"},
		{"POST", "/api/password"},
		{"PUT", "/api/config"},
	} {
		w := send(h, c, path.method, path.url, `{"name":"`+large+`","yaml":"`+large+`"}`)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with a 3 MiB body → %d, want 400/413", path.method, path.url, w.Code)
		}
	}
	if string(s.configFile(t)) != before {
		t.Error("an oversized body changed the file")
	}
}

// A normal config (a few hundred services) must fit in the PUT /api/config limit.
func TestConfigPutAcceptsLargeFile(t *testing.T) {
	_, h, c := orderServer(t)
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
		t.Fatalf("test file too small: %d", b.Len())
	}
	w := send(h, c, "PUT", "/api/config", `{"yaml":"`+b.String()+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("large but reasonable config → %d %.200s", w.Code, w.Body)
	}
}

// ── SHYG-43d: a password change ends the other sessions ──────────────────────

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	h := s.Handler()
	u, err := s.st.AddUser(ctx, "admin", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.st.AddUser(ctx, "drugi", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	tok := func(id int64) *http.Cookie {
		v, _, err := s.st.NewSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: v}
	}
	current, stolen, foreign := tok(u.ID), tok(u.ID), tok(other.ID)

	w := send(h, current, "POST", "/api/password", `{"old":"haslo12345","new":"nowehaslo99"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("password change: %d %s", w.Code, w.Body)
	}
	if w := send(h, current, "GET", "/api/users", ""); w.Code != http.StatusOK {
		t.Errorf("the current session must stay: %d", w.Code)
	}
	if w := send(h, stolen, "GET", "/api/users", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("another session of this user must fall: %d", w.Code)
	}
	if w := send(h, foreign, "GET", "/api/users", ""); w.Code != http.StatusOK {
		t.Errorf("another user's session must not fall: %d", w.Code)
	}
}
