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

func putNotify(s *Server, ciało string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify", strings.NewReader(ciało)))
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

func komunikat(rr *httptest.ResponseRecorder) string {
	var o struct{ Błąd string `json:"error"` }
	_ = json.Unmarshal(rr.Body.Bytes(), &o)
	return o.Błąd
}

func TestNotifyGetPokazujeFormat(t *testing.T) {
	s := serwerTestowy(t)
	m := getNotify(t, s)
	if m["format"] != "discord" || m["formatZrodlo"] != "domyslny" {
		t.Errorf("domyślny: %v", m)
	}
	lista, _ := m["formaty"].([]any)
	if len(lista) != 6 || lista[0] != "discord" || lista[5] != "generic" {
		t.Errorf("formaty: %v", lista)
	}
}

func TestNotifyPutZapisujeFormatDoBazy(t *testing.T) {
	s := serwerTestowy(t)
	rr := putNotify(s, `{"format":"ntfy"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", rr.Code, rr.Body)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "ntfy" {
		t.Errorf("w bazie: %q", v)
	}
	if s.powiad.Format() != notify.FormatNtfy {
		t.Errorf("format w powiadamiaczu: %q", s.powiad.Format())
	}
	if m := getNotify(t, s); m["format"] != "ntfy" || m["formatZrodlo"] != "baza" {
		t.Errorf("GET: %v", m)
	}
	// powrót do domyślnego kasuje wiersz
	if rr := putNotify(s, `{"format":"discord"}`); rr.Code != http.StatusOK {
		t.Fatalf("kod %d", rr.Code)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("discord jest domyślny i nie powinien leżeć w bazie: %q", v)
	}
}

func TestNotifyPutOdrzucaNieznanyFormat(t *testing.T) {
	s := serwerTestowy(t)
	for _, f := range []string{"teams", "", "Discord", "ntfy "} {
		rr := putNotify(s, `{"format":"`+f+`"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%q: kod %d, chcę 400", f, rr.Code)
		}
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("w bazie coś zostało: %q", v)
	}
}

// Ta sama zasada co przy SHYG_WEBHOOK: to, co przyszło ze środowiska, jest
// własnością deployu i klikanie w panelu tego nie cofa.
func TestNotifyPutFormatZeŚrodowiskaTo409(t *testing.T) {
	s := serwerTestowy(t)
	s.formatZeŚrodowiska = true
	s.powiad.SetFormat(notify.FormatSlack)
	rr := putNotify(s, `{"format":"ntfy"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("kod %d, chcę 409", rr.Code)
	}
	if s.powiad.Format() != notify.FormatSlack {
		t.Error("format zmieniony mimo 409")
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("baza ruszona: %q", v)
	}
	if m := getNotify(t, s); m["formatZrodlo"] != "srodowisko" {
		t.Errorf("źródło: %v", m["formatZrodlo"])
	}
}

// Adres z flagi nie blokuje formatu: to dwa niezależne ustawienia, każde z
// własnym źródłem.
func TestNotifyPutFormatDziałaPrzyAdresieZeŚrodowiska(t *testing.T) {
	s := serwerTestowy(t)
	s.webhookZeŚrodowiska = true
	s.powiad.SetWebhook("https://ntfy.example/temat")
	if rr := putNotify(s, `{"format":"ntfy"}`); rr.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", rr.Code, rr.Body)
	}
}

func TestNotifyPutPilnujeKształtuAdresuWgFormatu(t *testing.T) {
	s := serwerTestowy(t)
	// razem: format + zły adres → 400, nic nie zapisane
	rr := putNotify(s, `{"format":"telegram","webhook":"https://api.telegram.org/bot1:A/sendMessage"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(komunikat(rr), "chat_id") {
		t.Fatalf("kod %d: %s", rr.Code, rr.Body)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.webhook"); v != "" {
		t.Errorf("adres zapisany mimo odrzucenia: %q", v)
	}
	if v, _ := s.st.Setting(s.ctx, "notify.format"); v != "" {
		t.Errorf("format zapisany mimo odrzucenia: %q", v)
	}
	// dobry
	rr = putNotify(s, `{"format":"telegram","webhook":"https://api.telegram.org/bot1:A/sendMessage?chat_id=42"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", rr.Code, rr.Body)
	}
	// sam nowy adres sprawdzany względem ZAPISANEGO formatu
	if rr := putNotify(s, `{"webhook":"https://api.telegram.org/bot1:A/sendMessage"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("adres bez chat_id przy formacie telegram: kod %d", rr.Code)
	}
	// sam nowy format sprawdzany względem ZAPISANEGO adresu
	if rr := putNotify(s, `{"format":"gotify"}`); rr.Code != http.StatusBadRequest || !strings.Contains(komunikat(rr), "token") {
		t.Errorf("gotify na adresie telegrama: kod %d: %s", rr.Code, rr.Body)
	}
	if s.powiad.Format() != notify.FormatTelegram {
		t.Errorf("format po odrzuconych zmianach: %q", s.powiad.Format())
	}
	// komunikat po polsku
	rr = httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify", strings.NewReader(`{"format":"gotify"}`))
	req.Header.Set("Accept-Language", "pl")
	s.handleNotifyPut(rr, req)
	if komunikat(rr) != i18n.T(i18n.PL, "webhook_adres_gotify") {
		t.Errorf("komunikat PL: %q", komunikat(rr))
	}
}

func TestNotifyPutWszystkieFormatyWymagająHTTPS(t *testing.T) {
	for _, f := range notify.Formats {
		s := serwerTestowy(t)
		rr := putNotify(s, `{"format":"`+string(f)+`","webhook":"http://lan.example/message?token=x&chat_id=1"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: http przeszło (kod %d)", f, rr.Code)
		}
	}
}

// Sekrety siedzą w różnych miejscach adresu: token bota i temat ntfy w
// ścieżce, token Gotify i chat_id w query. Maska zostawia schemat i host.
func TestMaskaZasłaniaSekretyWKażdymFormacie(t *testing.T) {
	cases := []struct {
		format, adres string
		sekrety       []string
	}{
		{"telegram", "https://api.telegram.org/bot123456:AAH-sekretBota/sendMessage?chat_id=-1009988", []string{"123456", "sekretBota", "1009988", "sendMessage"}},
		{"gotify", "https://gotify.home.arpa/message?token=TajnyTokenGotify", []string{"TajnyTokenGotify", "token"}},
		{"ntfy", "https://anna:haslo@ntfy.home.arpa/temat-tajny", []string{"anna", "haslo", "temat-tajny"}},
		{"slack", "https://hooks.slack.com/services/T000/B000/sekretSlacka", []string{"T000", "sekretSlacka"}},
		{"generic", "https://hook.home.arpa/in/sekretGeneric?k=v", []string{"sekretGeneric", "k=v"}},
	}
	for _, c := range cases {
		s := serwerTestowy(t)
		if rr := putNotify(s, `{"format":"`+c.format+`","webhook":"`+c.adres+`"}`); rr.Code != http.StatusOK {
			t.Fatalf("%s: kod %d: %s", c.format, rr.Code, rr.Body)
		}
		rr := httptest.NewRecorder()
		s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
		for _, sekret := range c.sekrety {
			if strings.Contains(rr.Body.String(), sekret) {
				t.Errorf("%s: odpowiedź GET zdradza %q: %s", c.format, sekret, rr.Body)
			}
		}
		// odpowiedź na sam PUT też
		rr = putNotify(s, `{"format":"`+c.format+`"}`)
		for _, sekret := range c.sekrety {
			if strings.Contains(rr.Body.String(), sekret) {
				t.Errorf("%s: odpowiedź PUT zdradza %q: %s", c.format, sekret, rr.Body)
			}
		}
	}
}

// Przycisk „wyślij próbę" w każdym formacie, na prawdziwym handlerze.
func TestPrzyciskTestDziałaWKażdymFormacie(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.URL.RequestURI()+" "+r.Header.Get("Title")+" "+string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	ścieżki := map[notify.Format]string{
		notify.FormatDiscord: "/api/webhooks/1/a", notify.FormatSlack: "/services/a/b/c",
		notify.FormatNtfy: "/temat", notify.FormatGotify: "/message?token=T",
		notify.FormatTelegram: "/bot1:A/sendMessage?chat_id=7", notify.FormatGeneric: "/h",
	}
	for _, f := range notify.Formats {
		s := serwerTestowy(t)
		s.powiad.SetWebhook(srv.URL + ścieżki[f]) // http: omija regułę https z PUT, to nie jest test tej reguły
		s.powiad.SetFormat(f)
		rr := httptest.NewRecorder()
		s.handleNotifyTest(rr, httptest.NewRequest("POST", "/api/notify/test", nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s: kod %d: %s", f, rr.Code, rr.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(notify.Formats) {
		t.Fatalf("żądań %d, chcę %d: %v", len(got), len(notify.Formats), got)
	}
	for i, g := range got {
		if !strings.Contains(strings.ToLower(g), "test") {
			t.Errorf("%s: %s", notify.Formats[i], g)
		}
	}
}

func TestPrzyciskTestNieZdradzaAdresuPrzyBłędzie(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	adres := srv.URL + "/bot123:TAJNYTOKEN/sendMessage?chat_id=555"
	srv.Close()

	s := serwerTestowy(t)
	s.powiad.SetWebhook(adres)
	s.powiad.SetFormat(notify.FormatTelegram)
	rr := httptest.NewRecorder()
	s.handleNotifyTest(rr, httptest.NewRequest("POST", "/api/notify/test", nil))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("kod %d", rr.Code)
	}
	for _, sekret := range []string{"TAJNYTOKEN", "555", "/bot123"} {
		if strings.Contains(rr.Body.String(), sekret) {
			t.Errorf("błąd testu zdradza %q: %s", sekret, rr.Body)
		}
	}
}

// ── start serwera: skąd bierze się format ────────────────────────────────────

func serwerZOpcjami(t *testing.T, o Opcje, przed func(*store.Store)) *Server {
	t.Helper()
	katalog := t.TempDir()
	st, err := store.Open(filepath.Join(katalog, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if przed != nil {
		przed(st)
	}
	o.BackgroundDir = filepath.Join(katalog, "bg")
	s, err := New(filepath.Join(katalog, "x.yaml"), st, o)
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	return s
}

func TestFormatPoStarcieZBazy(t *testing.T) {
	s := serwerZOpcjami(t, Opcje{}, func(st *store.Store) {
		_ = st.SetSetting(context.Background(), "notify.format", "gotify")
	})
	if s.powiad.Format() != notify.FormatGotify || s.formatZeŚrodowiska {
		t.Errorf("format %q, zeŚrodowiska=%v", s.powiad.Format(), s.formatZeŚrodowiska)
	}
}

func TestFormatZeŚrodowiskaBijeBazę(t *testing.T) {
	s := serwerZOpcjami(t, Opcje{WebhookFormat: "slack", WebhookFormatZeŚrodowiska: true}, func(st *store.Store) {
		_ = st.SetSetting(context.Background(), "notify.format", "gotify")
	})
	if s.powiad.Format() != notify.FormatSlack || !s.formatZeŚrodowiska {
		t.Errorf("format %q, zeŚrodowiska=%v", s.powiad.Format(), s.formatZeŚrodowiska)
	}
}

func TestNieznanyFormatWBazieNieWyłączaAlertów(t *testing.T) {
	s := serwerZOpcjami(t, Opcje{}, func(st *store.Store) {
		_ = st.SetSetting(context.Background(), "notify.format", "format-z-przyszłości")
	})
	if s.powiad.Format() != notify.FormatDiscord {
		t.Errorf("format %q, chcę discord", s.powiad.Format())
	}
}

func TestTekstyFormatówMająObaJęzyki(t *testing.T) {
	klucze := []string{"format_webhooka", "webhook_zly_format", "webhook_format_ze_srodowiska",
		"webhook_adres_telegram", "webhook_adres_gotify", "format_generic"}
	for _, f := range notify.Formats {
		klucze = append(klucze, "format_podpowiedz_"+string(f))
	}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range klucze {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: brak tekstu %q", l, k)
			}
		}
	}
}
