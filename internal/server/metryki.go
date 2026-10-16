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

// GET /metrics — format tekstowy Prometheusa, pisany ręcznie (zero zależności).
//
// Znaczenie serii:
//   - shyg_service_up: 1 tylko dla stanu `up`; 0 dla `degraded` i `down`.
//   - shyg_service_degraded: 1, gdy serwer odpowiada, ale nie tak, jak trzeba.
//   - response_seconds: tylko gdy serwer odpowiedział (up albo degraded).
//   - availability_ratio: z historii w SQLite, okno 24 h, 0..1.
//   - last_check_timestamp_seconds: kiedy ostatnio sprawdzono.
//
// ! Usługa, której jeszcze nie sprawdzono (albo bez adresu), nie ma serii
// wcale — zero oznaczałoby „leży" i budziło alert, którego nikt nie chciał.

const typMetryk = "text/plain; version=0.0.4; charset=utf-8"

// escapeEtykiety — wartość etykiety: \ → \\, " → \", nowa linia → \n.
func escapeEtykiety(v string) string {
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

// escapeHelp — tekst HELP: \ → \\ i nowa linia → \n (cudzysłów zostaje).
func escapeHelp(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(v)
}

func liczba(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

type rodzinaMetryk struct {
	nazwa, help string
	linie       []string
}

func (r *rodzinaMetryk) dodaj(etykiety string, wartość float64) {
	r.linie = append(r.linie, r.nazwa+"{"+etykiety+"} "+liczba(wartość))
}

func (r *rodzinaMetryk) zapisz(b *strings.Builder) {
	if len(r.linie) == 0 {
		return // rodzina bez próbek nie dostaje nawet nagłówków
	}
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", r.nazwa, escapeHelp(r.help), r.nazwa)
	for _, l := range r.linie {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}

// dostępnościZCache — odpowiedź Dostępności jest liczona co najwyżej raz na
// dostępnościTTL; scrape co kilka sekund nie ma za każdym razem grupować
// dziesiątek tysięcy wierszy historii.
func (s *Server) dostępnościZCache(ctx context.Context) map[string]store.DostępnośćUsługi {
	s.muDostępności.Lock()
	defer s.muDostępności.Unlock()
	if s.dostępnościMapa != nil && time.Now().Before(s.dostępnościDo) {
		return s.dostępnościMapa
	}
	m, err := s.st.Dostępności(ctx, 24*time.Hour)
	if err != nil {
		return s.dostępnościMapa // lepiej stare dane niż żadne
	}
	s.dostępnościMapa = m
	s.dostępnościDo = time.Now().Add(s.dostępnościTTL)
	return m
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	migawka := s.mon.Snapshot()
	dostępności := s.dostępnościZCache(r.Context())

	up := &rodzinaMetryk{nazwa: "shyg_service_up",
		help: "1 when the last check of the service succeeded (state up), 0 when it is degraded or down."}
	degraded := &rodzinaMetryk{nazwa: "shyg_service_degraded",
		help: "1 when the service answers but not as expected (wrong status code or content), else 0."}
	odpowiedź := &rodzinaMetryk{nazwa: "shyg_service_response_seconds",
		help: "Response time of the last check in seconds. Absent when the service did not answer."}
	dostępność := &rodzinaMetryk{nazwa: "shyg_service_availability_ratio",
		help: "Share of successful checks (state up) in the window, from 0 to 1."}
	ostatnie := &rodzinaMetryk{nazwa: "shyg_service_last_check_timestamp_seconds",
		help: "Unix time of the last check of the service."}

	widziane := map[string]bool{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			for _, u := range g.Services {
				klucz := key(g.Name, u.Name)
				// dwie usługi o tym samym kluczu dałyby zdublowaną serię, a Prometheus
				// odrzuca wtedy cały scrape; monitor i tak śledzi je jako jedną
				if widziane[klucz] {
					continue
				}
				widziane[klucz] = true

				et := `group="` + escapeEtykiety(g.Name) + `",service="` + escapeEtykiety(u.Name) + `"`
				wynik, jest := migawka[klucz]
				if jest && wynik.State != health.StateUnknown && !wynik.At.IsZero() {
					up.dodaj(et, boolFloat(wynik.State == health.StateUp))
					degraded.dodaj(et, boolFloat(wynik.State == health.StateDegraded))
					if wynik.State == health.StateUp || wynik.State == health.StateDegraded {
						odpowiedź.dodaj(et, wynik.Latency.Seconds())
					}
					ostatnie.dodaj(et, float64(wynik.At.Unix()))
				}
				if d, ok := dostępności[klucz]; ok && d.Próbek > 0 {
					dostępność.dodaj(et+`,window="24h"`, float64(d.Udanych)/float64(d.Próbek))
				}
			}
		}
	}

	info := &rodzinaMetryk{nazwa: "shyg_build_info",
		help: "Build information; the value is always 1."}
	info.dodaj(`version="`+escapeEtykiety(Version)+`"`, 1)

	var b strings.Builder
	for _, rodzina := range []*rodzinaMetryk{up, degraded, odpowiedź, dostępność, ostatnie, info} {
		rodzina.zapisz(&b)
	}
	w.Header().Set("Content-Type", typMetryk)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
