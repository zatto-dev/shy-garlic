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

// ── parser formatu tekstowego Prometheusa — tylko tyle, ile potrzeba testom ──

type próbka struct {
	nazwa     string
	etykiety  map[string]string
	wartość   float64
	numerLinii int
}

type wyjścieMetryk struct {
	próbki []próbka
	typ    map[string]string
	help   map[string]string
}

func (w wyjścieMetryk) wartość(t *testing.T, nazwa string, etykiety map[string]string) (float64, bool) {
	t.Helper()
	for _, p := range w.próbki {
		if p.nazwa != nazwa || len(p.etykiety) != len(etykiety) {
			continue
		}
		ok := true
		for k, v := range etykiety {
			if p.etykiety[k] != v {
				ok = false
			}
		}
		if ok {
			return p.wartość, true
		}
	}
	return 0, false
}

func (w wyjścieMetryk) ile(nazwa string) int {
	n := 0
	for _, p := range w.próbki {
		if p.nazwa == nazwa {
			n++
		}
	}
	return n
}

// parsujMetryki rozbiera tekst i PRZY OKAZJI pilnuje składni: każda próbka musi
// mieć wcześniej TYPE i HELP swojej rodziny, a każdy TYPE/HELP wystąpić raz.
func parsujMetryki(t *testing.T, tekst string) wyjścieMetryk {
	t.Helper()
	w := wyjścieMetryk{typ: map[string]string{}, help: map[string]string{}}
	if !strings.HasSuffix(tekst, "\n") {
		t.Error("wyjście musi kończyć się znakiem nowej linii")
	}
	for i, linia := range strings.Split(strings.TrimSuffix(tekst, "\n"), "\n") {
		nr := i + 1
		switch {
		case linia == "":
			t.Errorf("linia %d: pusta linia w środku wyjścia", nr)
		case strings.HasPrefix(linia, "# HELP "):
			c := strings.SplitN(strings.TrimPrefix(linia, "# HELP "), " ", 2)
			if len(c) != 2 || c[1] == "" {
				t.Errorf("linia %d: HELP bez tekstu: %q", nr, linia)
				continue
			}
			if _, dwa := w.help[c[0]]; dwa {
				t.Errorf("linia %d: drugi HELP dla %s", nr, c[0])
			}
			w.help[c[0]] = c[1]
		case strings.HasPrefix(linia, "# TYPE "):
			c := strings.Fields(strings.TrimPrefix(linia, "# TYPE "))
			if len(c) != 2 || (c[1] != "gauge" && c[1] != "counter") {
				t.Errorf("linia %d: zły TYPE: %q", nr, linia)
				continue
			}
			if _, dwa := w.typ[c[0]]; dwa {
				t.Errorf("linia %d: drugi TYPE dla %s", nr, c[0])
			}
			w.typ[c[0]] = c[1]
		case strings.HasPrefix(linia, "#"):
			t.Errorf("linia %d: nieznany komentarz %q", nr, linia)
		default:
			p, err := parsujPróbkę(linia)
			if err != nil {
				t.Errorf("linia %d: %v: %q", nr, err, linia)
				continue
			}
			p.numerLinii = nr
			if w.typ[p.nazwa] == "" || w.help[p.nazwa] == "" {
				t.Errorf("linia %d: próbka %s bez wcześniejszych HELP/TYPE", nr, p.nazwa)
			}
			w.próbki = append(w.próbki, p)
		}
	}
	// próbki jednej rodziny muszą leżeć razem, bez rodzin „przeplatanych"
	ostatni, widziane := "", map[string]bool{}
	for _, p := range w.próbki {
		if p.nazwa != ostatni {
			if widziane[p.nazwa] {
				t.Errorf("rodzina %s rozdzielona innymi", p.nazwa)
			}
			widziane[p.nazwa] = true
			ostatni = p.nazwa
		}
	}
	// brak duplikatów (ta sama nazwa i etykiety) — Prometheus odrzuca taki scrape
	unikat := map[string]bool{}
	for _, p := range w.próbki {
		k := p.nazwa + fmt.Sprint(p.etykiety)
		if unikat[k] {
			t.Errorf("duplikat próbki %s%v", p.nazwa, p.etykiety)
		}
		unikat[k] = true
	}
	return w
}

func parsujPróbkę(l string) (próbka, error) {
	var p próbka
	p.etykiety = map[string]string{}
	i := strings.IndexAny(l, "{ ")
	if i <= 0 {
		return p, fmt.Errorf("brak nazwy")
	}
	p.nazwa = l[:i]
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
				return p, fmt.Errorf("zła etykieta")
			}
			nazwa := rest[:eq]
			rest = rest[eq+2:]
			var wart strings.Builder
			zamknięta := false
			for j := 0; j < len(rest); j++ {
				c := rest[j]
				if c == '\\' {
					if j+1 >= len(rest) {
						return p, fmt.Errorf("urwany escape")
					}
					switch rest[j+1] {
					case '\\':
						wart.WriteByte('\\')
					case '"':
						wart.WriteByte('"')
					case 'n':
						wart.WriteByte('\n')
					default:
						return p, fmt.Errorf("nieznany escape \\%c", rest[j+1])
					}
					j++
					continue
				}
				if c == '"' {
					rest = rest[j+1:]
					zamknięta = true
					break
				}
				wart.WriteByte(c)
			}
			if !zamknięta {
				return p, fmt.Errorf("niezamknięta wartość etykiety")
			}
			p.etykiety[nazwa] = wart.String()
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
			}
		}
	}
	if !strings.HasPrefix(rest, " ") {
		return p, fmt.Errorf("brak wartości")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		return p, fmt.Errorf("wartość: %v", err)
	}
	p.wartość = v
	return p, nil
}

func TestParserMetrykWyłapujeBłędy(t *testing.T) {
	// sanity: parser testowy sam musi umieć odróżnić dobre od złego
	if _, err := parsujPróbkę(`a{b="c\"d"} 1`); err != nil {
		t.Errorf("dobra próbka odrzucona: %v", err)
	}
	for _, zła := range []string{`a{b="c} 1`, `a{b="c\x"} 1`, `a{b="c"} x`, `{b="c"} 1`, `a 1 2 3`} {
		if _, err := parsujPróbkę(zła); err == nil {
			t.Errorf("zła próbka przyjęta: %q", zła)
		}
	}
}

// ── dane testowe ─────────────────────────────────────────────────────────────

const configMetryki = `groups:
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

// serwerMetryki stawia panel na configu z trudnymi nazwami i wykonuje po jednym
// sprawdzeniu każdej usługi, żeby stan był deterministyczny.
func serwerMetryki(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	cel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zle" {
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(cel.Close)

	s := serwerTestowy(t)
	ciasteczkoZalogowanego(t, s) // konto istnieje, więc /metrics nie trafia na /setup
	s.dostępnościTTL = 0
	if err := os.WriteFile(s.path, []byte(fmt.Sprintf(configMetryki, cel.URL)), 0o644); err != nil {
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
			if _, err := s.mon.SprawdźTeraz(s.ctx, key(g.Name, u.Name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, s.Handler()
}

func pobierzMetryki(t *testing.T, s *Server, h http.Handler) (*httptest.ResponseRecorder, wyjścieMetryk) {
	t.Helper()
	token, _, err := s.st.NowyTokenAPI(s.ctx, "test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	w := wyślijBezCiastka(h, "GET", "/metrics", "", "Authorization", "Bearer "+token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics: kod %d: %s", w.Code, w.Body)
	}
	return w, parsujMetryki(t, w.Body.String())
}

func TestMetrykiFormatIEscapowanie(t *testing.T) {
	s, h := serwerMetryki(t)
	_ = s.st.ZapiszWynik(s.ctx, store.Wynik{Klucz: "Media/Plain", Stan: "up", Kiedy: time.Now()})
	w, m := pobierzMetryki(t, s, h)

	if ct := w.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
	for _, rodzina := range []string{
		"shyg_service_up", "shyg_service_degraded", "shyg_service_response_seconds",
		"shyg_service_availability_ratio", "shyg_service_last_check_timestamp_seconds", "shyg_build_info",
	} {
		if m.typ[rodzina] != "gauge" || m.help[rodzina] == "" {
			t.Errorf("%s: TYPE=%q HELP=%q", rodzina, m.typ[rodzina], m.help[rodzina])
		}
	}

	// etykiety po rozpakowaniu wracają do oryginalnych nazw — także cudzysłów,
	// backslash i nowa linia
	trudne := map[string]string{"group": `G"1\x`, "service": `Quo"te\Back`}
	if v, ok := m.wartość(t, "shyg_service_up", trudne); !ok || v != 1 {
		t.Errorf("up dla nazwy z cudzysłowem i backslashem: %v %v", v, ok)
	}
	nowaLinia := map[string]string{"group": `G"1\x`, "service": "line1\nline2"}
	if v, ok := m.wartość(t, "shyg_service_up", nowaLinia); !ok || v != 1 {
		t.Errorf("up dla nazwy z nową linią: %v %v", v, ok)
	}
	// i to widać w surowym tekście jako escape'y, nie jako znaki
	for _, chcę := range []string{`group="G\"1\\x"`, `service="Quo\"te\\Back"`, `service="line1\nline2"`} {
		if !strings.Contains(w.Body.String(), chcę) {
			t.Errorf("w wyjściu brak %s", chcę)
		}
	}
}

func TestMetrykiWartościStanów(t *testing.T) {
	s, h := serwerMetryki(t)
	_, m := pobierzMetryki(t, s, h)
	plain := map[string]string{"group": "Media", "service": "Plain"}
	zepsuty := map[string]string{"group": "Media", "service": "Zepsuty"}
	martwy := map[string]string{"group": "Padnięty", "service": "Martwy"}

	if v, ok := m.wartość(t, "shyg_service_up", plain); !ok || v != 1 {
		t.Errorf("Plain up = %v %v", v, ok)
	}
	if v, ok := m.wartość(t, "shyg_service_degraded", plain); !ok || v != 0 {
		t.Errorf("Plain degraded = %v %v", v, ok)
	}
	// degraded: serwer odpowiada, ale kodem spoza expect
	if v, _ := m.wartość(t, "shyg_service_up", zepsuty); v != 0 {
		t.Errorf("Zepsuty up = %v, chcę 0", v)
	}
	if v, _ := m.wartość(t, "shyg_service_degraded", zepsuty); v != 1 {
		t.Errorf("Zepsuty degraded = %v, chcę 1", v)
	}
	if _, ok := m.wartość(t, "shyg_service_response_seconds", zepsuty); !ok {
		t.Error("degraded ma czas odpowiedzi — serwer odpowiedział")
	}
	// down: nic nie odpowiedziało, więc nie ma czasu odpowiedzi
	if v, ok := m.wartość(t, "shyg_service_up", martwy); !ok || v != 0 {
		t.Errorf("Martwy up = %v %v", v, ok)
	}
	if v, _ := m.wartość(t, "shyg_service_degraded", martwy); v != 0 {
		t.Errorf("Martwy degraded = %v", v)
	}
	if _, ok := m.wartość(t, "shyg_service_response_seconds", martwy); ok {
		t.Error("down nie ma mieć shyg_service_response_seconds")
	}
	if v, ok := m.wartość(t, "shyg_service_response_seconds", plain); !ok || v < 0 || v > 5 {
		t.Errorf("Plain response_seconds = %v %v", v, ok)
	}
	// usługa bez adresu nie jest sprawdzana — nie ma o czym raportować (zamiast fałszywego 0)
	if _, ok := m.wartość(t, "shyg_service_up", map[string]string{"group": `G"1\x`, "service": "BezAdresu"}); ok {
		t.Error("usługa bez adresu ma brak serii, nie 0")
	}
	if v, ok := m.wartość(t, "shyg_service_last_check_timestamp_seconds", plain); !ok ||
		time.Since(time.Unix(int64(v), 0)) > time.Minute || v < 1e9 {
		t.Errorf("last_check_timestamp = %v %v", v, ok)
	}
}

func TestMetrykiDostępność24h(t *testing.T) {
	s, h := serwerMetryki(t)
	teraz := time.Now()
	for i, stan := range []string{"up", "up", "up", "down"} {
		_ = s.st.ZapiszWynik(s.ctx, store.Wynik{Klucz: "Media/Plain", Stan: stan, Kiedy: teraz.Add(-time.Duration(i+1) * time.Hour)})
	}
	_ = s.st.ZapiszWynik(s.ctx, store.Wynik{Klucz: "Media/Plain", Stan: "down", Kiedy: teraz.Add(-72 * time.Hour)})

	_, m := pobierzMetryki(t, s, h)
	// 3 z 4 próbek w oknie to „up"; piąta, sprzed 3 dni, jest poza oknem 24 h
	v, ok := m.wartość(t, "shyg_service_availability_ratio", map[string]string{"group": "Media", "service": "Plain", "window": "24h"})
	if !ok || v != 0.75 {
		t.Errorf("availability = %v %v, chcę 0.75", v, ok)
	}
	// usługa bez próbek w oknie — brak serii, nie zero (zero byłoby alarmem)
	if _, ok := m.wartość(t, "shyg_service_availability_ratio", map[string]string{"group": "Media", "service": "Zepsuty", "window": "24h"}); ok {
		t.Error("usługa bez historii ma brak serii")
	}
}

func TestMetrykiBuildInfoZEscapowaniemWersji(t *testing.T) {
	stara := Version
	Version = `1.2.3-"rc"\1`
	t.Cleanup(func() { Version = stara })
	s, h := serwerMetryki(t)
	_, m := pobierzMetryki(t, s, h)
	if v, ok := m.wartość(t, "shyg_build_info", map[string]string{"version": Version}); !ok || v != 1 {
		t.Errorf("build_info: %v %v", v, ok)
	}
}

func TestMetrykiBezDuplikatówGdyNazwyKolidują(t *testing.T) {
	s, h := serwerMetryki(t)
	// dwie usługi o tym samym kluczu — Prometheus odrzuciłby cały scrape
	s.mu.Lock()
	g := &s.cfg.Groups[0]
	g.Services = append(g.Services, g.Services[0])
	s.mu.Unlock()
	pobierzMetryki(t, s, h) // parsujMetryki zgłasza duplikaty
}

func TestMetrykiPustyConfigDajeTylkoBuildInfo(t *testing.T) {
	s := serwerTestowy(t)
	ciasteczkoZalogowanego(t, s)
	s.dostępnościTTL = 0
	if err := os.WriteFile(s.path, []byte("groups: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	_, m := pobierzMetryki(t, s, s.Handler())
	if len(m.próbki) != 1 || m.próbki[0].nazwa != "shyg_build_info" {
		t.Errorf("%+v", m.próbki)
	}
}
