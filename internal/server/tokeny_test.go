package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// wyślijBezCiastka — żądanie bez sesji, jak od Prometheusa albo curl-a.
// Nagłówek Sec-Fetch-Site jest ustawiony, żeby warstwa CSRF nie zasłoniła
// tego, co sprawdzamy (autoryzację).
func wyślijBezCiastka(h http.Handler, metoda, ścieżka, ciało string, nagłówki ...string) *httptest.ResponseRecorder {
	return wyślijZAdresu(h, "192.0.2.10:4000", metoda, ścieżka, ciało, nagłówki...)
}

func wyślijZAdresu(h http.Handler, zdalny, metoda, ścieżka, ciało string, nagłówki ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(metoda, ścieżka, strings.NewReader(ciało))
	r.RemoteAddr = zdalny
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(nagłówki); i += 2 {
		r.Header.Set(nagłówki[i], nagłówki[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func bearer(token string) []string { return []string{"Authorization", "Bearer " + token} }

func nowyToken(t *testing.T, s *Server) string {
	t.Helper()
	token, _, err := s.st.NowyTokenAPI(s.ctx, "test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// ── zarządzanie tokenami (sesja) ─────────────────────────────────────────────

func TestTokenyAPIUtwórzListaOdwołaj(t *testing.T) {
	s, h, c := serwerOpisy(t)

	w := wyślij(h, c, "POST", "/api/tokens", `{"name":"prometheus"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	var utw struct {
		Token string `json:"token"`
		ID    int64  `json:"id"`
		Nazwa string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &utw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(utw.Token, "shyg_") || len(utw.Token) != 69 || utw.Nazwa != "prometheus" || utw.ID == 0 {
		t.Fatalf("odpowiedź: %+v", utw)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("token w odpowiedzi bez no-store: %q", cc)
	}

	// lista nie zdradza tokenu — ani w całości, ani jego skrótu
	w = wyślij(h, c, "GET", "/api/tokens", "")
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d", w.Code)
	}
	if strings.Contains(w.Body.String(), strings.TrimPrefix(utw.Token, "shyg_")) {
		t.Errorf("lista zawiera sekret: %s", w.Body)
	}
	var lista struct {
		Tokens []struct {
			ID       int64  `json:"id"`
			Nazwa    string `json:"name"`
			Hint     string `json:"hint"`
			Created  string `json:"created"`
			LastUsed string `json:"lastUsed"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lista); err != nil {
		t.Fatal(err)
	}
	if len(lista.Tokens) != 1 || lista.Tokens[0].Nazwa != "prometheus" || lista.Tokens[0].LastUsed != "" ||
		lista.Tokens[0].Created == "" || !strings.HasPrefix(utw.Token, lista.Tokens[0].Hint) {
		t.Errorf("lista: %+v", lista.Tokens)
	}

	// token działa, po odwołaniu przestaje
	if w := wyślijBezCiastka(h, "GET", "/api/status", "", bearer(utw.Token)...); w.Code != http.StatusOK {
		t.Fatalf("świeży token: kod %d", w.Code)
	}
	w = wyślij(h, c, "DELETE", "/api/tokens/"+itoa(utw.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE: kod %d: %s", w.Code, w.Body)
	}
	if w := wyślijBezCiastka(h, "GET", "/api/status", "", bearer(utw.Token)...); w.Code != http.StatusUnauthorized {
		t.Errorf("odwołany token: kod %d, chcę 401", w.Code)
	}
	if w := wyślij(h, c, "DELETE", "/api/tokens/"+itoa(utw.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("ponowne odwołanie: kod %d, chcę 404", w.Code)
	}
	_ = s
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }

func TestTokenyAPIWalidacjaNazwy(t *testing.T) {
	_, h, c := serwerOpisy(t)
	for _, ciało := range []string{`{"name":""}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("a", 65) + `"}`, `{"name":"a\nb"}`, `{}`, `nie json`} {
		w := wyślij(h, c, "POST", "/api/tokens", ciało, "Accept-Language", "pl")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: kod %d, chcę 400", ciało, w.Code)
		}
	}
	w := wyślij(h, c, "POST", "/api/tokens", `{"name":""}`, "Accept-Language", "pl")
	if !strings.Contains(w.Body.String(), i18n.T(i18n.PL, "blad_token_nazwa")[:20]) {
		t.Errorf("komunikat po polsku: %s", w.Body)
	}
}

func TestTokenyAPILimitSztuk(t *testing.T) {
	_, h, c := serwerOpisy(t)
	for i := 0; i < store.MaksTokenówAPI; i++ {
		if w := wyślij(h, c, "POST", "/api/tokens", `{"name":"t"}`); w.Code != http.StatusCreated {
			t.Fatalf("token %d: kod %d", i, w.Code)
		}
	}
	if w := wyślij(h, c, "POST", "/api/tokens", `{"name":"t"}`); w.Code != http.StatusConflict {
		t.Errorf("ponad limit: kod %d, chcę 409", w.Code)
	}
}

// ── bearer: tylko trzy trasy GET ─────────────────────────────────────────────

func TestBearerDziałaWyłącznieNaTrzechTrasach(t *testing.T) {
	s, h, _ := serwerOpisy(t)
	token := nowyToken(t, s)
	przed := string(s.plikConfigu(t))

	dozwolone := []string{"/api/status", "/api/history?key=Media%2FJellyfin", "/metrics"}
	for _, ścieżka := range dozwolone {
		if w := wyślijBezCiastka(h, "GET", ścieżka, "", bearer(token)...); w.Code != http.StatusOK {
			t.Errorf("GET %s z tokenem: kod %d, chcę 200", ścieżka, w.Code)
		}
		// i bez tokenu to samo żądanie nie przechodzi — 401, nie przekierowanie
		w := wyślijBezCiastka(h, "GET", ścieżka, "")
		if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" {
			t.Errorf("GET %s bez tokenu: kod %d, Location %q — chcę 401 bez przekierowania", ścieżka, w.Code, w.Header().Get("Location"))
		}
	}

	odrzucone := []struct{ metoda, ścieżka, ciało string }{
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
	for _, c := range odrzucone {
		w := wyślijBezCiastka(h, c.metoda, c.ścieżka, c.ciało, bearer(token)...)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s z tokenem: kod %d, chcę 401 (bearer nie ma tu prawa)", c.metoda, c.ścieżka, w.Code)
		}
	}
	// strumień zdarzeń: klient SSE zawsze wysyła Accept: text/event-stream
	if w := wyślijBezCiastka(h, "GET", "/events", "", append(bearer(token), "Accept", "text/event-stream")...); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /events z tokenem: kod %d, chcę 401", w.Code)
	}
	// strona główna: bearer nie otwiera panelu, tylko odsyła do logowania
	if w := wyślijBezCiastka(h, "GET", "/", "", bearer(token)...); w.Code != http.StatusSeeOther ||
		!strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Errorf("GET / z tokenem: kod %d, Location %q", w.Code, w.Header().Get("Location"))
	}

	if string(s.plikConfigu(t)) != przed {
		t.Error("plik configu zmieniony przez żądania z tokenem")
	}
	if l, _ := s.st.TokenyAPI(s.ctx); len(l) != 1 {
		t.Errorf("tokenów po próbach: %d, chcę 1", len(l))
	}
}

func TestBearerTylkoZNagłówkaIOnlyGET(t *testing.T) {
	s, h, _ := serwerOpisy(t)
	token := nowyToken(t, s)

	// token w query string albo ciasteczku nie jest tokenem
	if w := wyślijBezCiastka(h, "GET", "/api/status?token="+token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("token w query: kod %d", w.Code)
	}
	if w := wyślijBezCiastka(h, "GET", "/api/status", "", "Cookie", "shyg_session="+token); w.Code != http.StatusUnauthorized {
		t.Errorf("token jako ciasteczko sesji: kod %d", w.Code)
	}
	// inne schematy i śmieci
	for _, nagłówek := range []string{"Basic " + token, "Bearer", "Bearer ", token, "Bearer " + token + "x", "Bearer  " + token + " extra"} {
		if w := wyślijBezCiastka(h, "GET", "/api/status", "", "Authorization", nagłówek); w.Code != http.StatusUnauthorized {
			t.Errorf("%q: kod %d, chcę 401", nagłówek, w.Code)
		}
	}
	// schemat wg RFC 7235 nie rozróżnia wielkości liter
	if w := wyślijBezCiastka(h, "GET", "/api/status", "", "Authorization", "bearer "+token); w.Code != http.StatusOK {
		t.Errorf("bearer (małe litery): kod %d", w.Code)
	}
	// tylko GET: HEAD i POST na trasie tokenowej nie przechodzą
	for _, m := range []string{"POST", "PUT", "DELETE", "HEAD"} {
		if w := wyślijBezCiastka(h, m, "/api/status", "", bearer(token)...); w.Code == http.StatusOK {
			t.Errorf("%s /api/status z tokenem przeszło", m)
		}
	}
}

func TestSesjaDalejDziałaNaTrasachTokenowych(t *testing.T) {
	_, h, c := serwerOpisy(t)
	for _, ścieżka := range []string{"/api/status", "/metrics", "/api/history?key=Media%2FJellyfin"} {
		if w := wyślij(h, c, "GET", ścieżka, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s z sesją: kod %d", ścieżka, w.Code)
		}
	}
}

// Świeża instancja (bez kont) przekierowuje na /setup — ale scraper ma dostać
// 401, nie HTML kreatora.
func TestMetrykiNaŚwieżejInstancjiTo401NieRedirect(t *testing.T) {
	s := serwerTestowy(t) // brak użytkowników
	h := s.Handler()
	for _, hdr := range [][]string{nil, bearer("shyg_" + strings.Repeat("a", 64))} {
		w := wyślijBezCiastka(h, "GET", "/metrics", "", hdr...)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" {
			t.Errorf("kod %d, Location %q", w.Code, w.Header().Get("Location"))
		}
	}
	// zwykła strona dalej kieruje na /setup
	if w := wyślijBezCiastka(h, "GET", "/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup" {
		t.Errorf("GET /: kod %d, Location %q", w.Code, w.Header().Get("Location"))
	}
}

func TestOdpowiedź401MaWWWAuthenticate(t *testing.T) {
	s, h, _ := serwerOpisy(t)
	_ = s
	w := wyślijBezCiastka(h, "GET", "/metrics", "")
	if !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
	}
}

func TestOstatnieUżycieTokenuAktualizowaneRzadko(t *testing.T) {
	s, h, _ := serwerOpisy(t)
	token := nowyToken(t, s)
	pobierz := func() string {
		l, _ := s.st.TokenyAPI(s.ctx)
		return l[0].OstatnieUżycie.String()
	}
	if l, _ := s.st.TokenyAPI(s.ctx); !l[0].OstatnieUżycie.IsZero() {
		t.Fatal("nowy token ma last_used")
	}
	wyślijBezCiastka(h, "GET", "/api/status", "", bearer(token)...)
	pierwsze := pobierz()
	if strings.HasPrefix(pierwsze, "0001") {
		t.Fatal("last_used nie ustawione po użyciu")
	}
	for i := 0; i < 5; i++ {
		wyślijBezCiastka(h, "GET", "/api/status", "", bearer(token)...)
	}
	if pobierz() != pierwsze {
		t.Error("last_used zmieniane częściej niż raz na minutę")
	}
}

func TestZłeTokenyPodlegająLimitowi(t *testing.T) {
	s, h, _ := serwerOpisy(t)
	token := nowyToken(t, s)
	zły := "shyg_" + strings.Repeat("0", 64)
	limit := 0
	for i := 0; i < 500; i++ {
		w := wyślijZAdresu(h, "198.51.100.7:1000", "GET", "/metrics", "", bearer(zły)...)
		if w.Code == http.StatusTooManyRequests {
			limit = i
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("próba %d: kod %d", i, w.Code)
		}
	}
	if limit == 0 {
		t.Fatal("brak limitu prób z błędnym tokenem")
	}
	// ten sam adres, nawet z dobrym tokenem, czeka; inny adres jest nietknięty
	if w := wyślijZAdresu(h, "198.51.100.7:1000", "GET", "/metrics", "", bearer(token)...); w.Code != http.StatusTooManyRequests {
		t.Errorf("zablokowany adres z dobrym tokenem: kod %d", w.Code)
	}
	if w := wyślijZAdresu(h, "198.51.100.8:1000", "GET", "/metrics", "", bearer(token)...); w.Code != http.StatusOK {
		t.Errorf("inny adres: kod %d", w.Code)
	}
	// limit nie współdzieli licznika z logowaniem
	if !s.lim.wolno("198.51.100.7") {
		t.Error("błędne tokeny zużyły limit prób logowania")
	}
}

// ── nieuwierzytelnione /metrics dla wskazanych sieci ────────────────────────

func TestMetrykiZCIDR(t *testing.T) {
	t.Cleanup(func() { _ = UstawZaufaneProxy(nil) })

	cases := []struct {
		nazwa    string
		allow    []string
		proxy    []string
		zdalny   string
		nagłówki []string
		chcę     int
	}{
		{"domyślnie pusta lista — token wymagany", nil, nil, "192.168.0.50:1", nil, 401},
		{"adres z listy, bez tokenu", []string{"192.168.0.0/24"}, nil, "192.168.0.50:1", nil, 200},
		{"adres spoza listy", []string{"192.168.0.0/24"}, nil, "10.9.9.9:1", nil, 401},
		{"goły adres na liście", []string{"192.168.0.50"}, nil, "192.168.0.50:1", nil, 200},
		{"IPv6", []string{"fd00::/8"}, nil, "[fd00::5]:1", nil, 200},
		{"IPv4 jako ::ffff:", []string{"192.168.0.0/24"}, nil, "[::ffff:192.168.0.9]:1", nil, 200},
		{"XFF od niezaufanego peera — nie da się zaklasyfikować, odmowa",
			[]string{"192.168.0.0/24"}, nil, "192.168.0.50:1", []string{"X-Forwarded-For", "192.168.0.7"}, 401},
		{"X-Real-IP od niezaufanego peera — odmowa",
			[]string{"192.168.0.0/24"}, nil, "192.168.0.50:1", []string{"X-Real-IP", "192.168.0.7"}, 401},
		{"Forwarded od niezaufanego peera — odmowa",
			[]string{"192.168.0.0/24"}, nil, "192.168.0.50:1", []string{"Forwarded", "for=192.168.0.7"}, 401},
		{"zaufane proxy, klient z listy", []string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7"}, 200},
		{"zaufane proxy, klient spoza listy", []string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "8.8.8.8"}, 401},
		{"podszycie: obcy wpisał swój XFF z lewej, proxy dopisał prawdziwy adres z prawej",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7, 8.8.8.8"}, 401},
		{"fałszywy wpis z lewej nie szkodzi, gdy prawy jest z listy",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "8.8.8.8, 192.168.0.7"}, 200},
		{"łańcuch zaufanych proxy — pomijamy je od prawej",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.0/24"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7, 10.0.0.2"}, 200},
		{"śmieć w XFF — odmowa", []string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "unknown"}, 401},
		{"zaufane proxy bez XFF — adres proxy decyduje (i nie jest na liście)",
			[]string{"192.168.0.0/24"}, []string{"10.0.0.1"}, "10.0.0.1:1", nil, 401},
		{"peer zaufany nie znaczy zezwolony", nil, []string{"10.0.0.1"}, "10.0.0.1:1",
			[]string{"X-Forwarded-For", "192.168.0.7"}, 401},
	}
	for _, c := range cases {
		s := serwerTestowy(t)
		ciasteczkoZalogowanego(t, s) // konto istnieje, więc /metrics nie idzie na /setup
		if err := s.ustawMetrykiAllow(c.allow); err != nil {
			t.Fatalf("%s: %v", c.nazwa, err)
		}
		if err := UstawZaufaneProxy(c.proxy); err != nil {
			t.Fatal(err)
		}
		w := wyślijZAdresu(s.Handler(), c.zdalny, "GET", "/metrics", "", c.nagłówki...)
		if w.Code != c.chcę {
			t.Errorf("%s: kod %d, chcę %d", c.nazwa, w.Code, c.chcę)
		}
	}
}

// Lista sieci otwiera WYŁĄCZNIE /metrics. Reszta, także trasy akceptujące
// token, dalej wymaga sesji albo tokenu.
func TestCIDRNieOtwieraInnychTras(t *testing.T) {
	s := serwerTestowy(t)
	ciasteczkoZalogowanego(t, s)
	if err := s.ustawMetrykiAllow([]string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	if w := wyślijBezCiastka(h, "GET", "/metrics", ""); w.Code != http.StatusOK {
		t.Fatalf("/metrics: %d", w.Code)
	}
	for _, ścieżka := range []string{"/api/status", "/api/history?key=a", "/api/config", "/api/tokens"} {
		if w := wyślijBezCiastka(h, "GET", ścieżka, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s z adresu z listy: kod %d, chcę 401", ścieżka, w.Code)
		}
	}
	if w := wyślijBezCiastka(h, "POST", "/metrics", ""); w.Code == http.StatusOK {
		t.Error("POST /metrics przeszło")
	}
}

func TestMetrykiAllowOdrzucaZłyCIDR(t *testing.T) {
	s := serwerTestowy(t)
	if err := s.ustawMetrykiAllow([]string{"nie-cidr"}); err == nil {
		t.Error("zły CIDR przyjęty")
	}
	if err := s.ustawMetrykiAllow([]string{" 10.0.0.0/8 ", "", "192.168.1.5"}); err != nil {
		t.Errorf("spacje i goły adres mają przechodzić: %v", err)
	}
}

// adresIP (limiter logowania) używa teraz tej samej reguły: skrajnie prawy
// niezaufany wpis, a nie pierwszy z lewej, który klient wpisuje sam.
func TestAdresIPBierzePrawyNiezaufanyWpis(t *testing.T) {
	t.Cleanup(func() { _ = UstawZaufaneProxy(nil) })
	if err := UstawZaufaneProxy([]string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:5"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if got := adresIP(r); got != "203.0.113.9" {
		t.Errorf("adresIP = %q, chcę 203.0.113.9", got)
	}
	_ = os.Stdout
}

func TestTekstyTokenówMająObaJęzyki(t *testing.T) {
	klucze := []string{"tokeny_tytul", "tokeny_opis", "tokeny_nazwa", "tokeny_utworz", "tokeny_pokazany_raz",
		"tokeny_brak", "tokeny_odwolaj", "tokeny_potwierdz", "tokeny_uzyty", "tokeny_nieuzyty",
		"tokeny_utworzony", "tokeny_odwolany", "kopiuj", "skopiowano", "blad_token_nazwa", "blad_token_limit"}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Mapa(l)
		for _, k := range klucze {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: brak tekstu %q", l, k)
			}
		}
	}
}
