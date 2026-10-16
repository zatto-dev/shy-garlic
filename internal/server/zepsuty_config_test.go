package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/store"
)

// SHYG-45: zepsuty config nie może zabijać panelu.

const configDobry = "title: Dobry\ngroups:\n  - name: Media\n    services:\n      - name: Sonarr\n"
const configZepsuty = "title: Zepsuty\ngroups:\n\t- name: Media\n"

// serwerNaTreści stawia panel na podanej treści pliku BEZ wczytywania go.
func serwerNaTreści(t *testing.T, treść string) *Server {
	t.Helper()
	katalog := t.TempDir()
	cfg := filepath.Join(katalog, "shygarlic.yaml")
	if treść != "" {
		if err := os.WriteFile(cfg, []byte(treść), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(katalog, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, st, Opcje{BackgroundDir: filepath.Join(katalog, "bg")})
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	t.Cleanup(func() { s.Close(); _ = st.Close() })
	return s
}

var ciasteczkaTestowe sync.Map // *Server → *http.Cookie

// sesja zakłada konto tylko raz na serwer — ciasteczkoZalogowanego woła się raz.
func sesja(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	if c, ok := ciasteczkaTestowe.Load(s); ok {
		return c.(*http.Cookie)
	}
	c := ciasteczkoZalogowanego(t, s)
	ciasteczkaTestowe.Store(s, c)
	return c
}

func stronaGłówna(t *testing.T, s *Server, nagłówki ...string) string {
	t.Helper()
	c := sesja(t, s)
	w := wyślij(s.Handler(), c, "GET", "/", "", nagłówki...)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / → %d: %.200s", w.Code, w.Body)
	}
	return w.Body.String()
}

func TestStartZZepsutymConfigiemNieUmiera(t *testing.T) {
	s := serwerNaTreści(t, configZepsuty)
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatalf("start z zepsutym plikiem ma przejść (crashloop w Dockerze): %v", err)
	}
	html := stronaGłówna(t, s)
	if !strings.Contains(html, `id="baner-config"`) || !strings.Contains(html, `data-stan="error"`) {
		t.Errorf("brak banera błędu:\n%.600s", html)
	}
	// numer linii błędu (tabulator w linii 3)
	if !strings.Contains(html, "Line 3") {
		t.Errorf("w banerze nie ma numeru linii")
	}
	if strings.Contains(html, "Zepsuty") {
		t.Error("panel pokazuje treść zepsutego pliku")
	}
	if !strings.Contains(html, "nothing to show") {
		t.Error("baner nie mówi, że nie ma czego pokazać")
	}
}

func TestStartZeZepsutymConfigiemBierzeOstatniąDobrąZHistorii(t *testing.T) {
	s := serwerNaTreści(t, configZepsuty)
	ctx := context.Background()
	if err := s.st.ZapiszWersję(ctx, "x", "stara", "title: Stary\ngroups:\n  - name: Archiwum\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.ZapiszWersję(ctx, "x", "uszkodzona", configZepsuty, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.ZapiszWersję(ctx, "x", "dobra", configDobry, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.ZapiszWersję(ctx, "x", "też uszkodzona", configZepsuty, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.wczytajPoStarcie(ctx); err != nil {
		t.Fatal(err)
	}
	html := stronaGłówna(t, s)
	if !strings.Contains(html, "Dobry") || !strings.Contains(html, "Sonarr") {
		t.Errorf("nie wzięto najnowszej DZIAŁAJĄCEJ wersji z historii:\n%.800s", html)
	}
	if !strings.Contains(html, "saved in history") {
		t.Error("baner nie mówi, że to wersja z historii")
	}
	// plik na dysku nietknięty — nie nadpisujemy tego, co człowiek właśnie edytuje
	if b, _ := os.ReadFile(s.path); string(b) != configZepsuty {
		t.Errorf("panel zmienił plik na dysku: %q", b)
	}
}

func TestStartBezPlikuTeżNieUmiera(t *testing.T) {
	s := serwerNaTreści(t, "")
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatalf("brak pliku = baner, nie exit: %v", err)
	}
	if html := stronaGłówna(t, s); !strings.Contains(html, `data-stan="error"`) {
		t.Error("brak banera przy braku pliku")
	}
}

// Psucie pliku w trakcie pracy: panel zostaje przy poprzedniej konfiguracji,
// loguje błąd RAZ, nie kręci się co 2 s i sprząta baner po naprawie.
func TestZepsutyPlikPodczasPracyLogujeRazIZdejmujeBaner(t *testing.T) {
	var log bytes.Buffer
	var mu sync.Mutex
	stary := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{&log, &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(stary) })

	s := serwerNaTreści(t, configDobry+"      - name: Radarr\n        chek: 1\n")
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatal(err)
	}
	if html := stronaGłówna(t, s); strings.Contains(html, `data-stan="error"`) {
		t.Fatal("baner przy dobrym pliku")
	}
	if b := s.banerInstancji("en"); len(b.Ostrzeżenia) != 1 {
		t.Fatalf("setup: chcę 1 ostrzeżenie, mam %v", b.Ostrzeżenia)
	}

	zepsuj := func(treść string) {
		if err := os.WriteFile(s.path, []byte(treść), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	zepsuj(configZepsuty)
	for i := 0; i < 5; i++ { // pięć „tyknięć" watch() na tym samym błędzie
		s.sprawdźZmianęPliku(s.ctx)
	}
	mu.Lock()
	n := strings.Count(log.String(), "config niepoprawny")
	mu.Unlock()
	if n != 1 {
		t.Fatalf("błąd zalogowany %d razy, chcę 1", n)
	}
	html := stronaGłówna(t, s)
	if !strings.Contains(html, `data-stan="error"`) || !strings.Contains(html, "Dobry") ||
		!strings.Contains(html, "last working configuration") {
		t.Errorf("po zepsuciu: panel ma zostać na poprzedniej konfiguracji z banerem:\n%.800s", html)
	}

	// ostrzeżenia z poprzedniej wersji pliku nie wiszą obok nowego błędu
	if b := s.banerInstancji("en"); len(b.Ostrzeżenia) != 0 {
		t.Errorf("przy błędzie baner pokazuje stare ostrzeżenia: %v", b.Ostrzeżenia)
	}

	// inny błąd → nowy wpis w logu
	zepsuj("groups: 5\n")
	s.sprawdźZmianęPliku(s.ctx)
	mu.Lock()
	n = strings.Count(log.String(), "config niepoprawny")
	mu.Unlock()
	if n != 2 {
		t.Errorf("inny błąd: %d wpisów, chcę 2", n)
	}

	zepsuj("title: Naprawiony\ngroups:\n  - name: Sieć\n")
	s.sprawdźZmianęPliku(s.ctx)
	html = stronaGłówna(t, s)
	if strings.Contains(html, `data-stan="error"`) || !strings.Contains(html, "Naprawiony") {
		t.Errorf("po naprawie baner ma zniknąć, a panel wczytać plik:\n%.800s", html)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestNieznanyKluczToOstrzeżenieWBanerze(t *testing.T) {
	s := serwerNaTreści(t, "groups:\n  - name: G\n    services:\n      - name: S\n        chek: {interval: 5s}\n")
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatal(err)
	}
	html := stronaGłówna(t, s)
	if !strings.Contains(html, `data-stan="warnings"`) {
		t.Fatalf("brak banera ostrzeżeń:\n%.600s", html)
	}
	for _, chcę := range []string{`unknown key`, "chek", "line 5", `did you mean`, "check"} {
		if !strings.Contains(html, chcę) {
			t.Errorf("w banerze brak %q", chcę)
		}
	}
	// po polsku
	pl := stronaGłówna(t, s, "Accept-Language", "pl")
	for _, chcę := range []string{"nieznany klucz", "linia 5", "chodziło o"} {
		if !strings.Contains(pl, chcę) {
			t.Errorf("wersja PL: brak %q", chcę)
		}
	}
	// sam panel działa normalnie
	if !strings.Contains(html, "S") {
		t.Error("usługa zniknęła")
	}
}

func TestDobryPlikBezBanera(t *testing.T) {
	s := serwerNaTreści(t, configDobry)
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatal(err)
	}
	if html := stronaGłówna(t, s); !strings.Contains(html, `data-stan="ok"`) {
		t.Error("baner nie jest w stanie ok")
	}
}

// Edytor surowego YAML musi działać na zepsutym pliku — to jedyne miejsce w UI,
// z którego da się go naprawić.
func TestSurowyEdytorNaprawiaZepsutyPlik(t *testing.T) {
	s := serwerNaTreści(t, configZepsuty)
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := sesja(t, s)

	// zły YAML dalej odrzucony
	w := wyślij(h, c, "PUT", "/api/config", `{"yaml":"groups: [\n"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("zły YAML przyjęty: %d", w.Code)
	}
	w = wyślij(h, c, "PUT", "/api/config", `{"yaml":"title: Naprawa\ngroups:\n  - name: Sieć\n"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("naprawa przez edytor: %d %s", w.Code, w.Body)
	}
	if html := stronaGłówna(t, s); strings.Contains(html, `data-stan="error"`) || !strings.Contains(html, "Naprawa") {
		t.Errorf("po naprawie baner został:\n%.600s", html)
	}
	// operacje strukturalne na zepsutym pliku dają czytelny błąd, nie panikę
	if err := os.WriteFile(s.path, []byte(configZepsuty), 0o644); err != nil {
		t.Fatal(err)
	}
	w = wyślij(h, c, "POST", "/api/groups", `{"name":"X"}`)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /api/groups na zepsutym pliku → %d, chcę 500 z komunikatem", w.Code)
	}
}

// Baner jedzie do otwartych kart w zdarzeniu SSE „konfig".
func TestBanerPrzezSSE(t *testing.T) {
	s := serwerNaTreści(t, configDobry)
	if err := s.wczytajPoStarcie(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := sesja(t, s)

	req, _ := http.NewRequest("GET", srv.URL+"/events", nil)
	req.AddCookie(c)
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	zdarzenia := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var typ string
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				typ = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: ") && typ == "konfig":
				zdarzenia <- strings.TrimPrefix(l, "data: ")
				typ = ""
			case l == "":
				typ = ""
			}
		}
	}()

	time.Sleep(100 * time.Millisecond) // subskrypcja
	if err := os.WriteFile(s.path, []byte(configZepsuty), 0o644); err != nil {
		t.Fatal(err)
	}
	s.sprawdźZmianęPliku(s.ctx)

	select {
	case d := <-zdarzenia:
		var b struct {
			Stan  string `json:"state"`
			Błąd  string `json:"error"`
			Tytuł string `json:"title"`
		}
		if err := json.Unmarshal([]byte(d), &b); err != nil {
			t.Fatal(err)
		}
		if b.Stan != "error" || !strings.Contains(b.Błąd, "3") || b.Tytuł == "" {
			t.Errorf("zdarzenie konfig = %s", d)
		}
	case <-ctx.Done():
		t.Fatal("brak zdarzenia konfig w SSE")
	}
}
