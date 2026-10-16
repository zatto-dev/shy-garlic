package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/store"
)

// ── Prometheus text format parser — only as much as the tests need ───────────

type sample struct {
	name       string
	labels     map[string]string
	value      float64
	lineNumber int
}

type metricsOutput struct {
	samples []sample
	kind    map[string]string
	help    map[string]string
}

func (w metricsOutput) value(t *testing.T, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	for _, p := range w.samples {
		if p.name != name || len(p.labels) != len(labels) {
			continue
		}
		ok := true
		for k, v := range labels {
			if p.labels[k] != v {
				ok = false
			}
		}
		if ok {
			return p.value, true
		}
	}
	return 0, false
}

func (w metricsOutput) count(name string) int {
	n := 0
	for _, p := range w.samples {
		if p.name == name {
			n++
		}
	}
	return n
}

// parseMetrics takes the text apart and ALONG THE WAY guards the syntax: every sample must
// have TYPE and HELP of its family before it, and each TYPE/HELP must occur once.
func parseMetrics(t *testing.T, text string) metricsOutput {
	t.Helper()
	w := metricsOutput{kind: map[string]string{}, help: map[string]string{}}
	if !strings.HasSuffix(text, "\n") {
		t.Error("the output must end with a newline")
	}
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		num := i + 1
		switch {
		case line == "":
			t.Errorf("line %d: empty line in the middle of the output", num)
		case strings.HasPrefix(line, "# HELP "):
			c := strings.SplitN(strings.TrimPrefix(line, "# HELP "), " ", 2)
			if len(c) != 2 || c[1] == "" {
				t.Errorf("line %d: HELP without text: %q", num, line)
				continue
			}
			if _, two := w.help[c[0]]; two {
				t.Errorf("line %d: second HELP for %s", num, c[0])
			}
			w.help[c[0]] = c[1]
		case strings.HasPrefix(line, "# TYPE "):
			c := strings.Fields(strings.TrimPrefix(line, "# TYPE "))
			if len(c) != 2 || (c[1] != "gauge" && c[1] != "counter") {
				t.Errorf("line %d: bad TYPE: %q", num, line)
				continue
			}
			if _, two := w.kind[c[0]]; two {
				t.Errorf("line %d: second TYPE for %s", num, c[0])
			}
			w.kind[c[0]] = c[1]
		case strings.HasPrefix(line, "#"):
			t.Errorf("line %d: unknown comment %q", num, line)
		default:
			p, err := parseSample(line)
			if err != nil {
				t.Errorf("line %d: %v: %q", num, err, line)
				continue
			}
			p.lineNumber = num
			if w.kind[p.name] == "" || w.help[p.name] == "" {
				t.Errorf("line %d: sample %s without a preceding HELP/TYPE", num, p.name)
			}
			w.samples = append(w.samples, p)
		}
	}
	// samples of one family must lie together, no "interleaved" families
	last, seen := "", map[string]bool{}
	for _, p := range w.samples {
		if p.name != last {
			if seen[p.name] {
				t.Errorf("family %s split by others", p.name)
			}
			seen[p.name] = true
			last = p.name
		}
	}
	// no duplicates (same name and labels) — Prometheus rejects such a scrape
	unique := map[string]bool{}
	for _, p := range w.samples {
		k := p.name + fmt.Sprint(p.labels)
		if unique[k] {
			t.Errorf("duplicate sample %s%v", p.name, p.labels)
		}
		unique[k] = true
	}
	return w
}

func parseSample(l string) (sample, error) {
	var p sample
	p.labels = map[string]string{}
	i := strings.IndexAny(l, "{ ")
	if i <= 0 {
		return p, fmt.Errorf("missing name")
	}
	p.name = l[:i]
	rest := l[i:]
	if rest[0] == '{' {
		rest = rest[1:]
		for {
			if strings.HasPrefix(rest, "}") {
				rest = rest[1:]
				break
			}
			eq := strings.Index(rest, `="`)
			if eq <= 0 {
				return p, fmt.Errorf("bad label")
			}
			name := rest[:eq]
			rest = rest[eq+2:]
			var wantOut strings.Builder
			closed := false
			for j := 0; j < len(rest); j++ {
				c := rest[j]
				if c == '\\' {
					if j+1 >= len(rest) {
						return p, fmt.Errorf("urwany escape")
					}
					switch rest[j+1] {
					case '\\':
						wantOut.WriteByte('\\')
					case '"':
						wantOut.WriteByte('"')
					case 'n':
						wantOut.WriteByte('\n')
					default:
						return p, fmt.Errorf("unknown escape \\%c", rest[j+1])
					}
					j++
					continue
				}
				if c == '"' {
					rest = rest[j+1:]
					closed = true
					break
				}
				wantOut.WriteByte(c)
			}
			if !closed {
				return p, fmt.Errorf("unterminated label value")
			}
			p.labels[name] = wantOut.String()
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
			}
		}
	}
	if !strings.HasPrefix(rest, " ") {
		return p, fmt.Errorf("missing value")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		return p, fmt.Errorf("value: %v", err)
	}
	p.value = v
	return p, nil
}

func TestMetricsParserCatchesErrors(t *testing.T) {
	// sanity: the test parser itself must be able to tell good from bad
	if _, err := parseSample(`a{b="c\"d"} 1`); err != nil {
		t.Errorf("a good sample was rejected: %v", err)
	}
	for _, bad := range []string{`a{b="c} 1`, `a{b="c\x"} 1`, `a{b="c"} x`, `{b="c"} 1`, `a 1 2 3`} {
		if _, err := parseSample(bad); err == nil {
			t.Errorf("a bad sample was accepted: %q", bad)
		}
	}
}

// ── test data ────────────────────────────────────────────────────────────────

const metricsConfig = `groups:
  - name: Media
    services:
      - name: Plain
        url: %[1]s
      - name: Zepsuty
        url: %[1]s/zle
  - name: "G\"1\\x"
    services:
      - name: 'Quo"te\Back'
        url: %[1]s
      - name: "line1\nline2"
        url: %[1]s
      - name: BezAdresu
  - name: Padnięty
    services:
      - name: Martwy
        url: http://127.0.0.1:1
`

// metricsServer sets the panel up on a config with tricky names and performs one
// check of each service, so the state is deterministic.
func metricsServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zle" {
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(target.Close)

	s := testServer(t)
	loggedInCookie(t, s) // the account exists, so /metrics does not hit /setup
	s.availabilityTTL = 0
	if err := os.WriteFile(s.path, []byte(fmt.Sprintf(metricsConfig, target.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range s.cfg.Groups {
		for _, u := range g.Services {
			if u.URL == "" {
				continue
			}
			if _, err := s.monitor.CheckNow(s.ctx, key(g.Name, u.Name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, s.Handler()
}

func fetchMetrics(t *testing.T, s *Server, h http.Handler) (*httptest.ResponseRecorder, metricsOutput) {
	t.Helper()
	token, _, err := s.st.NewAPIToken(s.ctx, "test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	w := sendWithoutCookie(h, "GET", "/metrics", "", "Authorization", "Bearer "+token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics: code %d: %s", w.Code, w.Body)
	}
	return w, parseMetrics(t, w.Body.String())
}

func TestMetricsFormatAndEscaping(t *testing.T) {
	s, h := metricsServer(t)
	_ = s.st.SaveResult(s.ctx, store.Result{Key: "Media/Plain", State: "up", At: time.Now()})
	w, m := fetchMetrics(t, s, h)

	if ct := w.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
	for _, family := range []string{
		"shyg_service_up", "shyg_service_degraded", "shyg_service_response_seconds",
		"shyg_service_availability_ratio", "shyg_service_last_check_timestamp_seconds", "shyg_build_info",
	} {
		if m.kind[family] != "gauge" || m.help[family] == "" {
			t.Errorf("%s: TYPE=%q HELP=%q", family, m.kind[family], m.help[family])
		}
	}

	// labels after unpacking return to the original names — also the quote,
	// backslash and newline
	hard := map[string]string{"group": `G"1\x`, "service": `Quo"te\Back`}
	if v, ok := m.value(t, "shyg_service_up", hard); !ok || v != 1 {
		t.Errorf("up for a name with a quote and backslash: %v %v", v, ok)
	}
	newLine := map[string]string{"group": `G"1\x`, "service": "line1\nline2"}
	if v, ok := m.value(t, "shyg_service_up", newLine); !ok || v != 1 {
		t.Errorf("up for a name with a newline: %v %v", v, ok)
	}
	// and in the raw text this shows as escapes, not as characters
	for _, want := range []string{`group="G\"1\\x"`, `service="Quo\"te\\Back"`, `service="line1\nline2"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the output lacks %s", want)
		}
	}
}

func TestMetricsStateValues(t *testing.T) {
	s, h := metricsServer(t)
	_, m := fetchMetrics(t, s, h)
	plain := map[string]string{"group": "Media", "service": "Plain"}
	broken := map[string]string{"group": "Media", "service": "Zepsuty"}
	dead := map[string]string{"group": "Padnięty", "service": "Martwy"}

	if v, ok := m.value(t, "shyg_service_up", plain); !ok || v != 1 {
		t.Errorf("Plain up = %v %v", v, ok)
	}
	if v, ok := m.value(t, "shyg_service_degraded", plain); !ok || v != 0 {
		t.Errorf("Plain degraded = %v %v", v, ok)
	}
	// degraded: the server responds, but with a code outside expect
	if v, _ := m.value(t, "shyg_service_up", broken); v != 0 {
		t.Errorf("Zepsuty up = %v, want 0", v)
	}
	if v, _ := m.value(t, "shyg_service_degraded", broken); v != 1 {
		t.Errorf("Zepsuty degraded = %v, want 1", v)
	}
	if _, ok := m.value(t, "shyg_service_response_seconds", broken); !ok {
		t.Error("degraded has a response time — the server responded")
	}
	// down: nothing responded, so there is no response time
	if v, ok := m.value(t, "shyg_service_up", dead); !ok || v != 0 {
		t.Errorf("Martwy up = %v %v", v, ok)
	}
	if v, _ := m.value(t, "shyg_service_degraded", dead); v != 0 {
		t.Errorf("Martwy degraded = %v", v)
	}
	if _, ok := m.value(t, "shyg_service_response_seconds", dead); ok {
		t.Error("down must not have shyg_service_response_seconds")
	}
	if v, ok := m.value(t, "shyg_service_response_seconds", plain); !ok || v < 0 || v > 5 {
		t.Errorf("Plain response_seconds = %v %v", v, ok)
	}
	// a service without an address is not checked — nothing to report (instead of a false 0)
	if _, ok := m.value(t, "shyg_service_up", map[string]string{"group": `G"1\x`, "service": "BezAdresu"}); ok {
		t.Error("a service without an address has no series, not 0")
	}
	if v, ok := m.value(t, "shyg_service_last_check_timestamp_seconds", plain); !ok ||
		time.Since(time.Unix(int64(v), 0)) > time.Minute || v < 1e9 {
		t.Errorf("last_check_timestamp = %v %v", v, ok)
	}
}

func TestMetricsAvailability24h(t *testing.T) {
	s, h := metricsServer(t)
	now := time.Now()
	for i, state := range []string{"up", "up", "up", "down"} {
		_ = s.st.SaveResult(s.ctx, store.Result{Key: "Media/Plain", State: state, At: now.Add(-time.Duration(i+1) * time.Hour)})
	}
	_ = s.st.SaveResult(s.ctx, store.Result{Key: "Media/Plain", State: "down", At: now.Add(-72 * time.Hour)})

	_, m := fetchMetrics(t, s, h)
	// 3 of 4 samples in the window are "up"; the fifth, from 3 days ago, is outside the 24 h window
	v, ok := m.value(t, "shyg_service_availability_ratio", map[string]string{"group": "Media", "service": "Plain", "window": "24h"})
	if !ok || v != 0.75 {
		t.Errorf("availability = %v %v, want 0.75", v, ok)
	}
	// a service without samples in the window — no series, not zero (zero would be an alarm)
	if _, ok := m.value(t, "shyg_service_availability_ratio", map[string]string{"group": "Media", "service": "Zepsuty", "window": "24h"}); ok {
		t.Error("a service without history has no series")
	}
}

func TestMetricsBuildInfoEscapesVersion(t *testing.T) {
	old := Version
	Version = `1.2.3-"rc"\1`
	t.Cleanup(func() { Version = old })
	s, h := metricsServer(t)
	_, m := fetchMetrics(t, s, h)
	if v, ok := m.value(t, "shyg_build_info", map[string]string{"version": Version}); !ok || v != 1 {
		t.Errorf("build_info: %v %v", v, ok)
	}
}

func TestMetricsNoDuplicatesWhenNamesCollide(t *testing.T) {
	s, h := metricsServer(t)
	// two services with the same key — Prometheus would reject the whole scrape
	s.mu.Lock()
	g := &s.cfg.Groups[0]
	g.Services = append(g.Services, g.Services[0])
	s.mu.Unlock()
	fetchMetrics(t, s, h) // parseMetrics reports duplicates
}

func TestMetricsEmptyConfigGivesOnlyBuildInfo(t *testing.T) {
	s := testServer(t)
	loggedInCookie(t, s)
	s.availabilityTTL = 0
	if err := os.WriteFile(s.path, []byte("groups: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	_, m := fetchMetrics(t, s, s.Handler())
	if len(m.samples) != 1 || m.samples[0].name != "shyg_build_info" {
		t.Errorf("%+v", m.samples)
	}
}
