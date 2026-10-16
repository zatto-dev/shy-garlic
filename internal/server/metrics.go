package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// GET /metrics — Prometheus text format, written by hand (zero dependencies).
//
// Meaning of the series:
//   - shyg_service_up: 1 only for state `up`; 0 for `degraded` and `down`.
//   - shyg_service_degraded: 1 when the server responds, but not the way it should.
//   - response_seconds: only when the server responded (up or degraded).
//   - availability_ratio: from the history in SQLite, 24 h window, 0..1.
//   - last_check_timestamp_seconds: when it was last checked.
//
// ! A service that has not been checked yet (or has no address) has no series
// at all — zero would mean "it is down" and wake someone with an alert nobody wanted.

const metricsKind = "text/plain; version=0.0.4; charset=utf-8"

// escapeLabels — label value: \ → \\, " → \", newline → \n.
func escapeLabels(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeHelp — HELP text: \ → \\ and newline → \n (the quote stays).
func escapeHelp(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(v)
}

func formatNumber(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

type metricFamily struct {
	name, help string
	lines      []string
}

func (r *metricFamily) add(labels string, value float64) {
	r.lines = append(r.lines, r.name+"{"+labels+"} "+formatNumber(value))
}

func (r *metricFamily) save(b *strings.Builder) {
	if len(r.lines) == 0 {
		return // a family without samples does not even get headers
	}
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", r.name, escapeHelp(r.help), r.name)
	for _, l := range r.lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}

// availabilityFromCache — the Availability answer is computed at most once per
// availabilityTTL; a scrape every few seconds must not group tens of thousands
// of history rows each time.
func (s *Server) availabilityFromCache(ctx context.Context) map[string]store.ServiceAvailability {
	s.availabilityMu.Lock()
	defer s.availabilityMu.Unlock()
	if s.availabilityMap != nil && time.Now().Before(s.availabilityUntil) {
		return s.availabilityMap
	}
	m, err := s.st.Availabilities(ctx, 24*time.Hour)
	if err != nil {
		return s.availabilityMap // better stale data than none
	}
	s.availabilityMap = m
	s.availabilityUntil = time.Now().Add(s.availabilityTTL)
	return m
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	snapshot := s.monitor.Snapshot()
	availability := s.availabilityFromCache(r.Context())

	up := &metricFamily{name: "shyg_service_up",
		help: "1 when the last check of the service succeeded (state up), 0 when it is degraded or down."}
	degraded := &metricFamily{name: "shyg_service_degraded",
		help: "1 when the service answers but not as expected (wrong status code or content), else 0."}
	response := &metricFamily{name: "shyg_service_response_seconds",
		help: "Response time of the last check in seconds. Absent when the service did not answer."}
	availabilityFam := &metricFamily{name: "shyg_service_availability_ratio",
		help: "Share of successful checks (state up) in the window, from 0 to 1."}
	last := &metricFamily{name: "shyg_service_last_check_timestamp_seconds",
		help: "Unix time of the last check of the service."}

	seen := map[string]bool{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			for _, u := range g.Services {
				key := key(g.Name, u.Name)
				// two services with the same key would give a duplicated series, and Prometheus
				// then rejects the whole scrape; the monitor tracks them as one anyway
				if seen[key] {
					continue
				}
				seen[key] = true

				labels := `group="` + escapeLabels(g.Name) + `",service="` + escapeLabels(u.Name) + `"`
				result, is := snapshot[key]
				if is && result.State != health.StateUnknown && !result.At.IsZero() {
					up.add(labels, boolFloat(result.State == health.StateUp))
					degraded.add(labels, boolFloat(result.State == health.StateDegraded))
					if result.State == health.StateUp || result.State == health.StateDegraded {
						response.add(labels, result.Latency.Seconds())
					}
					last.add(labels, float64(result.At.Unix()))
				}
				if d, ok := availability[key]; ok && d.Samples > 0 {
					availabilityFam.add(labels+`,window="24h"`, float64(d.Successes)/float64(d.Samples))
				}
			}
		}
	}

	info := &metricFamily{name: "shyg_build_info",
		help: "Build information; the value is always 1."}
	info.add(`version="`+escapeLabels(Version)+`"`, 1)

	var b strings.Builder
	for _, family := range []*metricFamily{up, degraded, response, availabilityFam, last, info} {
		family.save(&b)
	}
	w.Header().Set("Content-Type", metricsKind)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
