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
// Format wiadomości (discord, slack, ntfy, gotify, telegram, generic) jest
// zapisany obok adresu, w SQLite; SHYG_WEBHOOK_FORMAT ma wobec bazy tę samą
// pierwszeństwo co SHYG_WEBHOOK.
//
// Wysyłka idzie w tle, po jednej kolejce na usługę (kolejność zdarzeń jednej
// usługi jest zachowana), więc wolny albo martwy webhook nie wstrzymuje
// zapisu historii sprawdzeń.
//
// Świadomie NIE budujemy tu alertingu. Uptime Kuma robi to lepiej i nie
// zamierzamy go zastępować. To ma być jedno zdanie na kanale: „padło" i
// „wróciło", bez eskalacji, harmonogramów ciszy i reguł.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Formaty wiadomości (SHYG-50). Adres webhooka i format mieszkają obok siebie w
// SQLite; format mówi tylko, jak ubrać wiadomość i dokąd ją zapakować:
//
//	discord   embed z paskiem koloru (domyślny, jak dotąd)
//	slack     {"text": …} — także Mattermost i Rocket.Chat
//	ntfy      zwykły tekst + nagłówki Title/Priority/Tags/Click
//	gotify    {"title","message","priority"} na POST /message?token=…
//	telegram  {"text": …} na POST …/sendMessage?chat_id=… (token bota w ścieżce)
//	generic   stabilny JSON, patrz wiadomośćGeneric
type Format string

const (
	FormatDiscord  Format = "discord"
	FormatSlack    Format = "slack"
	FormatNtfy     Format = "ntfy"
	FormatGotify   Format = "gotify"
	FormatTelegram Format = "telegram"
	FormatGeneric  Format = "generic"
)

// Formaty — kolejność z listy rozwijanej w UI.
var Formaty = []Format{FormatDiscord, FormatSlack, FormatNtfy, FormatGotify, FormatTelegram, FormatGeneric}

func PoprawnyFormat(s string) bool {
	for _, f := range Formaty {
		if string(f) == s {
			return true
		}
	}
	return false
}

var (
	ErrAdresTelegram = errors.New("telegram: the address has to look like https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<ID>")
	ErrAdresGotify   = errors.New("gotify: the address has to look like https://<host>/message?token=<APP_TOKEN>")
)

// SprawdźAdres pilnuje kształtu adresu, którego wymaga dany format — żeby
// literówka wyszła przy zapisie, a nie dopiero jako 404 o trzeciej w nocy.
// Schematu nie sprawdza (to robi wołający), tylko ścieżkę i parametry.
func SprawdźAdres(f Format, adres string) error {
	if adres == "" {
		return nil
	}
	u, err := url.Parse(adres)
	if err != nil {
		return err
	}
	switch f {
	case FormatTelegram:
		if !strings.HasSuffix(u.Path, "/sendMessage") || u.Query().Get("chat_id") == "" {
			return ErrAdresTelegram
		}
	case FormatGotify:
		if !strings.HasSuffix(u.Path, "/message") || u.Query().Get("token") == "" {
			return ErrAdresGotify
		}
	}
	return nil
}

// Kolory pasków w embedach Discorda — te same, co stany w panelu.
const (
	kolorDown     = 0xE5484D
	kolorDegraded = 0xE8A33D
	kolorUp       = 0xA4CF36
)

type Zdarzenie struct {
	Klucz string
	Nazwa string
	Grupa string
	URL   string
	Stan  string // up | degraded | down
	Kod   int
	Błąd  string
	MS    int64
	Kiedy time.Time

	// Poprzedni — ostatni stan, o którym powiadomiliśmy (up | degraded | down).
	// Wypełnia Obserwuj; do formatu generic.
	Poprzedni string
	// Test — wiadomość próbna z przycisku w ustawieniach.
	Test bool
}

// rodzaj to nazwa zdarzenia w formacie generic: down | degraded | recovered | test.
func (z Zdarzenie) rodzaj() string {
	switch {
	case z.Test:
		return "test"
	case z.Stan == "up":
		return "recovered"
	default:
		return z.Stan
	}
}

type stanUsługi struct {
	zgłoszony string // ostatni stan, o którym powiadomiliśmy
	pod       int    // ile z rzędu sprawdzeń nie było „up"
	// oczekuje — stany, których wiadomość leży w kolejce albo jest w drodze.
	// Bez tego kolejne sprawdzenia (co kilkanaście sekund), gdy wysyłka jeszcze
	// trwa, ustawiałyby w kolejce ten sam alert w kółko.
	oczekuje map[string]bool
}

// zadanie — jedna wiadomość do wysłania. Trzyma WSKAŹNIK na stan usługi, a nie
// jej klucz: Przenieś przepina ten sam wskaźnik pod nowy klucz, więc wynik
// wysyłki zaczętej przed zmianą nazwy ląduje we właściwym miejscu.
type zadanie struct {
	z Zdarzenie
	s *stanUsługi
}

// kolejkaUsługi — wiadomości jednej usługi, wysyłane po kolei przez jeden
// goroutine, żeby „padło" nigdy nie wyprzedziło „wróciło".
type kolejkaUsługi struct {
	zdarzenia []zadanie
	działa    bool
}

const (
	// maksKolejkiUsługi — twarda granica; przy `oczekuje` kolejka ma w praktyce
	// najwyżej dwie pozycje, to bezpiecznik na wypadek błędu.
	maksKolejkiUsługi = 8
	// równoległeWysyłki — ile wiadomości naraz może być w drodze. Awaria
	// sieci kładzie wszystkie usługi w jednej sekundzie, a Discord po
	// kilku wiadomościach odpowiada 429.
	równoległeWysyłki = 3
)

type Powiadamiacz struct {
	// muKonf broni webhooka, formatu i progu. Czyta je pętla monitora, pisze
	// żądanie HTTP z panelu — bez tego wyścig jest pewny, nie hipotetyczny.
	muKonf  sync.RWMutex
	webhook string
	format  Format
	próg    int

	klient *http.Client

	mu      sync.Mutex
	stan    map[string]*stanUsługi
	kolejki map[string]*kolejkaUsługi

	sem chan struct{}
	wg  sync.WaitGroup
}

// Nowy zwraca powiadamiacz. Pusty webhook = wszystko dalej działa, tylko nic
// nie wychodzi na zewnątrz.
func Nowy(webhook string, próg int) *Powiadamiacz {
	if próg < 1 {
		próg = 2
	}
	return &Powiadamiacz{
		webhook: webhook,
		format:  FormatDiscord,
		próg:    próg,
		klient:  &http.Client{Timeout: 10 * time.Second},
		stan:    map[string]*stanUsługi{},
		kolejki: map[string]*kolejkaUsługi{},
		sem:     make(chan struct{}, równoległeWysyłki),
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

// UstawFormat podmienia format wiadomości. Nieznany wraca do domyślnego
// (discord) — format z bazy, którego ta wersja nie zna, nie może wyłączyć alertów.
func (p *Powiadamiacz) UstawFormat(f Format) {
	if !PoprawnyFormat(string(f)) {
		f = FormatDiscord
	}
	p.muKonf.Lock()
	defer p.muKonf.Unlock()
	p.format = f
}

// Format zwraca aktualny format.
func (p *Powiadamiacz) Format() Format {
	p.muKonf.RLock()
	defer p.muKonf.RUnlock()
	return p.format
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

// Obserwuj decyduje, czy to zdarzenie zasługuje na wiadomość, i jeśli tak,
// oddaje ją do wysłania w tle — wołający nie czeka na sieć.
//
// Awaria musi się potwierdzić (domyślnie dwa sprawdzenia z rzędu), żeby jedno
// mrugnięcie sieci nie budziło nikogo w nocy. Powrót zgłaszamy od razu.
//
// ! Stan „zgłoszony" ustawia dopiero wysyłka, i tylko udana (patrz obsłuż).
// Wcześniej stan był zapisywany przed wysyłką, więc jedna nieudana próba
// gubiła alert „down" na zawsze. Przy wysyłce w tle tę samą gwarancję trzyma
// `oczekuje`: nieudana wysyłka je zdejmuje, a następne sprawdzenie ponawia.
func (p *Powiadamiacz) Obserwuj(ctx context.Context, z Zdarzenie) {
	if !p.Włączony() {
		return
	}
	zad, ok := p.decyduj(z)
	if !ok {
		return
	}
	if !p.dołącz(ctx, zad) {
		slog.Warn("kolejka powiadomień usługi pełna, spróbuję przy następnym sprawdzeniu", "usługa", z.Klucz)
		p.mu.Lock()
		delete(zad.s.oczekuje, z.Stan)
		p.mu.Unlock()
	}
}

// Poczekaj blokuje, aż wszystkie wiadomości w kolejkach zostaną wysłane albo
// porzucone — dla testów i zamykania.
func (p *Powiadamiacz) Poczekaj() { p.wg.Wait() }

// decyduj aktualizuje liczniki i mówi, czy trzeba wysłać wiadomość. Drugi
// wynik false = nic do wysłania (unknown, ten sam stan, wiadomość już leci).
// Mutex zawsze zwalniany przez defer — wcześniej gałąź `default` wracała z
// zajętym.
func (p *Powiadamiacz) decyduj(z Zdarzenie) (zadanie, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, jest := p.stan[z.Klucz]
	if !jest {
		s = &stanUsługi{zgłoszony: "up"} // zakładamy dobrą wiarę na starcie
		p.stan[z.Klucz] = s
	}
	var wyślij bool
	switch z.Stan {
	case "up":
		s.pod = 0
		wyślij = s.zgłoszony != "up"
	case "down", "degraded":
		s.pod++
		wyślij = s.pod >= p.Próg() && s.zgłoszony != z.Stan
	default: // unknown — nic nie wiemy, nie zawracamy głowy
		return zadanie{}, false
	}
	if !wyślij || s.oczekuje[z.Stan] {
		return zadanie{}, false
	}
	if s.oczekuje == nil {
		s.oczekuje = map[string]bool{}
	}
	s.oczekuje[z.Stan] = true
	z.Poprzedni = s.zgłoszony
	return zadanie{z: z, s: s}, true
}

// dołącz kładzie zadanie w kolejce usługi i w razie potrzeby budzi jej
// goroutine. false = kolejka pełna.
func (p *Powiadamiacz) dołącz(ctx context.Context, zad zadanie) bool {
	p.mu.Lock()
	k := p.kolejki[zad.z.Klucz]
	if k == nil {
		k = &kolejkaUsługi{}
		p.kolejki[zad.z.Klucz] = k
	}
	if len(k.zdarzenia) >= maksKolejkiUsługi {
		p.mu.Unlock()
		return false
	}
	k.zdarzenia = append(k.zdarzenia, zad)
	start := !k.działa
	k.działa = true
	if start {
		p.wg.Add(1)
	}
	p.mu.Unlock()
	if start {
		go p.obsłuż(ctx, zad.z.Klucz)
	}
	return true
}

// obsłuż opróżnia kolejkę jednej usługi po kolei i kończy się, gdy jest pusta.
func (p *Powiadamiacz) obsłuż(ctx context.Context, klucz string) {
	defer p.wg.Done()
	for {
		p.mu.Lock()
		k := p.kolejki[klucz]
		if k == nil || len(k.zdarzenia) == 0 {
			if k != nil {
				k.działa = false
				delete(p.kolejki, klucz)
			}
			p.mu.Unlock()
			return
		}
		zad := k.zdarzenia[0]
		k.zdarzenia = k.zdarzenia[1:]
		p.mu.Unlock()

		err := p.wyślijZPonowieniem(ctx, zad.z)

		p.mu.Lock()
		delete(zad.s.oczekuje, zad.z.Stan)
		if err == nil {
			zad.s.zgłoszony = zad.z.Stan
		}
		p.mu.Unlock()
		if err != nil {
			slog.Warn("powiadomienie nieudane, spróbuję przy następnym sprawdzeniu",
				"usługa", zad.z.Klucz, "err", err)
		}
	}
}

// opóźnieniaPonowień — przerwy między kolejnymi próbami wysyłki (zmienna, żeby
// test nie czekał sekundami). Pierwsza próba idzie od razu, więc prób jest
// len(opóźnieniaPonowień)+1.
var opóźnieniaPonowień = []time.Duration{500 * time.Millisecond, 2 * time.Second}

func (p *Powiadamiacz) wyślijZPonowieniem(ctx context.Context, z Zdarzenie) error {
	err := p.wyślijPodLimitem(ctx, z)
	for _, d := range opóźnieniaPonowień {
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (anulowano: %v)", err, ctx.Err())
		case <-time.After(d):
		}
		err = p.wyślijPodLimitem(ctx, z)
	}
	return err
}

// wyślijPodLimitem zajmuje jedno z równoległeWysyłki miejsc na czas samego
// żądania — nie na czas oczekiwania między ponowieniami.
func (p *Powiadamiacz) wyślijPodLimitem(ctx context.Context, z Zdarzenie) error {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.wyślij(ctx, z)
}

// Przenieś przepina stan usługi pod nowy klucz (stary → nowy), gdy usługę
// przemianowano albo przeniesiono do innej grupy. Bez tego Zapomnij kasowało
// licznik i „zgłoszony", więc trwająca awaria alarmowała od nowa, a powrót
// nigdy nie był zgłoszony.
func (p *Powiadamiacz) Przenieś(pary map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for stary, nowy := range pary {
		if stary == nowy {
			continue
		}
		if s, ok := p.stan[stary]; ok {
			p.stan[nowy] = s
			delete(p.stan, stary)
		}
	}
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

// ── budowanie żądania ────────────────────────────────────────────────────────

func (p *Powiadamiacz) wyślij(ctx context.Context, z Zdarzenie) error {
	req, err := zbudujŻądanie(ctx, p.Format(), p.Webhook(), z)
	if err != nil {
		return odsłoń(err)
	}
	req.Header.Set("User-Agent", "ShyGarlic")

	resp, err := p.klient.Do(req)
	if err != nil {
		return odsłoń(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s", resp.Status)
	}
	return nil
}

// odsłoń zdejmuje z błędu adres. *url.Error z net/http niesie cały URL, a w nim
// token bota Telegrama, token Gotify albo ścieżkę webhooka Discorda — błąd
// trafia i do logu, i do odpowiedzi API przy „wyślij próbę".
func odsłoń(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("webhook: %s: %w", ue.Op, ue.Err)
	}
	return err
}

func zbudujŻądanie(ctx context.Context, f Format, adres string, z Zdarzenie) (*http.Request, error) {
	tytuł, treść := tekstWiadomości(z)
	switch f {
	case FormatSlack:
		return jsonPOST(ctx, adres, map[string]any{"text": tytuł + "\n" + treść})
	case FormatNtfy:
		return ntfyPOST(ctx, adres, z, tytuł, treść)
	case FormatGotify:
		ciało := map[string]any{"title": tytuł, "message": treść, "priority": priorytetGotify(z)}
		if klikalny(z.URL) && !z.Test {
			ciało["extras"] = map[string]any{
				"client::notification": map[string]any{"click": map[string]any{"url": z.URL}},
			}
		}
		return jsonPOST(ctx, adres, ciało)
	case FormatTelegram:
		// chat_id jedzie w adresie (?chat_id=…), tak jak token bota w ścieżce;
		// bez parse_mode, żeby _ i * w nazwie usługi nie psuły Markdowna
		return jsonPOST(ctx, adres, map[string]any{"text": tytuł + "\n" + treść, "disable_web_page_preview": true})
	case FormatGeneric:
		return jsonPOST(ctx, adres, wiadomośćGeneric(z))
	default:
		return jsonPOST(ctx, adres, wiadomośćDiscord(z))
	}
}

func jsonPOST(ctx context.Context, adres string, ciało any) (*http.Request, error) {
	b, err := json.Marshal(ciało)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adres, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func wiadomośćDiscord(z Zdarzenie) map[string]any {
	tytuł, opis := tekstDiscord(z)
	kolor := kolorUp
	switch z.Stan {
	case "down":
		kolor = kolorDown
	case "degraded":
		kolor = kolorDegraded
	}
	return map[string]any{
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
}

// wiadomośćGeneric — KONTRAKT dla cudzych integracji; nazw pól się nie zmienia.
//
//	event     down | degraded | recovered | test
//	service   nazwa usługi
//	group     nazwa grupy
//	state     nowy stan: up | degraded | down
//	previous  ostatni zgłoszony stan: up | degraded | down
//	reason    powód (np. "connection refused", "HTTP 502"); pusty dla recovered
//	at        czas sprawdzenia, RFC 3339 UTC
//	url       adres usługi
func wiadomośćGeneric(z Zdarzenie) map[string]any {
	powodem := ""
	if z.Stan != "up" {
		powodem = powód(z)
	}
	if z.Test {
		powodem = "test message"
	}
	return map[string]any{
		"event": z.rodzaj(), "service": z.Nazwa, "group": z.Grupa,
		"state": z.Stan, "previous": z.Poprzedni, "reason": powodem,
		"at": z.Kiedy.UTC().Format(time.RFC3339), "url": z.URL,
	}
}

func ntfyPOST(ctx context.Context, adres string, z Zdarzenie, tytuł, treść string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adres, strings.NewReader(treść))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", nagłówekTekstowy(tytuł))
	priorytet, tag := "default", "white_check_mark"
	switch z.Stan {
	case "down":
		priorytet, tag = "high", "rotating_light"
	case "degraded":
		tag = "warning"
	}
	if z.Test {
		tag = "bell"
	}
	req.Header.Set("Priority", priorytet)
	req.Header.Set("Tags", tag)
	if klikalny(z.URL) && !z.Test {
		req.Header.Set("Click", z.URL)
	}
	return req, nil
}

// nagłówekTekstowy przygotowuje tekst do nagłówka HTTP: bez łamania linii
// (nazwa usługi z pliku nie wstrzyknie drugiego nagłówka) i z kodowaniem
// RFC 2047 dla znaków spoza ASCII, które ntfy odczytuje.
func nagłówekTekstowy(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return mime.QEncoding.Encode("utf-8", s)
}

func klikalny(adres string) bool {
	u, err := url.Parse(adres)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		!strings.ContainsAny(adres, "\r\n")
}

func priorytetGotify(z Zdarzenie) int {
	switch z.Stan {
	case "down":
		return 8
	case "degraded":
		return 5
	}
	return 3
}

// tekstWiadomości — tytuł i treść bez formatowania, wspólne dla wszystkich
// formatów poza Discordem (ten ma emoji w tytule i kod w opisie).
func tekstWiadomości(z Zdarzenie) (tytuł, treść string) {
	if z.Test {
		return "Shy Garlic: test notification", "If you can read this, notifications work."
	}
	switch z.Stan {
	case "down":
		return z.Nazwa + " is down", z.URL + "\n" + powód(z)
	case "degraded":
		return z.Nazwa + " answers, but not as expected", z.URL + "\n" + powód(z)
	default:
		return z.Nazwa + " is back", fmt.Sprintf("%s\n%d ms", z.URL, z.MS)
	}
}

func tekstDiscord(z Zdarzenie) (tytuł, opis string) {
	if z.Test {
		return "🔔 Shy Garlic: test notification", "If you can read this, notifications work."
	}
	switch z.Stan {
	case "down":
		return "🔴 " + z.Nazwa + " is down", fmt.Sprintf("`%s`\n%s", z.URL, powód(z))
	case "degraded":
		return "🟠 " + z.Nazwa + " answers, but not as expected", fmt.Sprintf("`%s`\n%s", z.URL, powód(z))
	default:
		return "🟢 " + z.Nazwa + " is back", fmt.Sprintf("`%s`\n%d ms", z.URL, z.MS)
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
// czekania, aż coś naprawdę padnie. Idzie od razu (bez kolejki i ponowień), bo
// człowiek czeka na odpowiedź i chce zobaczyć prawdziwy błąd.
func (p *Powiadamiacz) Test(ctx context.Context) error {
	if !p.Włączony() {
		return fmt.Errorf("no webhook configured")
	}
	return p.wyślij(ctx, Zdarzenie{
		Nazwa: "Shy Garlic", Grupa: "test", URL: "http://localhost",
		Stan: "up", Poprzedni: "up", Kiedy: time.Now(), Test: true,
	})
}
