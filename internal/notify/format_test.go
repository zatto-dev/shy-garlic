package notify

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// przechwycone — jedno żądanie, które dotarło do atrapy webhooka.
type przechwycone struct {
	metoda, ścieżka, zapytanie string
	nagłówki                   http.Header
	ciało                      string
}

type atrapa struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []przechwycone
}

func nowaAtrapa(status int) *atrapa {
	a := &atrapa{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.got = append(a.got, przechwycone{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)})
		a.mu.Unlock()
		w.WriteHeader(status)
	}))
	return a
}

func (a *atrapa) żądania() []przechwycone {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]przechwycone(nil), a.got...)
}

func (a *atrapa) jedno(t *testing.T) przechwycone {
	t.Helper()
	g := a.żądania()
	if len(g) != 1 {
		t.Fatalf("żądań = %d, chcę 1", len(g))
	}
	return g[0]
}

func wyślijFormat(t *testing.T, f Format, adres string, z Zdarzenie) {
	t.Helper()
	p := Nowy(adres, 1)
	p.UstawFormat(f)
	p.Obserwuj(context.Background(), z)
	p.Poczekaj()
}

func zdarzeniePełne(stan string) Zdarzenie {
	z := zdarzenie(stan)
	z.Błąd = "connection refused"
	z.Poprzedni = "up"
	z.Kiedy = time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	return z
}

func TestFormatDomyślnyToDiscord(t *testing.T) {
	a := nowaAtrapa(204)
	defer a.srv.Close()
	p := Nowy(a.srv.URL, 1)
	if p.Format() != FormatDiscord {
		t.Fatalf("domyślny format = %q", p.Format())
	}
	p.Obserwuj(context.Background(), zdarzeniePełne("down"))
	p.Poczekaj()
	r := a.jedno(t)
	var m map[string]any
	if err := json.Unmarshal([]byte(r.ciało), &m); err != nil || m["embeds"] == nil {
		t.Fatalf("discord: %s", r.ciało)
	}
	if r.metoda != "POST" || r.nagłówki.Get("Content-Type") != "application/json" {
		t.Errorf("%s %s", r.metoda, r.nagłówki.Get("Content-Type"))
	}
}

func TestFormatSlack(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	wyślijFormat(t, FormatSlack, a.srv.URL+"/services/T0/B0/xyz", zdarzeniePełne("down"))
	r := a.jedno(t)
	var m map[string]string
	if err := json.Unmarshal([]byte(r.ciało), &m); err != nil {
		t.Fatalf("%v: %s", err, r.ciało)
	}
	if len(m) != 1 || !strings.Contains(m["text"], "Jellyfin is down") ||
		!strings.Contains(m["text"], "connection refused") || !strings.Contains(m["text"], "http://x:8096") {
		t.Errorf("slack: %v", m)
	}
	if r.ścieżka != "/services/T0/B0/xyz" || r.nagłówki.Get("Content-Type") != "application/json" {
		t.Errorf("%s %s", r.ścieżka, r.nagłówki.Get("Content-Type"))
	}
}

func TestFormatNtfy(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	wyślijFormat(t, FormatNtfy, a.srv.URL+"/moj-temat", zdarzeniePełne("down"))
	r := a.jedno(t)
	if r.metoda != "POST" || r.ścieżka != "/moj-temat" {
		t.Errorf("%s %s", r.metoda, r.ścieżka)
	}
	if ct := r.nagłówki.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, chcę text/plain", ct)
	}
	if got := r.nagłówki.Get("Title"); got != "Jellyfin is down" {
		t.Errorf("Title = %q", got)
	}
	if got := r.nagłówki.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q, chcę high dla down", got)
	}
	if got := r.nagłówki.Get("Tags"); got != "rotating_light" {
		t.Errorf("Tags = %q", got)
	}
	if got := r.nagłówki.Get("Click"); got != "http://x:8096" {
		t.Errorf("Click = %q", got)
	}
	// ciało to zwykły tekst, nie JSON
	if strings.HasPrefix(strings.TrimSpace(r.ciało), "{") || !strings.Contains(r.ciało, "connection refused") {
		t.Errorf("ciało: %q", r.ciało)
	}
}

func TestFormatNtfyPriorytetyITagiPerStan(t *testing.T) {
	cases := []struct{ stan, priorytet, tag string }{
		{"down", "high", "rotating_light"},
		{"degraded", "default", "warning"},
		{"up", "default", "white_check_mark"},
	}
	for _, c := range cases {
		a := nowaAtrapa(200)
		p := Nowy(a.srv.URL+"/t", 1)
		p.UstawFormat(FormatNtfy)
		// powrót wymaga wcześniejszego „down" — wstrzykujemy stan wprost
		if c.stan == "up" {
			p.Obserwuj(context.Background(), zdarzenie("down"))
			p.Poczekaj()
		}
		p.Obserwuj(context.Background(), zdarzenie(c.stan))
		p.Poczekaj()
		g := a.żądania()
		r := g[len(g)-1]
		if r.nagłówki.Get("Priority") != c.priorytet || r.nagłówki.Get("Tags") != c.tag {
			t.Errorf("%s: Priority=%q Tags=%q", c.stan, r.nagłówki.Get("Priority"), r.nagłówki.Get("Tags"))
		}
		a.srv.Close()
	}
}

// Nagłówek HTTP nie unosi gołego UTF-8 — nazwa usługi z ogonkami idzie jako
// słowo zakodowane wg RFC 2047, które ntfy odczytuje.
func TestFormatNtfyTytułZOgonkami(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	z := zdarzeniePełne("down")
	z.Nazwa = "Zdjęcia\r\nX-Wstrzyknięty: tak"
	wyślijFormat(t, FormatNtfy, a.srv.URL+"/t", z)
	r := a.jedno(t)
	if r.nagłówki.Get("X-Wstrzyknięty") != "" || r.nagłówki.Get("X-Wstrzykniety") != "" {
		t.Fatal("wstrzyknięcie nagłówka przez nazwę usługi")
	}
	surowy := r.nagłówki.Get("Title")
	dek, err := new(mime.WordDecoder).DecodeHeader(surowy)
	if err != nil || !strings.Contains(dek, "Zdjęcia") || !strings.Contains(dek, "is down") {
		t.Errorf("Title %q → %q (%v)", surowy, dek, err)
	}
	for _, b := range []byte(surowy) {
		if b > 0x7e || b < 0x20 {
			t.Errorf("w nagłówku bajt spoza ASCII: %q", surowy)
			break
		}
	}
}

func TestFormatNtfyDaneLogowaniaZAdresuIdąJakoBasicAuth(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	u, _ := url.Parse(a.srv.URL)
	u.User = url.UserPassword("anna", "sekret")
	u.Path = "/t"
	wyślijFormat(t, FormatNtfy, u.String(), zdarzeniePełne("down"))
	r := a.jedno(t)
	if user, pass, ok := (&http.Request{Header: r.nagłówki}).BasicAuth(); !ok || user != "anna" || pass != "sekret" {
		t.Errorf("Authorization: %q", r.nagłówki.Get("Authorization"))
	}
}

func TestFormatGotify(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	wyślijFormat(t, FormatGotify, a.srv.URL+"/message?token=APPTOKEN", zdarzeniePełne("down"))
	r := a.jedno(t)
	if r.metoda != "POST" || r.ścieżka != "/message" || r.zapytanie != "token=APPTOKEN" {
		t.Errorf("%s %s?%s", r.metoda, r.ścieżka, r.zapytanie)
	}
	var m struct {
		Title    string         `json:"title"`
		Message  string         `json:"message"`
		Priority int            `json:"priority"`
		Extras   map[string]any `json:"extras"`
	}
	if err := json.Unmarshal([]byte(r.ciało), &m); err != nil {
		t.Fatalf("%v: %s", err, r.ciało)
	}
	if m.Title != "Jellyfin is down" || !strings.Contains(m.Message, "connection refused") || m.Priority != 8 {
		t.Errorf("gotify: %+v", m)
	}
	if m.Extras["client::notification"] == nil {
		t.Errorf("brak extras click: %s", r.ciało)
	}
}

func TestFormatTelegram(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	wyślijFormat(t, FormatTelegram, a.srv.URL+"/bot123:ABC-def/sendMessage?chat_id=-100555", zdarzeniePełne("down"))
	r := a.jedno(t)
	if r.metoda != "POST" || r.ścieżka != "/bot123:ABC-def/sendMessage" || r.zapytanie != "chat_id=-100555" {
		t.Errorf("%s %s?%s", r.metoda, r.ścieżka, r.zapytanie)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.ciało), &m); err != nil {
		t.Fatalf("%v: %s", err, r.ciało)
	}
	tekst, _ := m["text"].(string)
	if !strings.Contains(tekst, "Jellyfin is down") || !strings.Contains(tekst, "connection refused") {
		t.Errorf("telegram: %v", m)
	}
	if m["disable_web_page_preview"] != true {
		t.Errorf("podgląd linku ma być wyłączony: %v", m)
	}
	if _, ma := m["parse_mode"]; ma {
		t.Error("bez parse_mode — nazwy usług z _ i * nie mogą psuć Markdowna")
	}
}

// Schemat generic jest kontraktem: zmiana nazw pól łamie cudze integracje.
func TestFormatGenericStabilnySchemat(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	wyślijFormat(t, FormatGeneric, a.srv.URL+"/hook", zdarzeniePełne("down"))
	r := a.jedno(t)
	var m map[string]any
	if err := json.Unmarshal([]byte(r.ciało), &m); err != nil {
		t.Fatal(err)
	}
	chcę := map[string]any{
		"event": "down", "service": "Jellyfin", "group": "Media", "state": "down",
		"previous": "up", "reason": "connection refused",
		"at": "2026-10-16T12:30:00Z", "url": "http://x:8096",
	}
	if len(m) != len(chcę) {
		t.Errorf("pól = %d (%v), chcę dokładnie %d", len(m), m, len(chcę))
	}
	for k, v := range chcę {
		if m[k] != v {
			t.Errorf("%s = %v, chcę %v", k, m[k], v)
		}
	}
	if r.nagłówki.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", r.nagłówki.Get("Content-Type"))
	}
}

func TestFormatGenericZdarzeniaRecoveredIDegraded(t *testing.T) {
	a := nowaAtrapa(200)
	defer a.srv.Close()
	p := Nowy(a.srv.URL+"/h", 1)
	p.UstawFormat(FormatGeneric)
	p.Obserwuj(context.Background(), zdarzenie("degraded"))
	p.Poczekaj()
	p.Obserwuj(context.Background(), zdarzenie("up"))
	p.Poczekaj()
	g := a.żądania()
	if len(g) != 2 {
		t.Fatalf("żądań %d", len(g))
	}
	var d, u map[string]string
	_ = json.Unmarshal([]byte(g[0].ciało), &d)
	_ = json.Unmarshal([]byte(g[1].ciało), &u)
	if d["event"] != "degraded" || d["previous"] != "up" || u["event"] != "recovered" || u["previous"] != "degraded" || u["state"] != "up" {
		t.Errorf("degraded=%v recovered=%v", d, u)
	}
}

// Przycisk „wyślij próbę" ma działać w każdym formacie, a próba jest
// rozpoznawalna jako próba (event=test w generic, osobny tytuł w reszcie).
func TestTestWysyłaPróbęWKażdymFormacie(t *testing.T) {
	adresy := map[Format]string{
		FormatDiscord:  "/api/webhooks/1/abc",
		FormatSlack:    "/services/a/b/c",
		FormatNtfy:     "/temat",
		FormatGotify:   "/message?token=T",
		FormatTelegram: "/bot1:A/sendMessage?chat_id=7",
		FormatGeneric:  "/h",
	}
	for _, f := range Formaty {
		a := nowaAtrapa(200)
		p := Nowy(a.srv.URL+adresy[f], 2)
		p.UstawFormat(f)
		if err := p.Test(context.Background()); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		r := a.jedno(t)
		if !strings.Contains(strings.ToLower(r.ciało+r.nagłówki.Get("Title")), "test") {
			t.Errorf("%s: wiadomość próbna nie wygląda na próbną: %q / %q", f, r.ciało, r.nagłówki.Get("Title"))
		}
		if f == FormatGeneric {
			var m map[string]any
			_ = json.Unmarshal([]byte(r.ciało), &m)
			if m["event"] != "test" {
				t.Errorf("generic: event = %v, chcę test", m["event"])
			}
		}
		a.srv.Close()
	}
}

func TestPoprawnyFormatIListaFormatów(t *testing.T) {
	for _, f := range []string{"discord", "slack", "ntfy", "gotify", "telegram", "generic"} {
		if !PoprawnyFormat(f) {
			t.Errorf("%s odrzucony", f)
		}
	}
	for _, f := range []string{"", "Discord", "teams", "json", "ntfy "} {
		if PoprawnyFormat(f) {
			t.Errorf("%q przyjęty", f)
		}
	}
	p := Nowy("", 1)
	p.UstawFormat("śmieć")
	if p.Format() != FormatDiscord {
		t.Errorf("nieznany format ma wracać do discord, jest %q", p.Format())
	}
}

func TestSprawdźAdresWgFormatu(t *testing.T) {
	ok := map[Format][]string{
		FormatTelegram: {"https://api.telegram.org/bot123:ABC/sendMessage?chat_id=-1001"},
		FormatGotify:   {"https://gotify.home.arpa/message?token=Aa1", "https://home.arpa/gotify/message?token=x"},
		FormatNtfy:     {"https://ntfy.sh/temat", "https://u:p@ntfy.home.arpa/temat"},
		FormatSlack:    {"https://hooks.slack.com/services/T/B/x"},
		FormatDiscord:  {"https://discord.com/api/webhooks/1/x"},
		FormatGeneric:  {"https://example.test/anything?x=1"},
	}
	for f, lista := range ok {
		for _, a := range lista {
			if err := SprawdźAdres(f, a); err != nil {
				t.Errorf("%s %s: %v", f, a, err)
			}
		}
	}
	zły := map[Format][]string{
		FormatTelegram: {"https://api.telegram.org/bot123:ABC/sendMessage", "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=", "https://api.telegram.org/bot123/getMe?chat_id=1"},
		FormatGotify:   {"https://g.home.arpa/message", "https://g.home.arpa/message?token=", "https://g.home.arpa/other?token=x"},
	}
	for f, lista := range zły {
		for _, a := range lista {
			if err := SprawdźAdres(f, a); err == nil {
				t.Errorf("%s %s przyjęty", f, a)
			}
		}
	}
	if err := SprawdźAdres(FormatTelegram, "https://x/bot1/sendMessage"); err != ErrAdresTelegram {
		t.Errorf("telegram: %v", err)
	}
	if err := SprawdźAdres(FormatGotify, "https://x/message"); err != ErrAdresGotify {
		t.Errorf("gotify: %v", err)
	}
}

// Błąd z http.Client niesie cały adres, a w adresie siedzi sekret (token bota,
// token Gotify, ścieżka webhooka). Nie może on trafić ani do odpowiedzi API, ani
// do logów.
func TestBłądWysyłkiNieZawieraAdresuZSekretem(t *testing.T) {
	a := nowaAtrapa(200)
	adres := a.srv.URL + "/bot123:SEKRETNYTOKEN/sendMessage?chat_id=424242"
	a.srv.Close() // połączenie odmówione

	p := Nowy(adres, 1)
	p.UstawFormat(FormatTelegram)
	err := p.Test(context.Background())
	if err == nil {
		t.Fatal("chcę błędu")
	}
	for _, sekret := range []string{"SEKRETNYTOKEN", "424242", "/bot123"} {
		if strings.Contains(err.Error(), sekret) {
			t.Errorf("błąd zdradza sekret %q: %v", sekret, err)
		}
	}

	// to samo przy złym statusie odpowiedzi
	b := nowaAtrapa(401)
	defer b.srv.Close()
	p2 := Nowy(b.srv.URL+"/message?token=TAJNY", 1)
	p2.UstawFormat(FormatGotify)
	if err := p2.Test(context.Background()); err == nil || strings.Contains(err.Error(), "TAJNY") {
		t.Errorf("status 401: %v", err)
	}
}
