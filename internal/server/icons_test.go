package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// przekierowujCDN podmienia hosta w każdym żądaniu na lokalny httptest.Server —
// pozwala testować pobierz()/Czytaj(), które budują URL wprost ze stałej
// cdnIkony, bez wychodzenia w sieć.
type przekierowujCDN struct {
	cel *url.URL
}

func (p przekierowujCDN) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = p.cel.Scheme
	req.URL.Host = p.cel.Host
	req.Host = p.cel.Host
	return http.DefaultTransport.RoundTrip(req)
}

func nowyKatalogZMockiemCDN(t *testing.T, obsługa http.HandlerFunc) (*Katalog, string) {
	t.Helper()
	mock := httptest.NewServer(obsługa)
	t.Cleanup(mock.Close)
	celURL, err := url.Parse(mock.URL)
	if err != nil {
		t.Fatalf("zły URL mocka: %v", err)
	}
	dir := t.TempDir()
	k := NowyKatalog("", dir)
	k.client = &http.Client{Transport: przekierowujCDN{cel: celURL}}
	return k, dir
}

// ── usterka nr 1: puste ciało z CDN zatruwało cache na zawsze ──────────────

func TestPobierzOdrzucaPustaOdpowiedź(t *testing.T) {
	var wywołań int
	k, dir := nowyKatalogZMockiemCDN(t, func(w http.ResponseWriter, r *http.Request) {
		wywołań++
		w.Header().Set("Content-Type", "image/svg+xml")
		w.WriteHeader(http.StatusOK)
		// świadomie NIC nie piszemy do ciała — to jest dokładnie ten
		// przypadek jsDelivr "200 OK, zero bajtów", który wcześniej trwale
		// zatruwał cache na dysku.
	})
	k.rozsz = map[string]string{"pusta-ikona": "svg"}
	k.nazwy = []string{"pusta-ikona"}

	dane, _, err := k.Czytaj(context.Background(), "pusta-ikona")
	if err == nil {
		t.Fatalf("Czytaj miało zwrócić błąd dla pustej odpowiedzi CDN, dostałem dane len=%d", len(dane))
	}
	if wywołań != 1 {
		t.Fatalf("oczekiwałem 1 próby pobrania, było %d", wywołań)
	}

	// ! sedno usterki: bez naprawy ten plik istniał na dysku z 0 bajtów i
	// każde kolejne żądanie serwowało tę samą pustkę w nieskończoność.
	if _, err := os.Stat(filepath.Join(dir, "pusta-ikona.svg")); err == nil {
		t.Fatal("pusta odpowiedź CDN NIE powinna zostać zapisana do cache na dysku")
	}
}

func TestCzytajSamNieUlecząZatrutegoCache(t *testing.T) {
	// Odtwarza produkcyjny stan: plik już leży w cache jako 0 bajtów (np. z
	// czasu przed tą naprawą), ale CDN dziś ma prawidłową treść. Katalog ma
	// samoistnie się z tego wyleczyć przy najbliższym żądaniu, zamiast
	// serwować pustkę z dysku w nieskończoność.
	realneDane := []byte(`<svg>prawdziwa zawartość</svg>`)
	k, dir := nowyKatalogZMockiemCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(realneDane)
	})
	k.rozsz = map[string]string{"zatruta-ikona": "svg"}
	k.nazwy = []string{"zatruta-ikona"}

	if err := os.WriteFile(filepath.Join(dir, "zatruta-ikona.svg"), nil, 0o644); err != nil {
		t.Fatalf("nie mogę przygotować zatrutego pliku cache: %v", err)
	}

	dane, typ, err := k.Czytaj(context.Background(), "zatruta-ikona")
	if err != nil {
		t.Fatalf("Czytaj: %v", err)
	}
	if string(dane) != string(realneDane) {
		t.Fatalf("Czytaj zwróciło %q (z zatrutego pliku 0B?), oczekiwałem %q", dane, realneDane)
	}
	if typ != "image/svg+xml" {
		t.Errorf("typ = %q, chcę image/svg+xml", typ)
	}
	naDysku, err := os.ReadFile(filepath.Join(dir, "zatruta-ikona.svg"))
	if err != nil || len(naDysku) == 0 {
		t.Errorf("plik cache po samoleczeniu powinien mieć realną treść, err=%v len=%d", err, len(naDysku))
	}
}

func TestPobierzZapisujeDoCache(t *testing.T) {
	// kontrolne: normalny, udany download nadal się cache'uje (nie
	// nadgorliwie zablokowany przez strażnika pustego ciała)
	realneDane := []byte(`<svg>ok</svg>`)
	k, dir := nowyKatalogZMockiemCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(realneDane)
	})
	k.rozsz = map[string]string{"ok-ikona": "svg"}
	k.nazwy = []string{"ok-ikona"}

	dane, _, err := k.Czytaj(context.Background(), "ok-ikona")
	if err != nil {
		t.Fatalf("Czytaj: %v", err)
	}
	if string(dane) != string(realneDane) {
		t.Fatalf("dane = %q, chcę %q", dane, realneDane)
	}
	naDysku, err := os.ReadFile(filepath.Join(dir, "ok-ikona.svg"))
	if err != nil || string(naDysku) != string(realneDane) {
		t.Fatalf("plik cache nie zawiera pobranej treści: err=%v dane=%q", err, naDysku)
	}
}

// ── usterka nr 2: warianty jasny/ciemny z metadata.json ────────────────────

func metadanePrzykładowe(t *testing.T) []byte {
	t.Helper()
	m := map[string]any{
		"dagster": map[string]any{
			"base":    "svg",
			"aliases": []string{},
			"colors":  map[string]string{"dark": "dagster-dark", "light": "dagster-light"},
		},
		"jellyfin": map[string]any{
			"base":    "svg",
			"aliases": []string{},
			// brak "colors" — ta ikona nie ma osobnych wariantów
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal metadanych: %v", err)
	}
	return b
}

// TestOdświeżPobieraWariantyTakżePrzySpisieZCache odtwarza najczęstszy
// przypadek produkcyjny: proces restartuje się z ciepłym cache (spis.json
// świeższy niż tydzień), więc Odśwież bierze listę nazw z dysku bez
// wychodzenia w sieć — ale warianty jasny/ciemny nie mają własnego pliku
// cache i MUSZĄ się mimo to pobrać z metadata.json, inaczej po restarcie
// panel wraca do jednej, stałej grafiki (ta sama usterka #2 od nowa).
func TestOdświeżPobieraWariantyTakżePrzySpisieZCache(t *testing.T) {
	var wołańMetadanych int
	k, dir := nowyKatalogZMockiemCDN(t, func(w http.ResponseWriter, r *http.Request) {
		wołańMetadanych++
		w.Write(metadanePrzykładowe(t))
	})

	// spis.json już leży na dysku i jest świeży — Odśwież ma go użyć bez
	// sieci dla LISTY NAZW, ale to nie zwalnia z pobrania wariantów.
	spis, _ := json.Marshal(map[string]string{"dagster": "svg", "jellyfin": "svg"})
	if err := os.WriteFile(filepath.Join(dir, "spis.json"), spis, 0o644); err != nil {
		t.Fatalf("przygotowanie spis.json: %v", err)
	}

	k.Odśwież(context.Background())

	if k.Liczba() != 2 {
		t.Fatalf("Liczba() = %d, chcę 2 (z cache na dysku, bez sieci)", k.Liczba())
	}
	if wołańMetadanych == 0 {
		t.Fatal("Odśwież NIE spróbował pobrać metadata.json mimo świeżego spis.json z dysku — warianty zostaną puste po każdym restarcie")
	}
	if ciemny, jasny := k.Warianty("dagster"); ciemny != "dagster-dark" || jasny != "dagster-light" {
		t.Errorf("Warianty(dagster) po Odśwież() = (%q, %q), chcę (dagster-dark, dagster-light)", ciemny, jasny)
	}
}

func TestZMetadanychBudujeWarianty(t *testing.T) {
	k, _ := nowyKatalogZMockiemCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(metadanePrzykładowe(t))
	})

	if _, err := k.zMetadanych(context.Background()); err != nil {
		t.Fatalf("zMetadanych: %v", err)
	}

	if ciemny, jasny := k.Warianty("dagster"); ciemny != "dagster-dark" || jasny != "dagster-light" {
		t.Errorf("Warianty(dagster) = (%q, %q), chcę (dagster-dark, dagster-light)", ciemny, jasny)
	}
	if ciemny, jasny := k.Warianty("jellyfin"); ciemny != "" || jasny != "" {
		t.Errorf("Warianty(jellyfin) = (%q, %q), chcę oba puste — ta ikona nie ma wariantów", ciemny, jasny)
	}
	if ciemny, jasny := k.Warianty("nieznana"); ciemny != "" || jasny != "" {
		t.Errorf("Warianty(nieznana) = (%q, %q), chcę oba puste", ciemny, jasny)
	}
}

func TestRozwiążIkonęUżywaWariantówGdySą(t *testing.T) {
	k, _ := nowyKatalogZMockiemCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(metadanePrzykładowe(t))
	})
	if _, err := k.zMetadanych(context.Background()); err != nil {
		t.Fatalf("zMetadanych: %v", err)
	}
	s := &Server{ikony: k}

	ref := s.rozwiążIkonę("dagster", "Dagster")
	if ref.Src != "" {
		t.Errorf("dla ikony z wariantami Src powinien być pusty (renderujemy oba warianty), mam %q", ref.Src)
	}
	if ref.WariantCiemny != "/icon/dagster-dark" || ref.WariantJasny != "/icon/dagster-light" {
		t.Errorf("warianty = (%q, %q), chcę (/icon/dagster-dark, /icon/dagster-light)", ref.WariantCiemny, ref.WariantJasny)
	}

	refBezWariantów := s.rozwiążIkonę("jellyfin", "Jellyfin")
	if refBezWariantów.Src != "/icon/jellyfin" {
		t.Errorf("bez wariantów oczekuję zwykłego Src, mam %q", refBezWariantów.Src)
	}
	if refBezWariantów.WariantCiemny != "" || refBezWariantów.WariantJasny != "" {
		t.Errorf("bez wariantów pola WariantCiemny/WariantJasny mają być puste, mam (%q, %q)",
			refBezWariantów.WariantCiemny, refBezWariantów.WariantJasny)
	}
}
