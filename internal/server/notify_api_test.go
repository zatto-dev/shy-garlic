package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// plikConfigu czyta plik z dysku, a nie stan w pamięci — chodzi o sprawdzenie,
// że zapis naprawdę wyszedł poza proces.
func (s *Server) plikConfigu(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("czytam config: %v", err)
	}
	return b
}

func TestNotifyGetŹródłoBaza(t *testing.T) {
	s := serwerTestowy(t)
	if err := s.st.UstawUstawienie(s.ctx, "notify.webhook", "https://example.test/hooks/abc123"); err != nil {
		t.Fatalf("zapis: %v", err)
	}
	s.powiad.UstawWebhook("https://example.test/hooks/abc123")

	rr := httptest.NewRecorder()
	s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d", rr.Code)
	}
	var odp struct {
		Włączony bool   `json:"wlaczony"`
		Webhook  string `json:"webhook"`
		Źródło   string `json:"zrodlo"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&odp); err != nil {
		t.Fatalf("dekoduję odpowiedź: %v", err)
	}
	if !odp.Włączony || odp.Źródło != "baza" {
		t.Fatalf("chcę włączony/baza, mam %v/%q", odp.Włączony, odp.Źródło)
	}
	if strings.Contains(odp.Webhook, "abc123") {
		t.Fatalf("sekret ma być zamaskowany, poszedł w całości: %q", odp.Webhook)
	}
	if !strings.Contains(odp.Webhook, "example.test") {
		t.Fatalf("host ma zostać, żeby dało się rozpoznać adres: %q", odp.Webhook)
	}
}

func TestNotifyGetBezWebhooka(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
	var odp struct {
		Włączony bool   `json:"wlaczony"`
		Źródło   string `json:"zrodlo"`
	}
	json.NewDecoder(rr.Body).Decode(&odp)
	if odp.Włączony || odp.Źródło != "brak" {
		t.Fatalf("chcę wyłączony/brak, mam %v/%q", odp.Włączony, odp.Źródło)
	}
}

// Webhook ze środowiska jest własnością deployu. Panel nie ma prawa go
// nadpisać — inaczej restart kontenera po cichu cofnąłby zmianę z UI.
func TestNotifyPutOdrzucaGdyŹródłoŚrodowisko(t *testing.T) {
	s := serwerTestowy(t)
	s.webhookZeŚrodowiska = true

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"https://inny.test/hook"}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("chcę 409, mam %d", rr.Code)
	}
	v, _ := s.st.Ustawienie(s.ctx, "notify.webhook")
	if v != "" {
		t.Fatalf("odrzucone żądanie nie ma nic zapisywać, w bazie leży %q", v)
	}
}

func TestNotifyPutZapisujeDoBazyIWłącza(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"https://example.test/hooks/xyz"}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d — ciało: %s", rr.Code, rr.Body.String())
	}
	v, _ := s.st.Ustawienie(s.ctx, "notify.webhook")
	if v != "https://example.test/hooks/xyz" {
		t.Fatalf("adres ma wylądować w bazie, mam %q", v)
	}
	if !s.powiad.Włączony() {
		t.Fatal("po zapisie powiadomienia mają być włączone bez restartu")
	}
}

// Sekret jedzie w ścieżce adresu. Po http poszedłby otwartym tekstem, więc
// panel nie może na to pozwolić.
func TestNotifyPutOdrzucaHTTP(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"http://example.test/hook"}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("chcę 400 dla http, mam %d", rr.Code)
	}
}

func TestNotifyPutCzyściWebhook(t *testing.T) {
	s := serwerTestowy(t)
	_ = s.st.UstawUstawienie(s.ctx, "notify.webhook", "https://example.test/hook")
	s.powiad.UstawWebhook("https://example.test/hook")

	rr := httptest.NewRecorder()
	s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":""}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d — ciało: %s", rr.Code, rr.Body.String())
	}
	if s.powiad.Włączony() {
		t.Fatal("po wyczyszczeniu powiadomienia mają być wyłączone")
	}
	v, _ := s.st.Ustawienie(s.ctx, "notify.webhook")
	if v != "" {
		t.Fatalf("wiersz ma zniknąć z bazy, leży %q", v)
	}
}

func TestNotifyPutZapisujePróg(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"prog":4}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d — ciało: %s", rr.Code, rr.Body.String())
	}
	if s.powiad.Próg() != 4 {
		t.Fatalf("próg ma zadziałać bez restartu, mam %d", s.powiad.Próg())
	}
	if !strings.Contains(string(s.plikConfigu(t)), "threshold: 4") {
		t.Fatalf("próg ma wylądować w YAML-u:\n%s", s.plikConfigu(t))
	}
}

func TestNotifyPutOdrzucaZłyPróg(t *testing.T) {
	s := serwerTestowy(t)
	for _, ciało := range []string{`{"prog":0}`, `{"prog":11}`} {
		rr := httptest.NewRecorder()
		s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify", strings.NewReader(ciało)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: chcę 400, mam %d", ciało, rr.Code)
		}
	}
}

func TestInstanceZwracaŹródłaBezSekretów(t *testing.T) {
	s := serwerTestowy(t)
	s.instancja = []OpisOpcji{
		{Klucz: "addr", Wartość: ":8080", Źródło: "domyslna"},
		{Klucz: "trusted-proxy", Wartość: "10.0.0.0/8", Źródło: "flaga"},
	}
	rr := httptest.NewRecorder()
	s.handleInstance(rr, httptest.NewRequest("GET", "/api/instance", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d", rr.Code)
	}
	ciało := rr.Body.String()
	for _, chcę := range []string{"addr", ":8080", "domyslna", "trusted-proxy", "flaga"} {
		if !strings.Contains(ciało, chcę) {
			t.Fatalf("brakuje %q w %s", chcę, ciało)
		}
	}
	// Webhook jest sekretem i nie ma prawa tu być, nawet zamaskowany —
	// od niego jest /api/notify.
	if strings.Contains(ciało, "webhook") {
		t.Fatal("webhook nie należy do listy instancji")
	}
}

func TestDiscoverWyłączonyZwraca403(t *testing.T) {
	s := serwerTestowy(t)
	nie := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &nie
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleDiscover(rr, httptest.NewRequest("GET", "/api/discover", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("chcę 403 przy wyłączonym podpowiadaniu, mam %d — ciało: %s", rr.Code, rr.Body.String())
	}
}

// Brak klucza to nie to samo co false — ma się zachować jak dotąd, czyli
// odmówić z powodu braku gniazda (404), a nie z powodu wyłączenia (403).
func TestDiscoverBezKluczaNieJestWyłączony(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	s.handleDiscover(rr, httptest.NewRequest("GET", "/api/discover", nil))
	if rr.Code == http.StatusForbidden {
		t.Fatal("bez klucza podpowiadanie ma zostać włączone")
	}
}

// Panel chowa pozycję menu na podstawie /api/info.docker — wyłączenie w pliku
// musi się tam odbić, inaczej user klikałby w coś, co odpowiada 403.
func TestInfoOdbijaWyłączonyDocker(t *testing.T) {
	s := serwerTestowy(t)
	nie := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &nie
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleInfo(rr, httptest.NewRequest("GET", "/api/info", nil))
	var odp struct {
		Docker    bool `json:"docker"`
		DockerNew int  `json:"dockerNew"`
	}
	json.NewDecoder(rr.Body).Decode(&odp)
	if odp.Docker {
		t.Fatal("wyłączony docker ma iść do UI jako false")
	}
	if odp.DockerNew != 0 {
		t.Fatalf("plakietka ma być pusta przy schowanej pozycji, mam %d", odp.DockerNew)
	}
}

// Wyłączenie w pliku i brak gniazda muszą dojechać do UI osobno — inaczej
// panel nie wie, czy pozycję schować, czy tylko wygasić.
func TestInfoRozróżniaPowodyBrakuDockera(t *testing.T) {
	s := serwerTestowy(t)
	nie := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &nie
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleInfo(rr, httptest.NewRequest("GET", "/api/info", nil))
	var odp struct {
		Docker    bool `json:"docker"`
		Wyłączony bool `json:"dockerWylaczony"`
	}
	json.NewDecoder(rr.Body).Decode(&odp)
	if !odp.Wyłączony {
		t.Fatal("wyłączenie w pliku ma być odróżnialne od braku gniazda")
	}

	// bez klucza: nie jest wyłączone, choćby gniazda nie było
	s2 := serwerTestowy(t)
	rr2 := httptest.NewRecorder()
	s2.handleInfo(rr2, httptest.NewRequest("GET", "/api/info", nil))
	var odp2 struct {
		Wyłączony bool `json:"dockerWylaczony"`
	}
	json.NewDecoder(rr2.Body).Decode(&odp2)
	if odp2.Wyłączony {
		t.Fatal("brak klucza to nie jest wyłączenie")
	}
}

// notify.threshold z pliku musi zadziałać przy starcie, nie dopiero po zapisie
// z panelu. Bez tego ustawienie progu w pliku i restart cicho przywracały
// domyślną dwójkę — a plik jest w tym projekcie źródłem prawdy.
func TestPrógZPlikuDziałaPoStarcie(t *testing.T) {
	s := serwerTestowy(t)
	if !s.zapiszDoConfigu(t, "notify:\n  threshold: 5\n") {
		return
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s.powiad.Próg(); got != 5 {
		t.Fatalf("próg z pliku ma obowiązywać po starcie, mam %d", got)
	}
}

// Jawnie podana flaga jest własnością deployu i bije plik — ta sama zasada,
// co przy webhooku, żeby restart kontenera nie cofał ustawienia z zewnątrz.
func TestFlagaProguBijePlik(t *testing.T) {
	s := serwerTestowy(t)
	s.prógZFlagi = true
	s.powiad.UstawPróg(7)
	if !s.zapiszDoConfigu(t, "notify:\n  threshold: 5\n") {
		return
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s.powiad.Próg(); got != 7 {
		t.Fatalf("flaga ma wygrać z plikiem, mam %d", got)
	}
}

// zapiszDoConfigu dopisuje blok na koniec pliku konfiguracyjnego serwera.
func (s *Server) zapiszDoConfigu(t *testing.T, blok string) bool {
	t.Helper()
	stare, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("czytam config: %v", err)
	}
	if err := os.WriteFile(s.path, append(stare, []byte("\n"+blok)...), 0o644); err != nil {
		t.Fatalf("zapisuję config: %v", err)
	}
	return true
}
