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

// captured — a single request that reached the webhook stub.
type captured struct {
	method, path, query string
	headers             http.Header
	body                string
}

type stubServer struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []captured
}

func newStubServer(status int) *stubServer {
	a := &stubServer{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.got = append(a.got, captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)})
		a.mu.Unlock()
		w.WriteHeader(status)
	}))
	return a
}

func (a *stubServer) requests() []captured {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]captured(nil), a.got...)
}

func (a *stubServer) single(t *testing.T) captured {
	t.Helper()
	g := a.requests()
	if len(g) != 1 {
		t.Fatalf("żądań = %d, chcę 1", len(g))
	}
	return g[0]
}

func sendFormat(t *testing.T, f Format, address string, z Event) {
	t.Helper()
	p := New(address, 1)
	p.SetFormat(f)
	p.Observe(context.Background(), z)
	p.Wait()
}

func fullEvent(state string) Event {
	z := makeEvent(state)
	z.Err = "connection refused"
	z.Previous = "up"
	z.At = time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	return z
}

func TestDefaultFormatIsDiscord(t *testing.T) {
	a := newStubServer(204)
	defer a.srv.Close()
	p := New(a.srv.URL, 1)
	if p.Format() != FormatDiscord {
		t.Fatalf("domyślny format = %q", p.Format())
	}
	p.Observe(context.Background(), fullEvent("down"))
	p.Wait()
	r := a.single(t)
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil || m["embeds"] == nil {
		t.Fatalf("discord: %s", r.body)
	}
	if r.method != "POST" || r.headers.Get("Content-Type") != "application/json" {
		t.Errorf("%s %s", r.method, r.headers.Get("Content-Type"))
	}
}

func TestFormatSlack(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	sendFormat(t, FormatSlack, a.srv.URL+"/services/T0/B0/xyz", fullEvent("down"))
	r := a.single(t)
	var m map[string]string
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	if len(m) != 1 || !strings.Contains(m["text"], "Jellyfin is down") ||
		!strings.Contains(m["text"], "connection refused") || !strings.Contains(m["text"], "http://x:8096") {
		t.Errorf("slack: %v", m)
	}
	if r.path != "/services/T0/B0/xyz" || r.headers.Get("Content-Type") != "application/json" {
		t.Errorf("%s %s", r.path, r.headers.Get("Content-Type"))
	}
}

func TestFormatNtfy(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	sendFormat(t, FormatNtfy, a.srv.URL+"/moj-temat", fullEvent("down"))
	r := a.single(t)
	if r.method != "POST" || r.path != "/moj-temat" {
		t.Errorf("%s %s", r.method, r.path)
	}
	if ct := r.headers.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, chcę text/plain", ct)
	}
	if got := r.headers.Get("Title"); got != "Jellyfin is down" {
		t.Errorf("Title = %q", got)
	}
	if got := r.headers.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q, chcę high dla down", got)
	}
	if got := r.headers.Get("Tags"); got != "rotating_light" {
		t.Errorf("Tags = %q", got)
	}
	if got := r.headers.Get("Click"); got != "http://x:8096" {
		t.Errorf("Click = %q", got)
	}
	// the body is plain text, not JSON
	if strings.HasPrefix(strings.TrimSpace(r.body), "{") || !strings.Contains(r.body, "connection refused") {
		t.Errorf("ciało: %q", r.body)
	}
}

func TestFormatNtfyPrioritiesAndTagsPerState(t *testing.T) {
	cases := []struct{ state, priority, tag string }{
		{"down", "high", "rotating_light"},
		{"degraded", "default", "warning"},
		{"up", "default", "white_check_mark"},
	}
	for _, c := range cases {
		a := newStubServer(200)
		p := New(a.srv.URL+"/t", 1)
		p.SetFormat(FormatNtfy)
		// a recovery requires an earlier "down" — we inject the state directly
		if c.state == "up" {
			p.Observe(context.Background(), makeEvent("down"))
			p.Wait()
		}
		p.Observe(context.Background(), makeEvent(c.state))
		p.Wait()
		g := a.requests()
		r := g[len(g)-1]
		if r.headers.Get("Priority") != c.priority || r.headers.Get("Tags") != c.tag {
			t.Errorf("%s: Priority=%q Tags=%q", c.state, r.headers.Get("Priority"), r.headers.Get("Tags"))
		}
		a.srv.Close()
	}
}

// An HTTP header does not carry bare UTF-8 — a service name with diacritics goes as a
// word encoded per RFC 2047, which ntfy decodes.
func TestFormatNtfyTitleWithDiacritics(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	z := fullEvent("down")
	z.Name = "Zdjęcia\r\nX-Wstrzyknięty: tak"
	sendFormat(t, FormatNtfy, a.srv.URL+"/t", z)
	r := a.single(t)
	if r.headers.Get("X-Wstrzyknięty") != "" || r.headers.Get("X-Wstrzykniety") != "" {
		t.Fatal("wstrzyknięcie nagłówka przez nazwę usługi")
	}
	raw := r.headers.Get("Title")
	decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil || !strings.Contains(decoded, "Zdjęcia") || !strings.Contains(decoded, "is down") {
		t.Errorf("Title %q → %q (%v)", raw, decoded, err)
	}
	for _, b := range []byte(raw) {
		if b > 0x7e || b < 0x20 {
			t.Errorf("w nagłówku bajt spoza ASCII: %q", raw)
			break
		}
	}
}

func TestFormatNtfyCredentialsFromAddressGoAsBasicAuth(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	u, _ := url.Parse(a.srv.URL)
	u.User = url.UserPassword("anna", "sekret")
	u.Path = "/t"
	sendFormat(t, FormatNtfy, u.String(), fullEvent("down"))
	r := a.single(t)
	if user, pass, ok := (&http.Request{Header: r.headers}).BasicAuth(); !ok || user != "anna" || pass != "sekret" {
		t.Errorf("Authorization: %q", r.headers.Get("Authorization"))
	}
}

func TestFormatGotify(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	sendFormat(t, FormatGotify, a.srv.URL+"/message?token=APPTOKEN", fullEvent("down"))
	r := a.single(t)
	if r.method != "POST" || r.path != "/message" || r.query != "token=APPTOKEN" {
		t.Errorf("%s %s?%s", r.method, r.path, r.query)
	}
	var m struct {
		Title    string         `json:"title"`
		Message  string         `json:"message"`
		Priority int            `json:"priority"`
		Extras   map[string]any `json:"extras"`
	}
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	if m.Title != "Jellyfin is down" || !strings.Contains(m.Message, "connection refused") || m.Priority != 8 {
		t.Errorf("gotify: %+v", m)
	}
	if m.Extras["client::notification"] == nil {
		t.Errorf("brak extras click: %s", r.body)
	}
}

func TestFormatTelegram(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	sendFormat(t, FormatTelegram, a.srv.URL+"/bot123:ABC-def/sendMessage?chat_id=-100555", fullEvent("down"))
	r := a.single(t)
	if r.method != "POST" || r.path != "/bot123:ABC-def/sendMessage" || r.query != "chat_id=-100555" {
		t.Errorf("%s %s?%s", r.method, r.path, r.query)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	text, _ := m["text"].(string)
	if !strings.Contains(text, "Jellyfin is down") || !strings.Contains(text, "connection refused") {
		t.Errorf("telegram: %v", m)
	}
	if m["disable_web_page_preview"] != true {
		t.Errorf("podgląd linku ma być wyłączony: %v", m)
	}
	if _, has := m["parse_mode"]; has {
		t.Error("bez parse_mode — nazwy usług z _ i * nie mogą psuć Markdowna")
	}
}

// The generic schema is a contract: renaming fields breaks others' integrations.
func TestFormatGenericStableSchema(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	sendFormat(t, FormatGeneric, a.srv.URL+"/hook", fullEvent("down"))
	r := a.single(t)
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"event": "down", "service": "Jellyfin", "group": "Media", "state": "down",
		"previous": "up", "reason": "connection refused",
		"at": "2026-10-16T12:30:00Z", "url": "http://x:8096",
	}
	if len(m) != len(want) {
		t.Errorf("pól = %d (%v), chcę dokładnie %d", len(m), m, len(want))
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, chcę %v", k, m[k], v)
		}
	}
	if r.headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", r.headers.Get("Content-Type"))
	}
}

func TestFormatGenericRecoveredAndDegradedEvents(t *testing.T) {
	a := newStubServer(200)
	defer a.srv.Close()
	p := New(a.srv.URL+"/h", 1)
	p.SetFormat(FormatGeneric)
	p.Observe(context.Background(), makeEvent("degraded"))
	p.Wait()
	p.Observe(context.Background(), makeEvent("up"))
	p.Wait()
	g := a.requests()
	if len(g) != 2 {
		t.Fatalf("żądań %d", len(g))
	}
	var d, u map[string]string
	_ = json.Unmarshal([]byte(g[0].body), &d)
	_ = json.Unmarshal([]byte(g[1].body), &u)
	if d["event"] != "degraded" || d["previous"] != "up" || u["event"] != "recovered" || u["previous"] != "degraded" || u["state"] != "up" {
		t.Errorf("degraded=%v recovered=%v", d, u)
	}
}

// The "send test" button must work in every format, and the test is
// recognizable as a test (event=test in generic, a separate title in the rest).
func TestTestSendsProbeInEveryFormat(t *testing.T) {
	addresses := map[Format]string{
		FormatDiscord:  "/api/webhooks/1/abc",
		FormatSlack:    "/services/a/b/c",
		FormatNtfy:     "/temat",
		FormatGotify:   "/message?token=T",
		FormatTelegram: "/bot1:A/sendMessage?chat_id=7",
		FormatGeneric:  "/h",
	}
	for _, f := range Formats {
		a := newStubServer(200)
		p := New(a.srv.URL+addresses[f], 2)
		p.SetFormat(f)
		if err := p.Test(context.Background()); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		r := a.single(t)
		if !strings.Contains(strings.ToLower(r.body+r.headers.Get("Title")), "test") {
			t.Errorf("%s: wiadomość próbna nie wygląda na próbną: %q / %q", f, r.body, r.headers.Get("Title"))
		}
		if f == FormatGeneric {
			var m map[string]any
			_ = json.Unmarshal([]byte(r.body), &m)
			if m["event"] != "test" {
				t.Errorf("generic: event = %v, chcę test", m["event"])
			}
		}
		a.srv.Close()
	}
}

func TestValidFormatAndFormatList(t *testing.T) {
	for _, f := range []string{"discord", "slack", "ntfy", "gotify", "telegram", "generic"} {
		if !ValidFormat(f) {
			t.Errorf("%s odrzucony", f)
		}
	}
	for _, f := range []string{"", "Discord", "teams", "json", "ntfy "} {
		if ValidFormat(f) {
			t.Errorf("%q przyjęty", f)
		}
	}
	p := New("", 1)
	p.SetFormat("śmieć")
	if p.Format() != FormatDiscord {
		t.Errorf("nieznany format ma wracać do discord, jest %q", p.Format())
	}
}

func TestValidateAddressByFormat(t *testing.T) {
	ok := map[Format][]string{
		FormatTelegram: {"https://api.telegram.org/bot123:ABC/sendMessage?chat_id=-1001"},
		FormatGotify:   {"https://gotify.home.arpa/message?token=Aa1", "https://home.arpa/gotify/message?token=x"},
		FormatNtfy:     {"https://ntfy.sh/temat", "https://u:p@ntfy.home.arpa/temat"},
		FormatSlack:    {"https://hooks.slack.com/services/T/B/x"},
		FormatDiscord:  {"https://discord.com/api/webhooks/1/x"},
		FormatGeneric:  {"https://example.test/anything?x=1"},
	}
	for f, list := range ok {
		for _, a := range list {
			if err := ValidateAddress(f, a); err != nil {
				t.Errorf("%s %s: %v", f, a, err)
			}
		}
	}
	bad := map[Format][]string{
		FormatTelegram: {"https://api.telegram.org/bot123:ABC/sendMessage", "https://api.telegram.org/bot123:ABC/sendMessage?chat_id=", "https://api.telegram.org/bot123/getMe?chat_id=1"},
		FormatGotify:   {"https://g.home.arpa/message", "https://g.home.arpa/message?token=", "https://g.home.arpa/other?token=x"},
	}
	for f, list := range bad {
		for _, a := range list {
			if err := ValidateAddress(f, a); err == nil {
				t.Errorf("%s %s przyjęty", f, a)
			}
		}
	}
	if err := ValidateAddress(FormatTelegram, "https://x/bot1/sendMessage"); err != ErrTelegramAddress {
		t.Errorf("telegram: %v", err)
	}
	if err := ValidateAddress(FormatGotify, "https://x/message"); err != ErrGotifyAddress {
		t.Errorf("gotify: %v", err)
	}
}

// An error from http.Client carries the whole address, and the address holds a secret (bot token,
// Gotify token, webhook path). It must not reach either the API response or
// the logs.
func TestSendErrorDoesNotContainSecretAddress(t *testing.T) {
	a := newStubServer(200)
	address := a.srv.URL + "/bot123:SEKRETNYTOKEN/sendMessage?chat_id=424242"
	a.srv.Close() // connection refused

	p := New(address, 1)
	p.SetFormat(FormatTelegram)
	err := p.Test(context.Background())
	if err == nil {
		t.Fatal("chcę błędu")
	}
	for _, secret := range []string{"SEKRETNYTOKEN", "424242", "/bot123"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("błąd zdradza sekret %q: %v", secret, err)
		}
	}

	// the same for a bad response status
	b := newStubServer(401)
	defer b.srv.Close()
	p2 := New(b.srv.URL+"/message?token=TAJNY", 1)
	p2.SetFormat(FormatGotify)
	if err := p2.Test(context.Background()); err == nil || strings.Contains(err.Error(), "TAJNY") {
		t.Errorf("status 401: %v", err)
	}
}
