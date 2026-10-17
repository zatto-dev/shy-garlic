// Package health checks whether services are alive.
//
// The rest of the market does this badly, and that is one of the reasons Shy Garlic
// exists (research 2026-08-14):
//   - Homepage treats only a code > 403 as a failure, so 401 and 403 glow green;
//   - Homarr only >= 500;
//   - Glance accepts only 200, so a 301 redirect is "red", and the check fires
//     only on an HTTP request — with no open tab nothing runs;
//   - Homer checks services from the browser, so CORS and mixed-content look like an outage.
//
// Here: the scheduler runs server-side, independently of the browser,
// the list of accepted codes is explicit, and "the server answered with something
// else" is a separate state from "cannot connect".
package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/netguard"
)

type State string

const (
	StateUp       State = "up"       // answered with one of the expected codes
	StateDegraded State = "degraded" // the server is alive, but the code differs from the expected one
	StateDown     State = "down"     // cannot connect / timeout / DNS
	StateUnknown  State = "unknown"  // not checked yet
)

// HistoryLen — how many of the latest results we keep for the chart.
const HistoryLen = 60

type Result struct {
	State   State         `json:"state"`
	Code    int           `json:"code,omitempty"`
	Latency time.Duration `json:"-"`
	MS      int64         `json:"ms"`
	Err     string        `json:"err,omitempty"`
	At      time.Time     `json:"at"`
}

// Target is a single service being checked.
type Target struct {
	Key      string
	URL      string
	Interval time.Duration
	Timeout  time.Duration
	Expect   []int
	Insecure bool // a homelab is often on a self-signed certificate

	// Content check (SHYG-48) — applies to http(s) only.
	Path        string // path (+ query) instead of the path from URL; always on the same host
	Contains    string // text that MUST be in the response body
	NotContains string // text that must NOT be in the body
}

// ContentPrefix — the beginning of Result.Err when the server answered with an
// acceptable code but the content failed the check. The UI uses it to recognize
// this kind of "degraded" (the HTTP code is good then, so "HTTP 200" would be misleading).
const ContentPrefix = "content: "

// maxBody — how many bytes of the response we read during the content check. A page
// larger than 1 MiB is not a health endpoint anyway, and without a limit a single
// tile could pull gigabytes every 30 s.
const maxBody = 1 << 20

type Monitor struct {
	mu      sync.RWMutex
	targets []Target
	last    map[string]Result
	hist    map[string][]Result
	subs    map[chan struct{}]struct{}

	client   *http.Client
	insecure *http.Client

	// ctl serializes SetTargets and Close. cancel and wg are the loops' lifecycle
	// state, and SetTargets is called concurrently (watch() and a save from the panel).
	// Without it two calls overwrote cancel — one group of loops was left with no way
	// to be stopped — and did wg.Add concurrently with wg.Wait.
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
			DialContext:         netguard.Dialer(5 * time.Second).DialContext,
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

// SetTargets replaces the target list and restarts the check loops.
// Results for targets that survived the replacement stay — after a config edit
// the panel must not flicker to "unknown".
func (m *Monitor) SetTargets(ctx context.Context, targets []Target) {
	m.ctl.Lock()
	defer m.ctl.Unlock()
	m.stop()

	m.mu.Lock()
	m.targets = targets
	alive := map[string]bool{}
	for _, t := range targets {
		alive[t.Key] = true
		if _, ok := m.last[t.Key]; !ok {
			m.last[t.Key] = Result{State: StateUnknown}
		}
	}
	for k := range m.last {
		if !alive[k] {
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

// MoveKeys re-points the last result and chart from old keys to new ones
// (old → new). Called when a service is renamed or moved to another group,
// BEFORE SetTargets — so SetTargets sees the new key as "known" and does not
// delete the state, and the card does not flicker to "unknown".
func (m *Monitor) MoveKeys(pairs map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for oldKey, newKey := range pairs {
		if oldKey == newKey {
			continue
		}
		if r, ok := m.last[oldKey]; ok {
			m.last[newKey] = r
			delete(m.last, oldKey)
		}
		if h, ok := m.hist[oldKey]; ok {
			m.hist[newKey] = h
			delete(m.hist, oldKey)
		}
	}
}

// stop assumes the caller holds m.ctl.
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

	// spread out the starts so 30 services do not fire in the same millisecond
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	// ! A ticker panics on a non-positive period, and a panic in this goroutine
	// takes the whole process down. Config validation already keeps such values
	// out; this is the last line, for Targets built any other way.
	tick := time.NewTicker(max(t.Interval, minInterval))
	defer tick.Stop()
	for {
		m.record(t.Key, m.safeCheck(ctx, t))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// minInterval — the floor for the check period, whatever the Target says.
// A variable only so tests can run fast loops.
var minInterval = time.Second

// safeCheck runs one check and turns a panic into a failed result with a log
// entry, so one broken target cannot stop its own loop or the process.
func (m *Monitor) safeCheck(ctx context.Context, t Target) (r Result) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("health check panicked", "key", t.Key, "panic", p)
			r = Result{State: StateDown, Err: "internal error", At: time.Now()}
		}
	}()
	return Check(ctx, m.clientFor(t), t)
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
		default: // the receiver cannot keep up — the next tick will bring the full state anyway
		}
	}
}

// CheckNow fires a check out of order — for the "check now" button in the UI.
// The result goes to history the same way as scheduled checks.
func (m *Monitor) CheckNow(ctx context.Context, key string) (Result, error) {
	m.mu.RLock()
	var target Target
	var found bool
	for _, t := range m.targets {
		if t.Key == key {
			target, found = t, true
			break
		}
	}
	m.mu.RUnlock()
	if !found {
		return Result{}, errors.New("no such service")
	}
	r := Check(ctx, m.clientFor(target), target)
	m.record(key, r)
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

// Subscribe returns a channel woken on every result change, and an unsubscribe function.
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

// Check performs a single check. Extracted and stateless so it can be
// tested without the scheduler.
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
	res.Err = userinfoInText.ReplaceAllString(res.Err, "$1")
	return res
}

// userinfoInText finds user[:password]@ after a scheme in free text. Errors from
// net/http quote the requested URL, and Result.Err ends up in /api/status, in
// the history and in notifications — the check itself keeps the credentials, its
// output must not.
var userinfoInText = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s"'?#]*@`)

// checkURL builds the address we actually query: the service URL, and when
// Path is set — with its path and query replaced. Scheme, host, port and login
// data stay from URL, so Path cannot lead the check to a different server.
func (t Target) checkURL() (string, error) {
	if t.Path == "" {
		return t.URL, nil
	}
	if !strings.HasPrefix(t.Path, "/") || strings.HasPrefix(t.Path, "//") {
		return "", errors.New("invalid check.path: it has to start with a single /")
	}
	ref, err := url.Parse(t.Path)
	if err != nil || ref.Scheme != "" || ref.Host != "" {
		return "", errors.New("invalid check.path")
	}
	u, err := url.Parse(t.URL)
	if err != nil {
		return "", err
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = ref.Path, ref.RawPath, ref.RawQuery, ""
	return u.String(), nil
}

func checkHTTP(ctx context.Context, client *http.Client, t Target, res Result) Result {
	address, err := t.checkURL()
	if err != nil {
		res.State, res.Err = StateDown, err.Error()
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		res.State, res.Err = StateDown, err.Error()
		return res
	}
	req.Header.Set("User-Agent", "ShyGarlic/0.1 (health check)")

	resp, err := client.Do(req)
	if err != nil {
		res.State = StateDown
		res.Err = shortError(err)
		return res
	}
	defer resp.Body.Close()

	res.Code = resp.StatusCode
	expect := t.Expect
	if len(expect) == 0 {
		expect = []int{200, 301, 302, 401, 403}
	}
	if slices.Contains(expect, resp.StatusCode) {
		if t.Contains == "" && t.NotContains == "" {
			res.State = StateUp
			return res
		}
		return checkContent(resp.Body, t, res)
	}
	// ! The server answered, just not with what we expect. This is NOT the same as
	// "cannot connect" — the user should see the difference.
	res.State = StateDegraded
	res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
	return res
}

// checkContent reads the beginning of the body (up to maxBody) and compares it with the rules.
//
// ! A mismatch is degraded, not down: the server is alive and answers with an
// accepted code — only the content is wrong (nginx's default page instead of the
// app, a maintenance message). A bad HTTP code is treated the same way.
// A failed READ of the body (timeout, dropped connection) is down, though.
func checkContent(r io.Reader, t Target, res Result) Result {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		res.State, res.Err = StateDown, shortError(err)
		return res
	}
	wasTruncated := len(b) > maxBody
	if wasTruncated {
		b = b[:maxBody]
	}
	body := string(b)

	switch {
	case t.Contains != "" && !strings.Contains(body, t.Contains):
		res.State = StateDegraded
		res.Err = ContentPrefix + `missing "` + shorten(t.Contains) + `"`
		if wasTruncated {
			res.Err += " (first 1 MiB checked)"
		}
	case t.NotContains != "" && strings.Contains(body, t.NotContains):
		res.State = StateDegraded
		res.Err = ContentPrefix + `found "` + shorten(t.NotContains) + `"`
	default:
		res.State = StateUp
	}
	return res
}

// shorten trims the searched text to a size that fits in a tile.
func shorten(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

func checkTCP(ctx context.Context, host string, res Result) Result {
	conn, err := netguard.Dialer(0).DialContext(ctx, "tcp", host)
	if err != nil {
		res.State, res.Err = StateDown, shortError(err)
		return res
	}
	_ = conn.Close()
	res.State = StateUp
	return res
}

// shortError turns chatty net/http errors into something that fits in a tile.
func shortError(err error) string {
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
