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
	if l.ile() != 0 {
		t.Fatalf("wysłano %d wiadomości po jednej porażce, chcę 0", l.ile())
	}

	p.Obserwuj(context.Background(), zdarzenie("down"))
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
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Obserwuj(context.Background(), zdarzenie("up"))

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
}

func TestDegradedIDownToOsobneStany(t *testing.T) {
	l := nowyŁapacz()
	defer l.srv.Close()
	p := Nowy(l.srv.URL, 1)

	p.Obserwuj(context.Background(), zdarzenie("degraded"))
	p.Obserwuj(context.Background(), zdarzenie("down"))
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
