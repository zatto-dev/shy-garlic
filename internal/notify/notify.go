// Package notify wysyła wiadomość, gdy usługa padnie albo wróci.
//
// ! Adres webhooka NIE jest w pliku konfiguracyjnym. Config jest źródłem prawdy,
// pokazujemy go w UI i commitujemy do gita — sekret w takim pliku wyciekłby
// przy pierwszym `git push`. Mieszka więc w SQLite, obok haseł i sesji, a
// panel ustawia go przez PUT /api/notify.
//
// Zmienna środowiskowa (SHYG_WEBHOOK) dalej działa i BIJE bazę: adres podany
// przy starcie jest własnością deployu, więc panel go nie nadpisze. Inaczej
// restart kontenera po cichu cofałby zmianę zrobioną w UI.
//
// Świadomie NIE budujemy tu alertingu. Uptime Kuma robi to lepiej i nie
// zamierzamy go zastępować. To ma być jedno zdanie na Discordzie: „padło" i
// „wróciło", bez eskalacji, harmonogramów ciszy i reguł.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Kolory pasków w embedach Discorda — te same, co stany w panelu.
const (
	kolorDown     = 0xE5484D
	kolorDegraded = 0xE8A33D
	kolorUp       = 0xA4CF36
)

type Zdarzenie struct {
	Klucz  string
	Nazwa  string
	Grupa  string
	URL    string
	Stan   string // up | degraded | down
	Kod    int
	Błąd   string
	MS     int64
	Kiedy  time.Time
}

type stanUsługi struct {
	zgłoszony string // ostatni stan, o którym powiadomiliśmy
	pod       int    // ile z rzędu sprawdzeń nie było „up"
}

type Powiadamiacz struct {
	// muKonf broni webhooka i progu. Czyta je pętla monitora, pisze żądanie
	// HTTP z panelu — bez tego wyścig jest pewny, nie hipotetyczny.
	muKonf  sync.RWMutex
	webhook string
	próg    int

	klient *http.Client

	mu   sync.Mutex
	stan map[string]*stanUsługi
}

// Nowy zwraca powiadamiacz. Pusty webhook = wszystko dalej działa, tylko nic
// nie wychodzi na zewnątrz.
func Nowy(webhook string, próg int) *Powiadamiacz {
	if próg < 1 {
		próg = 2
	}
	return &Powiadamiacz{
		webhook: webhook,
		próg:    próg,
		klient:  &http.Client{Timeout: 10 * time.Second},
		stan:    map[string]*stanUsługi{},
	}
}

func (p *Powiadamiacz) Włączony() bool { return p.Webhook() != "" }

// UstawWebhook podmienia adres w locie. Pusty wyłącza powiadomienia.
func (p *Powiadamiacz) UstawWebhook(adres string) {
	p.muKonf.Lock()
	defer p.muKonf.Unlock()
	p.webhook = adres
}

// Webhook zwraca aktualny adres.
func (p *Powiadamiacz) Webhook() string {
	p.muKonf.RLock()
	defer p.muKonf.RUnlock()
	return p.webhook
}

// UstawPróg podmienia liczbę nieudanych sprawdzeń z rzędu. Poniżej 1 nie ma
// sensu, więc prostuje się do 1.
func (p *Powiadamiacz) UstawPróg(n int) {
	if n < 1 {
		n = 1
	}
	p.muKonf.Lock()
	defer p.muKonf.Unlock()
	p.próg = n
}

// Próg zwraca aktualny próg.
func (p *Powiadamiacz) Próg() int {
	p.muKonf.RLock()
	defer p.muKonf.RUnlock()
	return p.próg
}

// Obserwuj decyduje, czy to zdarzenie zasługuje na wiadomość.
//
// Awaria musi się potwierdzić (domyślnie dwa sprawdzenia z rzędu), żeby jedno
// mrugnięcie sieci nie budziło nikogo w nocy. Powrót zgłaszamy od razu.
func (p *Powiadamiacz) Obserwuj(ctx context.Context, z Zdarzenie) {
	if !p.Włączony() {
		return
	}

	wyślij, ok := p.decyduj(z)
	if !ok || !wyślij {
		return
	}
	// ! `zgłoszony` ustawiamy dopiero po UDANEJ wysyłce. Wcześniej stan był
	// zapisywany przed wysyłką, więc jedna nieudana próba gubiła alert „down"
	// na zawsze: następne sprawdzenia widziały `zgłoszony == down` i milczały.
	if err := p.wyślijZPonowieniem(ctx, z); err != nil {
		slog.Warn("powiadomienie nieudane, spróbuję przy następnym sprawdzeniu",
			"usługa", z.Klucz, "err", err)
		return
	}
	p.mu.Lock()
	if s, jest := p.stan[z.Klucz]; jest {
		s.zgłoszony = z.Stan
	}
	p.mu.Unlock()
}

// decyduj aktualizuje liczniki i mówi, czy trzeba wysłać wiadomość. Drugi
// wynik false = zdarzenie do zignorowania (unknown). Mutex zawsze zwalniany
// przez defer — wcześniej gałąź `default` wracała z zajętym.
func (p *Powiadamiacz) decyduj(z Zdarzenie) (wyślij, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, jest := p.stan[z.Klucz]
	if !jest {
		s = &stanUsługi{zgłoszony: "up"} // zakładamy dobrą wiarę na starcie
		p.stan[z.Klucz] = s
	}
	switch z.Stan {
	case "up":
		s.pod = 0
		return s.zgłoszony != "up", true
	case "down", "degraded":
		s.pod++
		return s.pod >= p.Próg() && s.zgłoszony != z.Stan, true
	default: // unknown — nic nie wiemy, nie zawracamy głowy
		return false, false
	}
}

// opóźnieniaPonowień — przerwy między kolejnymi próbami wysyłki (zmienna, żeby
// test nie czekał sekundami). Pierwsza próba idzie od razu, więc prób jest
// len(opóźnieniaPonowień)+1.
var opóźnieniaPonowień = []time.Duration{500 * time.Millisecond, 2 * time.Second}

func (p *Powiadamiacz) wyślijZPonowieniem(ctx context.Context, z Zdarzenie) error {
	err := p.wyślij(ctx, z)
	for _, d := range opóźnieniaPonowień {
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (anulowano: %v)", err, ctx.Err())
		case <-time.After(d):
		}
		err = p.wyślij(ctx, z)
	}
	return err
}

// Zapomnij czyści stan usługi, której już nie ma w configu.
func (p *Powiadamiacz) Zapomnij(żywe map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.stan {
		if !żywe[k] {
			delete(p.stan, k)
		}
	}
}

func (p *Powiadamiacz) wyślij(ctx context.Context, z Zdarzenie) error {
	tytuł, opis, kolor := treść(z)

	ciało := map[string]any{
		"username": "Shy Garlic",
		"embeds": []map[string]any{{
			"title":       tytuł,
			"description": opis,
			"color":       kolor,
			"url":         z.URL,
			"footer":      map[string]any{"text": z.Grupa},
			"timestamp":   z.Kiedy.UTC().Format(time.RFC3339),
		}},
	}
	b, err := json.Marshal(ciało)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Webhook(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ShyGarlic")

	resp, err := p.klient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s", resp.Status)
	}
	return nil
}

func treść(z Zdarzenie) (tytuł, opis string, kolor int) {
	switch z.Stan {
	case "down":
		return "🔴 " + z.Nazwa + " is down",
			fmt.Sprintf("`%s`\n%s", z.URL, powód(z)), kolorDown
	case "degraded":
		return "🟠 " + z.Nazwa + " answers, but not as expected",
			fmt.Sprintf("`%s`\n%s", z.URL, powód(z)), kolorDegraded
	default:
		return "🟢 " + z.Nazwa + " is back",
			fmt.Sprintf("`%s`\n%d ms", z.URL, z.MS), kolorUp
	}
}

func powód(z Zdarzenie) string {
	switch {
	case z.Błąd != "":
		return z.Błąd
	case z.Kod != 0:
		return fmt.Sprintf("HTTP %d", z.Kod)
	default:
		return "no answer"
	}
}

// Test wysyła wiadomość próbną — żeby dało się sprawdzić konfigurację bez
// czekania, aż coś naprawdę padnie.
func (p *Powiadamiacz) Test(ctx context.Context) error {
	if !p.Włączony() {
		return fmt.Errorf("no webhook configured")
	}
	return p.wyślij(ctx, Zdarzenie{
		Nazwa: "Shy Garlic", Grupa: "test", URL: "http://localhost",
		Stan: "up", MS: 0, Kiedy: time.Now(),
	})
}
