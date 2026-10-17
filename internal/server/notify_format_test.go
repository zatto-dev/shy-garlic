package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/notify"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

func putNotify(s *Server, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify", strings.NewReader(body)))
	return rr
}

func getNotify(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func message(rr *httptest.ResponseRecorder) string {
	var o struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &o)
	return o.Error
}

func TestNotifyGetShowsFormat(t *testing.T) {
	s := testServer(t)
	m := getNotify(t, s)
	if m["format"] != "discord" || m["formatSource"] != "default" {
		t.Errorf("default: %v", m)
	}
	list, _ := m["formats"].([]any)
	if len(list) != 6 || list[0] != "discord" || list[5] != "generic" {
		t.Errorf("formats: %v", list)
	}
}

func TestNotifyPutSavesFormatToDB(t *testing.T) {
	s := testServer(t)
	rr := putNotify(s, `{"format":"ntfy"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "ntfy" {
		t.Errorf("in the DB: %q", v)
	}
	if s.notifier.Format() != notify.FormatNtfy {
		t.Errorf("format w powiadamiaczu: %q", s.notifier.Format())
	}
	if m := getNotify(t, s); m["format"] != "ntfy" || m["formatSource"] != "database" {
		t.Errorf("GET: %v", m)
	}
	// returning to the default deletes the row
	if rr := putNotify(s, `{"format":"discord"}`); rr.Code != http.StatusOK {
		t.Fatalf("code %d", rr.Code)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("discord is the default and should not be in the DB: %q", v)
	}
}

func TestNotifyPutRejectsUnknownFormat(t *testing.T) {
	s := testServer(t)
	for _, f := range []string{"teams", "", "Discord", "ntfy "} {
		rr := putNotify(s, `{"format":"`+f+`"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%q: code %d, want 400", f, rr.Code)
		}
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("something was left in the DB: %q", v)
	}
}

// The same rule as with SHYG_WEBHOOK: what came from the environment belongs to
// the deploy and clicking in the panel does not undo it.
func TestNotifyPutFormatFromEnvIs409(t *testing.T) {
	s := testServer(t)
	s.formatFromEnv = true
	s.notifier.SetFormat(notify.FormatSlack)
	rr := putNotify(s, `{"format":"ntfy"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("code %d, want 409", rr.Code)
	}
	if s.notifier.Format() != notify.FormatSlack {
		t.Error("format zmieniony mimo 409")
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("DB touched: %q", v)
	}
	if m := getNotify(t, s); m["formatSource"] != "env" {
		t.Errorf("source: %v", m["formatSource"])
	}
}

// An address from a flag does not block the format: they are two independent
// settings, each with its own source.
func TestNotifyPutFormatWorksWithEnvAddress(t *testing.T) {
	s := testServer(t)
	s.webhookFromEnv = true
	s.notifier.SetWebhook("https://ntfy.example/temat")
	if rr := putNotify(s, `{"format":"ntfy"}`); rr.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
}

func TestNotifyPutGuardsAddressShapeByFormat(t *testing.T) {
	s := testServer(t)
	// together: format + bad address → 400, nothing saved
	rr := putNotify(s, `{"format":"telegram","webhook":"https://api.telegram.org/bot1:A/sendMessage"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(message(rr), "chat_id") {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.webhook"); v != "" {
		t.Errorf("address saved despite rejection: %q", v)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("format zapisany mimo odrzucenia: %q", v)
	}
	// good
	rr = putNotify(s, `{"format":"telegram","webhook":"https://api.telegram.org/bot1:A/sendMessage?chat_id=42"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rr.Code, rr.Body)
	}
	// a new address alone is checked against the SAVED format
	if rr := putNotify(s, `{"webhook":"https://api.telegram.org/bot1:A/sendMessage"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("address without chat_id with the telegram format: code %d", rr.Code)
	}
	// a new format alone is checked against the SAVED address
	if rr := putNotify(s, `{"format":"gotify"}`); rr.Code != http.StatusBadRequest || !strings.Contains(message(rr), "token") {
		t.Errorf("gotify on a telegram address: code %d: %s", rr.Code, rr.Body)
	}
	if s.notifier.Format() != notify.FormatTelegram {
		t.Errorf("format after rejected changes: %q", s.notifier.Format())
	}
	// message in Polish
	rr = httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify", strings.NewReader(`{"format":"gotify"}`))
	req.Header.Set("Accept-Language", "pl")
	s.handleNotifyPut(rr, req)
	if message(rr) != i18n.T(i18n.PL, "webhook_address_gotify") {
		t.Errorf("PL message: %q", message(rr))
	}
}

func TestNotifyPutAllFormatsRequireHTTPS(t *testing.T) {
	for _, f := range notify.Formats {
		s := testServer(t)
		rr := putNotify(s, `{"format":"`+string(f)+`","webhook":"http://lan.example/message?token=x&chat_id=1"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: http passed (code %d)", f, rr.Code)
		}
	}
}

// Secrets sit in different parts of the address: the bot token and ntfy topic in the
// path, the Gotify token and chat_id in the query. The mask keeps the scheme and host.
func TestMaskHidesSecretsInEveryFormat(t *testing.T) {
	cases := []struct {
		format, address string
		secrets         []string
	}{
		{"telegram", "https://api.telegram.org/bot123456:AAH-sekretBota/sendMessage?chat_id=-1009988", []string{"123456", "sekretBota", "1009988", "sendMessage"}},
		{"gotify", "https://gotify.home.arpa/message?token=TajnyTokenGotify", []string{"TajnyTokenGotify", "token"}},
		{"ntfy", "https://anna:haslo@ntfy.home.arpa/temat-tajny", []string{"anna", "haslo", "temat-tajny"}},
		{"slack", "https://hooks.slack.com/services/T000/B000/sekretSlacka", []string{"T000", "sekretSlacka"}},
		{"generic", "https://hook.home.arpa/in/sekretGeneric?k=v", []string{"sekretGeneric", "k=v"}},
	}
	for _, c := range cases {
		s := testServer(t)
		if rr := putNotify(s, `{"format":"`+c.format+`","webhook":"`+c.address+`"}`); rr.Code != http.StatusOK {
			t.Fatalf("%s: code %d: %s", c.format, rr.Code, rr.Body)
		}
		rr := httptest.NewRecorder()
		s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
		for _, secret := range c.secrets {
			if strings.Contains(rr.Body.String(), secret) {
				t.Errorf("%s: the GET response leaks %q: %s", c.format, secret, rr.Body)
			}
		}
		// the response to the PUT itself too
		rr = putNotify(s, `{"format":"`+c.format+`"}`)
		for _, secret := range c.secrets {
			if strings.Contains(rr.Body.String(), secret) {
				t.Errorf("%s: the PUT response leaks %q: %s", c.format, secret, rr.Body)
			}
		}
	}
}

// The "send a trial" button in every format, on the real handler.
func TestTestButtonWorksInEveryFormat(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.URL.RequestURI()+" "+r.Header.Get("Title")+" "+string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	paths := map[notify.Format]string{
		notify.FormatDiscord: "/api/webhooks/1/a", notify.FormatSlack: "/services/a/b/c",
		notify.FormatNtfy: "/temat", notify.FormatGotify: "/message?token=T",
		notify.FormatTelegram: "/bot1:A/sendMessage?chat_id=7", notify.FormatGeneric: "/h",
	}
	for _, f := range notify.Formats {
		s := testServer(t)
		s.notifier.SetWebhook(srv.URL + paths[f]) // http: bypasses the https rule from PUT, this is not a test of that rule
		s.notifier.SetFormat(f)
		rr := httptest.NewRecorder()
		s.handleNotifyTest(rr, httptest.NewRequest("POST", "/api/notify/test", nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s: code %d: %s", f, rr.Code, rr.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(notify.Formats) {
		t.Fatalf("requests %d, want %d: %v", len(got), len(notify.Formats), got)
	}
	for i, g := range got {
		if !strings.Contains(strings.ToLower(g), "test") {
			t.Errorf("%s: %s", notify.Formats[i], g)
		}
	}
}

func TestTestButtonDoesNotLeakAddressOnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	address := srv.URL + "/bot123:TAJNYTOKEN/sendMessage?chat_id=555"
	srv.Close()

	s := testServer(t)
	s.notifier.SetWebhook(address)
	s.notifier.SetFormat(notify.FormatTelegram)
	rr := httptest.NewRecorder()
	s.handleNotifyTest(rr, httptest.NewRequest("POST", "/api/notify/test", nil))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("code %d", rr.Code)
	}
	for _, secret := range []string{"TAJNYTOKEN", "555", "/bot123"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Errorf("the test error leaks %q: %s", secret, rr.Body)
		}
	}
}

// ── server start: where the format comes from ────────────────────────────────

func serverWithOptions(t *testing.T, o Options, before func(*store.Store)) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if before != nil {
		before(st)
	}
	o.BackgroundDir = filepath.Join(dir, "bg")
	s, err := New(filepath.Join(dir, "x.yaml"), st, o)
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	return s
}

func TestFormatAfterStartFromDB(t *testing.T) {
	s := serverWithOptions(t, Options{}, func(st *store.Store) {
		_ = st.SetSetting(context.Background(), "notify.format", "gotify")
	})
	if s.notifier.Format() != notify.FormatGotify || s.formatFromEnv {
		t.Errorf("format %q, fromEnv=%v", s.notifier.Format(), s.formatFromEnv)
	}
}

func TestFormatFromEnvBeatsDB(t *testing.T) {
	s := serverWithOptions(t, Options{WebhookFormat: "slack", WebhookFormatFromEnv: true}, func(st *store.Store) {
		_ = st.SetSetting(context.Background(), "notify.format", "gotify")
	})
	if s.notifier.Format() != notify.FormatSlack || !s.formatFromEnv {
		t.Errorf("format %q, fromEnv=%v", s.notifier.Format(), s.formatFromEnv)
	}
}

func TestUnknownFormatInDBDoesNotDisableAlerts(t *testing.T) {
	s := serverWithOptions(t, Options{}, func(st *store.Store) {
		_ = st.SetSetting(context.Background(), "notify.format", "format-z-przyszłości")
	})
	if s.notifier.Format() != notify.FormatDiscord {
		t.Errorf("format %q, want discord", s.notifier.Format())
	}
}

func TestFormatTextsHaveBothLanguages(t *testing.T) {
	keys := []string{"webhook_format", "webhook_bad_format", "webhook_format_from_env",
		"webhook_address_telegram", "webhook_address_gotify", "format_generic"}
	for _, f := range notify.Formats {
		keys = append(keys, "format_hint_"+string(f))
	}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range keys {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: missing text %q", l, k)
			}
		}
	}
}
