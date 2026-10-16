// Package health sprawdza, czy usługi żyją.
//
// Cała reszta rynku robi to źle i to jest jeden z powodów istnienia Shy Garlic
// (research 2026-08-14):
//   - Homepage uznaje za awarię dopiero kod > 403, więc 401 i 403 świecą zielono;
//   - Homarr dopiero >= 500;
//   - Glance akceptuje wyłącznie 200, więc przekierowanie 301 jest "czerwone",
//     a check odpala się dopiero przy żądaniu HTTP — bez otwartej karty nic nie chodzi;
//   - Homer sprawdza usługi z przeglądarki, więc CORS i mixed-content wygląda jak awaria.
//
// Tutaj: scheduler chodzi po stronie serwera niezależnie od przeglądarki,
// lista akceptowanych kodów jest jawna, a "serwer odpowiedział czymś innym"
// to osobny stan niż "nie da się połączyć".
package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type State string

const (
	StateUp       State = "up"       // odpowiedział jednym z oczekiwanych kodów
	StateDegraded State = "degraded" // serwer żyje, ale kod jest inny niż oczekiwany
	StateDown     State = "down"     // nie da się połączyć / timeout / DNS
	StateUnknown  State = "unknown"  // jeszcze nie sprawdzone
)

// HistoryLen — ile ostatnich wyników trzymamy na wykres.
const HistoryLen = 60

type Result struct {
	State   State         `json:"state"`
	Code    int           `json:"code,omitempty"`
	Latency time.Duration `json:"-"`
	MS      int64         `json:"ms"`
	Err     string        `json:"err,omitempty"`
	At      time.Time     `json:"at"`
}

// Target to jedna sprawdzana usługa.
type Target struct {
	Key      string
	URL      string
	Interval time.Duration
	Timeout  time.Duration
	Expect   []int
	Insecure bool // homelab bywa na samopodpisanym certyfikacie
}

type Monitor struct {
	mu      sync.RWMutex
	targets []Target
	last    map[string]Result
	hist    map[string][]Result
	subs    map[chan struct{}]struct{}

	client   *http.Client
	insecure *http.Client

	// ctl serializuje SetTargets i Close. cancel i wg to stan cyklu życia pętli,
	// a SetTargets woła się równolegle (watch() i zapis z panelu). Bez tego dwa
	// wywołania nadpisywały cancel — jedna grupa pętli zostawała bez możliwości
	// zatrzymania — i robiły wg.Add równolegle z wg.Wait.
	ctl    sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New() *Monitor {
	tr := func(skipVerify bool) *http.Transport {
		return &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: skipVerify},
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 2,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Monitor{
		last:     map[string]Result{},
		hist:     map[string][]Result{},
		subs:     map[chan struct{}]struct{}{},
		client:   &http.Client{Transport: tr(false), CheckRedirect: noRedirect},
		insecure: &http.Client{Transport: tr(true), CheckRedirect: noRedirect},
	}
}

// SetTargets podmienia listę celów i restartuje pętle sprawdzania.
// Wyniki dla celów, które przetrwały podmianę, zostają — po edycji configu
// panel nie ma migać na "unknown".
func (m *Monitor) SetTargets(ctx context.Context, targets []Target) {
	m.ctl.Lock()
	defer m.ctl.Unlock()
	m.stop()

	m.mu.Lock()
	m.targets = targets
	żywe := map[string]bool{}
	for _, t := range targets {
		żywe[t.Key] = true
		if _, ok := m.last[t.Key]; !ok {
			m.last[t.Key] = Result{State: StateUnknown}
		}
	}
	for k := range m.last {
		if !żywe[k] {
			delete(m.last, k)
			delete(m.hist, k)
		}
	}
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	for _, t := range targets {
		m.wg.Add(1)
		go m.loop(ctx, t)
	}
}

// PrzenieśKlucze przepina ostatni wynik i wykres ze starych kluczy na nowe
// (stary → nowy). Wołane przy zmianie nazwy usługi albo przeniesieniu do innej
// grupy, PRZED SetTargets — dzięki temu SetTargets widzi nowy klucz jako
// „znany" i nie kasuje stanu, a karta nie mruga na „unknown".
func (m *Monitor) PrzenieśKlucze(pary map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for stary, nowy := range pary {
		if stary == nowy {
			continue
		}
		if r, ok := m.last[stary]; ok {
			m.last[nowy] = r
			delete(m.last, stary)
		}
		if h, ok := m.hist[stary]; ok {
			m.hist[nowy] = h
			delete(m.hist, stary)
		}
	}
}

// stop zakłada, że wołający trzyma m.ctl.
func (m *Monitor) stop() {
	if m.cancel != nil {
		m.cancel()
		m.wg.Wait()
		m.cancel = nil
	}
}

func (m *Monitor) Close() {
	m.ctl.Lock()
	defer m.ctl.Unlock()
	m.stop()
}

func (m *Monitor) loop(ctx context.Context, t Target) {
	defer m.wg.Done()

	// rozrzut startów, żeby 30 usług nie ruszyło w tej samej milisekundzie
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	tick := time.NewTicker(t.Interval)
	defer tick.Stop()
	for {
		m.record(t.Key, Check(ctx, m.clientFor(t), t))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *Monitor) clientFor(t Target) *http.Client {
	if t.Insecure {
		return m.insecure
	}
	return m.client
}

func (m *Monitor) record(key string, r Result) {
	m.mu.Lock()
	m.last[key] = r
	h := append(m.hist[key], r)
	if len(h) > HistoryLen {
		h = h[len(h)-HistoryLen:]
	}
	m.hist[key] = h
	subs := make([]chan struct{}, 0, len(m.subs))
	for c := range m.subs {
		subs = append(subs, c)
	}
	m.mu.Unlock()

	for _, c := range subs {
		select {
		case c <- struct{}{}:
		default: // odbiorca nie nadąża — kolejny tick i tak przyniesie pełny stan
		}
	}
}

// SprawdźTeraz odpala sprawdzenie poza kolejnością — dla przycisku „sprawdź
// teraz" w UI. Wynik trafia do historii tak samo jak sprawdzenia z harmonogramu.
func (m *Monitor) SprawdźTeraz(ctx context.Context, klucz string) (Result, error) {
	m.mu.RLock()
	var cel Target
	var znaleziony bool
	for _, t := range m.targets {
		if t.Key == klucz {
			cel, znaleziony = t, true
			break
		}
	}
	m.mu.RUnlock()
	if !znaleziony {
		return Result{}, errors.New("no such service")
	}
	r := Check(ctx, m.clientFor(cel), cel)
	m.record(klucz, r)
	return r, nil
}

func (m *Monitor) Snapshot() map[string]Result {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]Result, len(m.last))
	for k, v := range m.last {
		out[k] = v
	}
	return out
}

func (m *Monitor) History(key string) []Result {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.hist[key])
}

// Subscribe zwraca kanał budzony przy każdej zmianie wyniku oraz funkcję odpięcia.
func (m *Monitor) Subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	m.mu.Lock()
	m.subs[c] = struct{}{}
	m.mu.Unlock()
	return c, func() {
		m.mu.Lock()
		delete(m.subs, c)
		m.mu.Unlock()
	}
}

// Check wykonuje pojedyncze sprawdzenie. Wydzielone i bezstanowe, żeby dało się
// je testować bez schedulera.
func Check(ctx context.Context, client *http.Client, t Target) Result {
	start := time.Now()
	res := Result{At: start}

	u, err := url.Parse(t.URL)
	if err != nil || u.Host == "" {
		res.State, res.Err = StateDown, "invalid url"
		return res
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch u.Scheme {
	case "http", "https":
		res = checkHTTP(ctx, client, t, res)
	case "tcp":
		res = checkTCP(ctx, u.Host, res)
	default:
		res.State, res.Err = StateDown, "unsupported scheme "+u.Scheme
	}
	res.Latency = time.Since(start)
	res.MS = res.Latency.Milliseconds()
	return res
}

func checkHTTP(ctx context.Context, client *http.Client, t Target, res Result) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		res.State, res.Err = StateDown, err.Error()
		return res
	}
	req.Header.Set("User-Agent", "ShyGarlic/0.1 (health check)")

	resp, err := client.Do(req)
	if err != nil {
		res.State = StateDown
		res.Err = krótkiBłąd(err)
		return res
	}
	defer resp.Body.Close()

	res.Code = resp.StatusCode
	expect := t.Expect
	if len(expect) == 0 {
		expect = []int{200, 301, 302, 401, 403}
	}
	if slices.Contains(expect, resp.StatusCode) {
		res.State = StateUp
		return res
	}
	// ! Serwer odpowiedział, tylko nie tym, czego oczekujemy. To NIE jest to samo
	// co "nie da się połączyć" — użytkownik ma widzieć różnicę.
	res.State = StateDegraded
	res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	return res
}

func checkTCP(ctx context.Context, host string, res Result) Result {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		res.State, res.Err = StateDown, krótkiBłąd(err)
		return res
	}
	_ = conn.Close()
	res.State = StateUp
	return res
}

// krótkiBłąd zamienia gadatliwe błędy net/http na coś, co mieści się w kafelku.
func krótkiBłąd(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "no such host"):
		return "DNS: no such host"
	case strings.Contains(s, "certificate"):
		return "TLS certificate"
	case strings.Contains(s, "context deadline exceeded"):
		return "timeout"
	}
	if i := strings.LastIndex(s, ": "); i > 0 && len(s)-i < 60 {
		return s[i+2:]
	}
	return s
}
