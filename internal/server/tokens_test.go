package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// sendWithoutCookie — a request without a session, like from Prometheus or curl.
// The Sec-Fetch-Site header is set so that the CSRF layer does not mask
// what we are checking (authorization).
func sendWithoutCookie(h http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	return sendToAddress(h, "192.0.2.10:4000", method, path, body, headers...)
}

func sendToAddress(h http.Handler, remote, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func bearer(token string) []string { return []string{"Authorization", "Bearer " + token} }

func newToken(t *testing.T, s *Server) string {
	t.Helper()
	token, _, err := s.st.NewAPIToken(s.ctx, "test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// ── token management (session) ───────────────────────────────────────────────

func TestAPITokensCreateListRevoke(t *testing.T) {
	s, h, c := descriptionsServer(t)

	w := send(h, c, "POST", "/api/tokens", `{"name":"prometheus"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	var created struct {
		Token string `json:"token"`
		ID    int64  `json:"id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "shyg_") || len(created.Token) != 69 || created.Name != "prometheus" || created.ID == 0 {
		t.Fatalf("response: %+v", created)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("token in a response without no-store: %q", cc)
	}

	// the list does not reveal the token — neither whole nor its hash
	w = send(h, c, "GET", "/api/tokens", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	if strings.Contains(w.Body.String(), strings.TrimPrefix(created.Token, "shyg_")) {
		t.Errorf("list contains a secret: %s", w.Body)
	}
	var list struct {
		Tokens []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Hint     string `json:"hint"`
			Created  string `json:"created"`
			LastUsed string `json:"lastUsed"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tokens) != 1 || list.Tokens[0].Name != "prometheus" || list.Tokens[0].LastUsed != "" ||
		list.Tokens[0].Created == "" || !strings.HasPrefix(created.Token, list.Tokens[0].Hint) {
		t.Errorf("list: %+v", list.Tokens)
	}

	// the token works, after revoking it stops
	if w := sendWithoutCookie(h, "GET", "/api/status", "", bearer(created.Token)...); w.Code != http.StatusOK {
		t.Fatalf("fresh token: code %d", w.Code)
	}
	w = send(h, c, "DELETE", "/api/tokens/"+itoa(created.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: code %d: %s", w.Code, w.Body)
	}
	if w := sendWithoutCookie(h, "GET", "/api/status", "", bearer(created.Token)...); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: code %d, want 401", w.Code)
	}
	if w := send(h, c, "DELETE", "/api/tokens/"+itoa(created.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("second revoke: code %d, want 404", w.Code)
	}
	_ = s
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }

func TestAPITokensNameValidation(t *testing.T) {
	_, h, c := descriptionsServer(t)
	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("a", 65) + `"}`, `{"name":"a\nb"}`, `{}`, `nie json`} {
		w := send(h, c, "POST", "/api/tokens", body, "Accept-Language", "pl")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: code %d, want 400", body, w.Code)
		}
	}
	w := send(h, c, "POST", "/api/tokens", `{"name":""}`, "Accept-Language", "pl")
	if !strings.Contains(w.Body.String(), i18n.T(i18n.PL, "blad_token_nazwa")[:20]) {
		t.Errorf("message in Polish: %s", w.Body)
	}
}

func TestAPITokensCountLimit(t *testing.T) {
	_, h, c := descriptionsServer(t)
	for i := 0; i < store.MaxAPITokens; i++ {
		if w := send(h, c, "POST", "/api/tokens", `{"name":"t"}`); w.Code != http.StatusCreated {
			t.Fatalf("token %d: code %d", i, w.Code)
		}
	}
	if w := send(h, c, "POST", "/api/tokens", `{"name":"t"}`); w.Code != http.StatusConflict {
		t.Errorf("over the limit: code %d, want 409", w.Code)
	}
}

// ── bearer: only three GET routes ────────────────────────────────────────────

func TestBearerWorksOnlyOnThreeRoutes(t *testing.T) {
	s, h, _ := descriptionsServer(t)
	token := newToken(t, s)
	before := string(s.configFile(t))

	allowed := []string{"/api/status", "/api/history?key=Media%2FJellyfin", "/metrics"}
	for _, path := range allowed {
		if w := sendWithoutCookie(h, "GET", path, "", bearer(token)...); w.Code != http.StatusOK {
			t.Errorf("GET %s with a token: code %d, want 200", path, w.Code)
		}
		// and without a token the same request does not pass — 401, not a redirect
		w := sendWithoutCookie(h, "GET", path, "")
		if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" {
			t.Errorf("GET %s without a token: code %d, Location %q — want 401 without a redirect", path, w.Code, w.Header().Get("Location"))
		}
	}

	rejected := []struct{ method, path, body string }{
		{"GET", "/api/config", ""},
		{"GET", "/api/tokens", ""},
		{"GET", "/api/users", ""},
		{"GET", "/api/service?key=Media%2FJellyfin", ""},
		{"GET", "/api/info", ""},
		{"GET", "/api/instance", ""},
		{"GET", "/api/notify", ""},
		{"GET", "/api/versions", ""},
		{"GET", "/api/appearance", ""},
		{"GET", "/api/icons?q=a", ""},
		{"POST", "/api/tokens", `{"name":"x"}`},
		{"DELETE", "/api/tokens/1", ""},
		{"POST", "/api/services", `{"group":"Media","name":"X","url":"http://x.home.arpa"}`},
		{"PATCH", "/api/services", `{"group":"Media","name":"Jellyfin","url":"http://y.home.arpa"}`},
		{"DELETE", "/api/services?group=Media&name=Jellyfin", ""},
		{"POST", "/api/services/move", `{"group":"Media","name":"Jellyfin","index":0}`},
		{"POST", "/api/check?key=Media%2FJellyfin", ""},
		{"PUT", "/api/notify", `{"webhook":"https://x.test/h"}`},
		{"POST", "/api/notify/test", ""},
		{"PUT", "/api/config", `{"yaml":"groups: []"}`},
		{"PUT", "/api/title", `{"title":"x"}`},
		{"POST", "/api/users", `{"login":"x","password":"xxxxxxxx"}`},
		{"POST", "/api/password", `{"old":"a","new":"bbbbbbbb"}`},
		{"PUT", "/api/appearance", `{"view":"list"}`},
		{"POST", "/api/groups", `{"name":"Nowa"}`},
	}
	for _, c := range rejected {
		w := sendWithoutCookie(h, c.method, c.path, c.body, bearer(token)...)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a token: code %d, want 401 (bearer has no right here)", c.method, c.path, w.Code)
		}
	}
	// event stream: an SSE client always sends Accept: text/event-stream
	if w := sendWithoutCookie(h, "GET", "/events", "", append(bearer(token), "Accept", "text/event-stream")...); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /events with a token: code %d, want 401", w.Code)
	}
	// home page: bearer does not open the panel, it only sends to login
	if w := sendWithoutCookie(h, "GET", "/", "", bearer(token)...); w.Code != http.StatusSeeOther ||
		!strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Errorf("GET / with a token: code %d, Location %q", w.Code, w.Header().Get("Location"))
	}

	if string(s.configFile(t)) != before {
		t.Error("config file changed by requests with a token")
	}
	if l, _ := s.st.APITokens(s.ctx); len(l) != 1 {
		t.Errorf("tokens after attempts: %d, want 1", len(l))
	}
}

func TestBearerOnlyFromHeaderAndOnlyGET(t *testing.T) {
	s, h, _ := descriptionsServer(t)
	token := newToken(t, s)

	// a token in the query string or a cookie is not a token
	if w := sendWithoutCookie(h, "GET", "/api/status?token="+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("token in query: code %d", w.Code)
	}
	if w := sendWithoutCookie(h, "GET", "/api/status", "", "Cookie", "shyg_session="+token); w.Code != http.StatusUnauthorized {
		t.Errorf("token as a session cookie: code %d", w.Code)
	}
	// other schemes and junk
	for _, header := range []string{"Basic " + token, "Bearer", "Bearer ", token, "Bearer " + token + "x", "Bearer  " + token + " extra"} {
		if w := sendWithoutCookie(h, "GET", "/api/status", "", "Authorization", header); w.Code != http.StatusUnauthorized {
			t.Errorf("%q: code %d, want 401", header, w.Code)
		}
	}
	// the RFC 7235 scheme is case-insensitive
	if w := sendWithoutCookie(h, "GET", "/api/status", "", "Authorization", "bearer "+token); w.Code != http.StatusOK {
		t.Errorf("bearer (lowercase): code %d", w.Code)
	}
	// GET only: HEAD and POST on a token route do not pass
	for _, m := range []string{"POST", "PUT", "DELETE", "HEAD"} {
		if w := sendWithoutCookie(h, m, "/api/status", "", bearer(token)...); w.Code == http.StatusOK {
			t.Errorf("%s /api/status with a token passed", m)
		}
	}
}

func TestSessionStillWorksOnTokenRoutes(t *testing.T) {
	_, h, c := descriptionsServer(t)
	for _, path := range []string{"/api/status", "/metrics", "/api/history?key=Media%2FJellyfin"} {
		if w := send(h, c, "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s with a session: code %d", path, w.Code)
		}
	}
}

// A fresh instance (no accounts) redirects to /setup — but a scraper should get
// 401, not the wizard's HTML.
func TestMetricsOnFreshInstanceIs401NotRedirect(t *testing.T) {
	s := testServer(t) // no users
	h := s.Handler()
	for _, hdr := range [][]string{nil, bearer("shyg_" + strings.Repeat("a", 64))} {
		w := sendWithoutCookie(h, "GET", "/metrics", "", hdr...)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" {
			t.Errorf("code %d, Location %q", w.Code, w.Header().Get("Location"))
		}
	}
	// an ordinary page still points to /setup
	if w := sendWithoutCookie(h, "GET", "/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup" {
		t.Errorf("GET /: code %d, Location %q", w.Code, w.Header().Get("Location"))
	}
}

func TestResponse401HasWWWAuthenticate(t *testing.T) {
	s, h, _ := descriptionsServer(t)
	_ = s
	w := sendWithoutCookie(h, "GET", "/metrics", "")
	if !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
	}
}

func TestTokenLastUsedUpdatedRarely(t *testing.T) {
	s, h, _ := descriptionsServer(t)
	token := newToken(t, s)
	fetch := func() string {
		l, _ := s.st.APITokens(s.ctx)
		return l[0].LastUsedAt.String()
	}
	if l, _ := s.st.APITokens(s.ctx); !l[0].LastUsedAt.IsZero() {
		t.Fatal("a new token has last_used")
	}
	sendWithoutCookie(h, "GET", "/api/status", "", bearer(token)...)
	first := fetch()
	if strings.HasPrefix(first, "0001") {
		t.Fatal("last_used not set after use")
	}
	for i := 0; i < 5; i++ {
		sendWithoutCookie(h, "GET", "/api/status", "", bearer(token)...)
	}
	if fetch() != first {
		t.Error("last_used changed more often than once a minute")
	}
}

func TestBadTokensAreRateLimited(t *testing.T) {
	s, h, _ := descriptionsServer(t)
	token := newToken(t, s)
	bad := "shyg_" + strings.Repeat("0", 64)
	limit := 0
	for i := 0; i < 500; i++ {
		w := sendToAddress(h, "198.51.100.7:1000", "GET", "/metrics", "", bearer(bad)...)
		if w.Code == http.StatusTooManyRequests {
			limit = i
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: code %d", i, w.Code)
		}
	}
	if limit == 0 {
		t.Fatal("no attempt limit with a wrong token")
	}
	// the same address, even with a good token, waits; another address is untouched
	if w := sendToAddress(h, "198.51.100.7:1000", "GET", "/metrics", "", bearer(token)...); w.Code != http.StatusTooManyRequests {
		t.Errorf("blocked address with a good token: code %d", w.Code)
	}
	if w := sendToAddress(h, "198.51.100.8:1000", "GET", "/metrics", "", bearer(token)...); w.Code != http.StatusOK {
		t.Errorf("another address: code %d", w.Code)
	}
	// the limit does not share a counter with login
	if !s.lim.allow("198.51.100.7") {
		t.Error("bad tokens used up the login attempt limit")
	}
}

// ── unauthenticated /metrics for the given networks ─────────────────────────

func TestMetricsWithCIDR(t *testing.T) {
	t.Cleanup(func() { _ = SetTrustedProxy(nil) })

	cases := []struct {
		name    string
		allow   []string
		proxy   []string
		remote  string
		headers []string
		want    int
	}{
		{"empty list by default — token required", nil, nil, "192.168.0.50:1", nil, 401},
		{"listed address, no token", []string{"192.168.0.0/24"}, nil, "192.168.0.50:1", nil, 200},
		{"adres spoza listy", []string{"192.168.0.0/24"}, nil, "10.9.9.9:1", nil, 401},
		{"bare address on the list", []string{"192.168.0.50"}, nil, "192.168.0.50:1", nil, 200},
		{"IPv6", []string{"fd00::/8"}, nil, "[fd00::5]:1", nil, 200},
		{"IPv4 jako ::ffff:", []string{"192.168.0.0/24"}, nil, "[::ffff:192.168.0.9]:1", nil, 200},
		{"XFF from an untrusted peer — cannot be classified, refusal",
			[]string{"192.168.0.0/24"}, nil, "192.168.0.50:1", []string{"X-Forwarded-For", "192.168.0.7"}, 401},
		{"X-Real-IP od niezaufanego peera — odmowa",
			[]string{"192.168.0.0/24"}, nil, "192.168.0.50:1", []string{"X-Real-IP", "192.168.0.7"}, 401},
		{"Forwarded od niezaufanego peera — odmowa",
			[]string{"192.168.0.0/24"}, nil, "192.168.0.50:1", []string{"Forwarded", "for=192.168.0.7"}, 401},
		{"zaufane proxy, klient z listy", []string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7"}, 200},
		{"zaufane proxy, klient spoza listy", []string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "8.8.8.8"}, 401},
		{"spoofing: a stranger put their own XFF on the left, the proxy appended the real address on the right",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7, 8.8.8.8"}, 401},
		{"a fake entry on the left does no harm when the right one is on the list",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "8.8.8.8, 192.168.0.7"}, 200},
		{"chain of trusted proxies — we skip them from the right",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.0/24"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7, 10.0.0.2"}, 200},
		{"junk in XFF — refusal", []string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "unknown"}, 401},
		{"trusted proxy without XFF — the proxy address decides (and is not on the list)",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1", nil, 401},
		{"trusted proxy without XFF, and the proxy address is on the list — refusal (otherwise the whole internet looks like the proxy)",
			[]string{"10.0.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1", nil, 401},
		{"trusted proxy with an empty XFF, proxy address on the list — refusal",
			[]string{"10.0.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1", []string{"X-Forwarded-For", " , "}, 401},
		{"trusted proxy with XFF, client from the list — still works",
			[]string{"10.0.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1", []string{"X-Forwarded-For", "10.0.0.7"}, 200},
		{"a trusted peer does not mean allowed", nil, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7"}, 401},
	}
	for _, c := range cases {
		s := testServer(t)
		loggedInCookie(t, s) // the account exists, so /metrics does not go to /setup
		if err := s.setMetricsAllow(c.allow); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := SetTrustedProxy(c.proxy); err != nil {
			t.Fatal(err)
		}
		w := sendToAddress(s.Handler(), c.remote, "GET", "/metrics", "", c.headers...)
		if w.Code != c.want {
			t.Errorf("%s: code %d, want %d", c.name, w.Code, c.want)
		}
	}
}

// The network list opens ONLY /metrics. The rest, also routes accepting a
// token, still requires a session or a token.
func TestCIDRDoesNotOpenOtherRoutes(t *testing.T) {
	s := testServer(t)
	loggedInCookie(t, s)
	if err := s.setMetricsAllow([]string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	if w := sendWithoutCookie(h, "GET", "/metrics", ""); w.Code != http.StatusOK {
		t.Fatalf("/metrics: %d", w.Code)
	}
	for _, path := range []string{"/api/status", "/api/history?key=a", "/api/config", "/api/tokens"} {
		if w := sendWithoutCookie(h, "GET", path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s from a listed address: code %d, want 401", path, w.Code)
		}
	}
	if w := sendWithoutCookie(h, "POST", "/metrics", ""); w.Code == http.StatusOK {
		t.Error("POST /metrics passed")
	}
}

func TestMetricsAllowRejectsBadCIDR(t *testing.T) {
	s := testServer(t)
	if err := s.setMetricsAllow([]string{"nie-cidr"}); err == nil {
		t.Error("a bad CIDR was accepted")
	}
	if err := s.setMetricsAllow([]string{" 10.0.0.0/8 ", "", "192.168.1.5"}); err != nil {
		t.Errorf("spaces and a bare address must pass: %v", err)
	}
}

// ipAddress (the login limiter) now uses the same rule: the rightmost
// untrusted entry, not the first from the left, which the client writes itself.
func TestIPAddressTakesRightmostUntrustedEntry(t *testing.T) {
	t.Cleanup(func() { _ = SetTrustedProxy(nil) })
	if err := SetTrustedProxy([]string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:5"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if got := ipAddress(r); got != "203.0.113.9" {
		t.Errorf("ipAddress = %q, want 203.0.113.9", got)
	}
	_ = os.Stdout
}

func TestTokenTextsHaveBothLanguages(t *testing.T) {
	keys := []string{"tokeny_tytul", "tokeny_opis", "tokeny_nazwa", "tokeny_utworz", "tokeny_pokazany_raz",
		"tokeny_brak", "tokeny_odwolaj", "tokeny_potwierdz", "tokeny_uzyty", "tokeny_nieuzyty",
		"tokeny_utworzony", "tokeny_odwolany", "kopiuj", "skopiowano", "blad_token_nazwa", "blad_token_limit"}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range keys {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: missing text %q", l, k)
			}
		}
	}
}

// A token also works where the network list is blocked (trusted proxy without XFF).
func TestMetricsTokenWorksWhenCIDRBlocked(t *testing.T) {
	t.Cleanup(func() { _ = SetTrustedProxy(nil) })
	s := testServer(t)
	loggedInCookie(t, s)
	if err := s.setMetricsAllow([]string{"10.0.0.0/24"}); err != nil {
		t.Fatal(err)
	}
	if err := SetTrustedProxy([]string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if w := sendToAddress(s.Handler(), "10.0.0.1:1", "GET", "/metrics", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", w.Code)
	}
}

// An account removed through the API takes its tokens with it; a password change does not touch them,
// and the success message says so explicitly.
func TestAccountRemovalRevokesTokensButPasswordChangeDoesNot(t *testing.T) {
	s := testServer(t)
	c := loggedInCookie(t, s) // admin
	h := s.Handler()
	other, err := s.st.AddUser(s.ctx, "inny", "haslo12345")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, _ := s.st.NewAPIToken(s.ctx, "inny-tok", "inny")
	adminToken, _, _ := s.st.NewAPIToken(s.ctx, "admin-tok", "admin")

	if w := send(h, c, "POST", "/api/password", `{"old":"haslo12345","new":"nowehaslo123"}`); w.Code != http.StatusOK {
		t.Fatalf("password change: %d %s", w.Code, w.Body)
	}
	if _, err := s.st.VerifyAPIToken(s.ctx, adminToken); err != nil {
		t.Errorf("a password change invalidated the token: %v", err)
	}
	if w := send(h, c, "DELETE", "/api/users/"+strconv.FormatInt(other.ID, 10), ""); w.Code != http.StatusOK {
		t.Fatalf("account removal: %d %s", w.Code, w.Body)
	}
	if _, err := s.st.VerifyAPIToken(s.ctx, otherToken); err == nil {
		t.Error("the token of the removed account still works")
	}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.PL} {
		if txt := i18n.T(lang, "haslo_zmienione"); !strings.Contains(txt, "API") {
			t.Errorf("%s: the message does not mention API tokens: %q", lang, txt)
		}
	}
}
