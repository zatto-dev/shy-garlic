package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type łapacz struct {
	mu    sync.Mutex
	ciała []map[string]any
	srv   *httptest.Server
}

func nowyŁapacz() *łapacz {
	l := &łapacz{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		l.mu.Lock()
		l.ciała = append(l.ciała, m)
		l.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	return l
}

func (l *łapacz) ile() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ciała)
}

func (l *łapacz) tytuł(i int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	embeds, _ := l.ciała[i]["embeds"].([]any)
	if len(embeds) == 0 {
		return ""
	}
	e, _ := embeds[0].(map[string]any)
	t, _ := e["title"].(string)
	return t
}

func zdarzenie(stan string) Zdarzenie {
	return Zdarzenie{Klucz: "Media/Jellyfin", Nazwa: "Jellyfin", Grupa: "Media",
		URL: "http://x:8096", Stan: stan, Kiedy: time.Now()}
}

// Jedno mrugnięcie sieci nie ma budzić nikogo w nocy.
func TestPojedynczaPorażkaNiePowiadamia(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 2)

	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	if l.ile() != 0 {
		t.Fatalf("wysłano %d wiadomości po jednej porażce, chcę 0", l.ile())
	}

	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	if l.ile() != 1 {
		t.Fatalf("wysłano %d po drugiej porażce, chcę 1", l.ile())
	}
}

func TestNieSpamujeTymSamymStanem(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 1)

	for i := 0; i < 5; i++ {
		p.Obserwuj(context.Background(), zdarzenie("down"))
		p.Poczekaj()
	}
	if l.ile() != 1 {
		t.Fatalf("wysłano %d wiadomości przy stałej awarii, chcę 1", l.ile())
	}
}

func TestPowrótZgłaszanyOdRazu(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 2)

	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	p.Obserwuj(context.Background(), zdarzenie("up"))
	p.Poczekaj()

	if l.ile() != 2 {
		t.Fatalf("wiadomości = %d, chcę 2 (awaria + powrót)", l.ile())
	}
	if got := l.tytuł(1); got == "" || got[:4] != "🟢"[:4] {
		t.Errorf("druga wiadomość to nie powrót: %q", got)
	}
}

// Usługa, która działa od początku, nie generuje żadnego szumu.
func TestSameSukcesyMilczą(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 2)

	for i := 0; i < 10; i++ {
		p.Obserwuj(context.Background(), zdarzenie("up"))
		p.Poczekaj()
	}
	if l.ile() != 0 {
		t.Fatalf("wysłano %d wiadomości dla działającej usługi, chcę 0", l.ile())
	}
}

func TestBezWebhookaNicSięNieDzieje(t *testing.T) {
	p := Nowy("", 2)
	if p.Włączony() {
		t.Fatal("pusty webhook ma być wyłączony")
	}
	p.Obserwuj(context.Background(), zdarzenie("down")) // nie może panikować
	p.Poczekaj()
}

func TestDegradedIDownToOsobneStany(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 1)

	p.Obserwuj(context.Background(), zdarzenie("degraded"))
	p.Poczekaj()
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	if l.ile() != 2 {
		t.Fatalf("wiadomości = %d, chcę 2 — przejście degraded→down to nowa informacja", l.ile())
	}
}

func TestUstawWebhookWłączaIWyłącza(t *testing.T) {
	p := Nowy("", 2)
	if p.Włączony() {
		t.Fatal("pusty webhook ma znaczyć wyłączone")
	}
	p.UstawWebhook("https://example.test/hook")
	if !p.Włączony() {
		t.Fatal("po ustawieniu adresu ma być włączone")
	}
	if got := p.Webhook(); got != "https://example.test/hook" {
		t.Fatalf("chcę zapisany adres, mam %q", got)
	}
	p.UstawWebhook("")
	if p.Włączony() {
		t.Fatal("wyczyszczenie adresu ma wyłączyć")
	}
}

// Próg poniżej 1 nie ma sensu — jedno nieudane sprawdzenie to jeszcze nie
// awaria, a zero znaczyłoby powiadomienie przy każdym mignięciu.
func TestUstawPrógProstuje(t *testing.T) {
	p := Nowy("", 5)
	p.UstawPróg(0)
	if p.Próg() != 1 {
		t.Fatalf("próg 0 ma się wyprostować do 1, mam %d", p.Próg())
	}
	p.UstawPróg(4)
	if p.Próg() != 4 {
		t.Fatalf("chcę 4, mam %d", p.Próg())
	}
}

// Odczyt z pętli monitora i zapis z żądania HTTP chodzą równolegle. Test ma
// sens wyłącznie pod -race, ale bez niego i tak nie zaszkodzi.
func TestWebhookRównolegle(t *testing.T) {
	p := Nowy("https://example.test/a", 2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			p.UstawWebhook("https://example.test/b")
			p.UstawWebhook("https://example.test/a")
		}
		close(done)
	}()
	for i := 0; i < 500; i++ {
		_ = p.Włączony()
		_ = p.Webhook()
	}
	<-done
}

// ── SHYG-42: nieudana wysyłka nie może zgubić alertu ─────────────────────────

// zawodnyWebhook odpowiada 500 przez pierwsze `padnij` żądań, potem 204.
func zawodnyWebhook(padnij int) (*httptest.Server, *int, *sync.Mutex) {
	var mu sync.Mutex
	licznik := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		licznik++
		n := licznik
		mu.Unlock()
		if n <= padnij {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return srv, &licznik, &mu
}

func krótkieOpóźnienia(t *testing.T) {
	t.Helper()
	stare := opóźnieniaPonowień
	opóźnieniaPonowień = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { opóźnieniaPonowień = stare })
}

// Chwilowy błąd webhooka w obrębie jednego wywołania → ponowienie, alert dociera.
func TestNieudanaWysyłkaJestPonawiana(t *testing.T) {
	krótkieOpóźnienia(t)
	srv, licznik, mu := zawodnyWebhook(1)
	defer srv.Close()
	p := Nowy(srv.URL, 1)

	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()

	mu.Lock()
	defer mu.Unlock()
	if *licznik != 2 {
		t.Fatalf("żądań = %d, chcę 2 (porażka + ponowienie)", *licznik)
	}
	// dostarczone → kolejny „down" nie może spamować
	mu.Unlock()
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	mu.Lock()
	if *licznik != 2 {
		t.Fatalf("po udanej wysyłce kolejny down wysłał ponownie (%d żądań)", *licznik)
	}
}

// Webhook leży przez wszystkie próby → alert NIE jest uznany za zgłoszony,
// następne sprawdzenie próbuje jeszcze raz (wcześniej: zgubiony na zawsze).
func TestPorażkaWysyłkiZostawiaStanDoPonowieniaPrzyNastępnymSprawdzeniu(t *testing.T) {
	krótkieOpóźnienia(t)
	srv, licznik, mu := zawodnyWebhook(1 + len(opóźnieniaPonowień)) // cała pierwsza seria pada
	defer srv.Close()
	p := Nowy(srv.URL, 1)

	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	mu.Lock()
	pierwsza := *licznik
	mu.Unlock()
	if pierwsza != 1+len(opóźnieniaPonowień) {
		t.Fatalf("żądań w pierwszej serii = %d, chcę %d", pierwsza, 1+len(opóźnieniaPonowień))
	}

	p.Obserwuj(context.Background(), zdarzenie("down")) // następne sprawdzenie
	p.Poczekaj()
	mu.Lock()
	defer mu.Unlock()
	if *licznik != pierwsza+1 {
		t.Fatalf("następne sprawdzenie nie ponowiło alertu (żądań %d)", *licznik)
	}
	// a teraz dostarczone — cisza
	mu.Unlock()
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	mu.Lock()
	if *licznik != pierwsza+1 {
		t.Fatalf("po dostarczeniu alert poszedł znowu (%d)", *licznik)
	}
}

// Nieudany powrót też musi się ponowić.
func TestNieudanyPowrótJestPonawiany(t *testing.T) {
	krótkieOpóźnienia(t)
	l := nowyŁapacz()
	p := Nowy(l.srv.URL, 1)
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	l.srv.Close() // webhook znika

	p.Obserwuj(context.Background(), zdarzenie("up"))
	p.Poczekaj()
	p.mu.Lock()
	z := p.stan["Media/Jellyfin"].zgłoszony
	p.mu.Unlock()
	if z != "down" {
		t.Fatalf("po nieudanym powrocie zgłoszony = %q, chcę down (do ponowienia)", z)
	}
}

// Stan „unknown" kiedyś wracał z trzymanym mutexem — następne wywołanie wisiało.
func TestUnknownNieBlokujeMutexu(t *testing.T) {
	p := Nowy("https://example.test/h", 1)
	p.Obserwuj(context.Background(), zdarzenie("unknown"))
	p.Poczekaj()
	done := make(chan struct{})
	go func() {
		p.Zapomnij(map[string]bool{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("mutex powiadamiacza został zajęty po stanie unknown")
	}
}

// SHYG-41: awaria trwa mimo przeniesienia usługi — nie alarmujemy od zera.
func TestPrzenieśZachowujeStanZgłoszenia(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 1)
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	if l.ile() != 1 {
		t.Fatalf("setup: wysłano %d", l.ile())
	}

	p.Przenieś(map[string]string{"Media/Jellyfin": "Sieć/Jellyfin"})
	p.Zapomnij(map[string]bool{"Sieć/Jellyfin": true}) // tak robi reload()

	z := zdarzenie("down")
	z.Klucz = "Sieć/Jellyfin"
	p.Obserwuj(context.Background(), z)
	p.Poczekaj()
	if l.ile() != 1 {
		t.Fatalf("po przeniesieniu alert poszedł drugi raz (%d)", l.ile())
	}
	z.Stan = "up"
	p.Obserwuj(context.Background(), z)
	p.Poczekaj()
	if l.ile() != 2 {
		t.Fatalf("powrót po przeniesieniu nie został zgłoszony (%d)", l.ile())
	}
}
