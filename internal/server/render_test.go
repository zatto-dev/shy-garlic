package server

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

var atrybutPresetu = regexp.MustCompile(`data-preset="([^"]*)"`)

// sprawdźWyjścieSzablonu pilnuje trzech rzeczy naraz: że do atrybutu nie
// wyciekła wartość spoza allowlisty, że w szablonie nie ma stylów inline
// (CSP ma style-src 'self', więc byłyby martwe) i że nie ma znacznika <style>.
func sprawdźWyjścieSzablonu(t *testing.T, html string) {
	t.Helper()
	for _, m := range atrybutPresetu.FindAllStringSubmatch(html, -1) {
		if !config.Allowed(m[1], config.BackgroundPresets) {
			t.Errorf("data-preset=%q — wartość spoza allowlisty w atrybucie", m[1])
		}
	}
	if strings.Contains(html, "style=") {
		t.Error("w wyjściu jest atrybut style= — CSP style-src 'self' go zablokuje")
	}
	if strings.Contains(html, "<style") {
		t.Error("w wyjściu jest znacznik <style> — to samo, CSP go zablokuje")
	}
}

func pełnyWidokStrony() widokStrony {
	d := widokStrony{
		Title: "Homelab", Wersja: "test", Użytkownik: "admin", LiczbaIkon: 3363,
		Stan: template.JS("{}"), Lang: "pl",
		Wyglad:     wyglądWidok{Preset: "aurora", Blur: 10, Ton: "dark", Widok: "grid"},
		WygladJSON: template.JS(`{"preset":"aurora","image":"","blur":10,"tone":"dark","view":"grid"}`),
		MoznaPisac: true, TłaLimit: LimitTłaMB,
		Groups: []widokGrupy{{Name: "Media", Services: []widokUsługi{{
			Key: "Media/Jellyfin", Group: "Media", Name: "Jellyfin",
			URL: "http://jellyfin", Host: "jellyfin", IconName: "jellyfin",
			Icon: iconRef{Src: "/icon/jellyfin", Litera: "J"}, Rozmiar: "md",
		}}}},
	}
	d.T, d.TJSON, d.DrugiJezyk, d.DrugiJezykNazwa, d.DrugiJezykKrotko = wypełnijJęzyk(i18n.PL)
	d.SzukajIkonPlaceholder = "Szukaj ikony"
	return d
}

// TestSzablonyWykonujaSie łapie literówkę w {{.Pole}}, której ParseFS nie widzi
// — błąd wychodzi dopiero przy wykonaniu, czyli u użytkownika.
func TestSzablonyWykonujaSie(t *testing.T) {
	s := serwerTestowy(t)
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "index.html", pełnyWidokStrony()); err != nil {
		t.Fatalf("index.html: %v", err)
	}
	sprawdźWyjścieSzablonu(t, buf.String())

	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		for _, setup := range []bool{false, true} {
			buf.Reset()
			dane := s.zJęzykiem(l, widokLogowania{Setup: setup, Wersja: "test", Tytuł: "Logowanie"})
			if err := s.tmpl.ExecuteTemplate(&buf, "login.html", dane); err != nil {
				t.Fatalf("login.html (%s, setup=%v): %v", l, setup, err)
			}
			sprawdźWyjścieSzablonu(t, buf.String())
		}
	}
}

func TestHandleIndexRenderujeBezPaniki(t *testing.T) {
	warianty := []struct {
		nazwa string
		ustaw func(*config.Config)
	}{
		{"z pliku", func(*config.Config) {}},
		{"pusty wygląd", func(c *config.Config) { c.Appearance = config.Appearance{} }},
		{"wygląd pełen śmieci", func(c *config.Config) {
			c.Appearance = config.Appearance{
				Background: config.Background{
					Preset: `<script>alert(1)</script>`,
					Image:  `a") ; } body{display:none} .x{a:url("`,
					Blur:   -5,
					Tone:   "x",
				},
				View: `" onload="x`,
			}
		}},
	}
	for _, w := range warianty {
		s := serwerTestowy(t)
		s.mu.Lock()
		w.ustaw(s.cfg)
		s.mu.Unlock()

		rec := httptest.NewRecorder()
		s.handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: kod = %d", w.nazwa, rec.Code)
		}
		body := rec.Body.String()
		sprawdźWyjścieSzablonu(t, body)
		if strings.Contains(body, "onload=") || strings.Contains(body, "<script>alert") {
			t.Errorf("%s: śmieci z configu wyszły niezescape'owane", w.nazwa)
		}
	}
}

func TestLoginNieWypuszczaTapety(t *testing.T) {
	s := serwerTestowy(t)
	s.mu.Lock()
	s.cfg.Appearance.Background.Preset = "photo"
	s.cfg.Appearance.Background.Image = "prywatne-zdjecie.jpg"
	s.mu.Unlock()

	w := s.wyglądBramy()
	if w.Preset == config.PresetPhoto || w.Obraz != "" {
		t.Fatalf("brama dostała tapetę: %+v", w)
	}

	var buf bytes.Buffer
	dane := s.zJęzykiem(i18n.PL, widokLogowania{Wersja: "test", Tytuł: "Logowanie"})
	if err := s.tmpl.ExecuteTemplate(&buf, "login.html", dane); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "/bg/") || strings.Contains(buf.String(), "prywatne-zdjecie") {
		t.Fatal("nazwa tapety wyciekła na stronę logowania")
	}
	sprawdźWyjścieSzablonu(t, buf.String())
}

// ══════════════════════════════════════════════════════════════════════════════
// Podgląd offline
//
// Panel siedzi za logowaniem, więc zrzuty ekranu robi się z plików, nie z
// przeglądarki wpuszczonej na serwer. Ten test renderuje prawdziwe szablony
// prawdziwym handlerem na examples/homelab.yaml i zapisuje gotowe strony do
// bin/podglad/ razem z assetami i ikonami. Ścieżki /assets/… i /icon/… robią
// się względne, żeby wszystko działało spod file:// bez ani jednego żądania.
//
// ! Zapis jest pod flagą — samo `./x test` (czyli `go test ./...`) flagi nie
// podaje, więc nic nie powstaje. Odpalenie podglądu: `./x podglad`.
// ══════════════════════════════════════════════════════════════════════════════

var flagaPodglądu = flag.Bool("podglad", false,
	"zapisz wyrenderowane strony do bin/podglad/ (podgląd bez serwera i bez logowania)")

// korzeńRepo szuka katalogu z go.mod — test chodzi w internal/server, a pliki
// mają wylądować w bin/podglad/ w korzeniu.
func korzeńRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		rodzic := filepath.Dir(dir)
		if rodzic == dir {
			t.Fatal("nie znalazłem korzenia repo (go.mod)")
		}
		dir = rodzic
	}
}

// serwerNaConfigu stawia panel na podanym pliku YAML.
//
// ! Celowo nie woła reload() — SetTargets ruszyłby monitor, a examples/homelab.yaml
// ma prawdziwe adresy z domowej sieci. Podgląd ma renderować, nie skanować.
func serwerNaConfigu(t *testing.T, źródło, cacheIkon string) *Server {
	t.Helper()
	surowy, err := os.ReadFile(źródło)
	if err != nil {
		t.Fatal(err)
	}
	katalog := t.TempDir()
	ścieżka := filepath.Join(katalog, "shygarlic.yaml")
	if err := os.WriteFile(ścieżka, surowy, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(katalog, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ścieżka, st, Opcje{CacheDir: cacheIkon, BackgroundDir: filepath.Join(katalog, "bg")})
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	cfg, err := config.Load(surowy)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cfg = cfg
	s.stamp = s.stampOf()
	s.mu.Unlock()
	t.Cleanup(func() {
		s.Close()
		_ = st.Close()
	})
	return s
}

// stanDemo — wyniki zdrowia wstawiane do podglądu. Monitor w teście nie chodzi
// (patrz serwerNaConfigu), a strona z samymi "—" nie pokazuje ani kolorów
// statusów, ani liczników w pasku. Wartości są zmyślone i tylko do podglądu.
func stanDemo(cfg *config.Config) map[string]health.Result {
	type wpis struct {
		stan health.State
		ms   int64
		kod  int
		błąd string
	}
	wyjątki := map[string]wpis{
		"Media/Immich":              {health.StateDegraded, 812, 502, ""},
		"Tools/Paperless-ngx":       {health.StateDown, 0, 0, "connection refused"},
		"Monitoring/Grafana":        {health.StateUnknown, 0, 0, ""},
		"Infrastructure/Home Assistant": {health.StateUp, 118, 200, ""},
	}
	domyślne := []int64{34, 47, 61, 72, 96, 128, 153}
	out := map[string]health.Result{}
	i := 0
	for _, g := range cfg.Groups {
		for _, svc := range g.Services {
			k := key(g.Name, svc.Name)
			kiedy := time.Now().Add(-time.Duration(7+3*i) * time.Second)
			if w, ok := wyjątki[k]; ok {
				out[k] = health.Result{State: w.stan, MS: w.ms, Code: w.kod, Err: w.błąd, At: kiedy}
			} else {
				out[k] = health.Result{State: health.StateUp, MS: domyślne[i%len(domyślne)],
					Code: 200, At: kiedy}
			}
			i++
		}
	}
	return out
}

// ikonyNaDysk ściąga ikony użyte w configu do <katalog>/icon/ i zwraca mapę
// nazwa → ścieżka względna. Bez tego karty w podglądzie byłyby puste: /icon/…
// obsługuje serwer, którego pod file:// nie ma.
func ikonyNaDysk(t *testing.T, s *Server, cfg *config.Config, katalog string) map[string]string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(katalog, "icon"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.ikony.Odśwież(context.Background())
	out := map[string]string{}
	pobierz := func(n string) {
		n = strings.TrimSuffix(strings.TrimSuffix(n, ".svg"), ".png")
		if n == "" || strings.Contains(n, "://") {
			return
		}
		if _, mam := out[n]; mam {
			return
		}
		dane, typ, err := s.ikony.Czytaj(context.Background(), n)
		if err != nil {
			t.Logf("ikona %q niedostępna (%v) — karta pokaże literę", n, err)
			return
		}
		ext := "svg"
		if typ == "image/png" {
			ext = "png"
		}
		plik := filepath.Join("icon", n+"."+ext)
		if err := os.WriteFile(filepath.Join(katalog, plik), dane, 0o644); err != nil {
			t.Fatal(err)
		}
		out[n] = plik
	}
	for _, g := range cfg.Groups {
		for _, svc := range g.Services {
			n := strings.TrimSuffix(strings.TrimSuffix(svc.Icon, ".svg"), ".png")
			pobierz(n)
			// ikona z osobnymi wariantami jasny/ciemny renderuje się jako DWA
			// <img>, więc podgląd offline musi mieć na dysku oba pliki, nie
			// tylko nazwę bazową z configu.
			if ciemny, jasny := s.ikony.Warianty(n); ciemny != "" {
				pobierz(ciemny)
				pobierz(jasny)
			}
		}
	}
	return out
}

// assetyNaDysk wysypuje embed.FS do <katalog>/assets/. style.css dostaje
// ścieżki względne, żeby @font-face trafił w pliki, a nie w korzeń dysku.
func assetyNaDysk(t *testing.T, katalog string) {
	t.Helper()
	err := fs.WalkDir(files, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		cel := filepath.Join(katalog, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(cel, 0o755)
		}
		dane, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".css") {
			dane = []byte(strings.ReplaceAll(string(dane), `url("/assets/`, `url("`))
		}
		return os.WriteFile(cel, dane, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// zaślepkaPodglądu — jedyna rzecz w podglądzie, której nie ma na produkcji.
// Pod file:// nie ma ani SSE, ani /api/*, więc bez tego stopka od razu zapala
// „brak połączenia", a wykresy na kartach zostają puste — czyli zrzut ekranu
// pokazywałby awarię, a nie wygląd. Dane historii są syntetyczne i wyliczane
// z wstrzykniętego stanu demo.
const zaślepkaPodglądu = `<script>
/* PODGLĄD OFFLINE — nie trafia na produkcję (test render_test.go) */
(() => {
  class StrumienPodgladu { close() {} addEventListener() {} }
  window.EventSource = StrumienPodgladu;

  const historia = (klucz) => {
    const teraz = (window.STAN_POCZATKOWY || {})[klucz] || { state: "up", ms: 60 };
    let h = 2166136261;
    for (const z of klucz) h = (Math.imul(h ^ z.charCodeAt(0), 16777619)) >>> 0;
    const punkty = [];
    for (let i = 0; i < 40; i++) {
      h = (Math.imul(h, 1103515245) + 12345) >>> 0;
      const r = h % 100;
      let stan = r < 3 ? "degraded" : r < 5 ? "down" : "up";
      if (i >= 37 && teraz.state !== "up") stan = teraz.state;
      punkty.push({ state: stan, ms: 28 + (h % 130) });
    }
    const up = punkty.filter((p) => p.state === "up").length;
    return { points: punkty, samples: punkty.length, uptime: (100 * up) / punkty.length };
  };

  window.fetch = (wejscie) => {
    const u = String(wejscie);
    const json = (o) => Promise.resolve(new Response(JSON.stringify(o),
      { status: 200, headers: { "Content-Type": "application/json" } }));
    if (u.startsWith("/api/history")) {
      return json(historia(new URLSearchParams(u.split("?")[1] || "").get("key") || ""));
    }
    if (u.startsWith("/api/service")) {
      const klucz = new URLSearchParams(u.split("?")[1] || "").get("key") || "";
      const el = document.querySelector('.karta[data-key="' + klucz + '"]');
      const h = historia(klucz);
      const teraz = (window.STAN_POCZATKOWY || {})[klucz] || { state: "up", ms: 60 };
      const punkty = h.points.map((p, i) => ({
        state: p.state, ms: p.ms, code: p.state === "degraded" ? 502 : 200,
        at: new Date(Date.now() - (h.points.length - i) * 30000).toISOString(),
      }));
      const zmiany = [];
      for (let i = 1; i < punkty.length; i++) {
        if (punkty[i].state !== punkty[i - 1].state) {
          zmiany.push({ at: punkty[i].at, from: punkty[i - 1].state, to: punkty[i].state,
            code: punkty[i].state === "degraded" ? 502 : 0,
            err: punkty[i].state === "down" ? "connection refused" : "" });
        }
      }
      return json({
        key: klucz, name: (el && el.dataset.nazwa) || klucz,
        url: (el && el.dataset.adres) || "", icon: (el && el.dataset.ikona) || "",
        interval: "30s", timeout: "6s", expect: [200, 301, 302, 401, 403], insecure: false,
        now: teraz,
        day: { samples: 2871, uptime: h.uptime, p50: 46, p95: 128, max: 512 },
        week: { samples: 20104, uptime: Math.min(100, h.uptime + 1.4), p50: 48, p95: 141, max: 980 },
        points: punkty, changes: zmiany.slice(-6).reverse(),
      });
    }
    if (u.startsWith("/api/info")) {
      return json({ docker: true, dockerNew: 0, backgrounds: true, notify: true });
    }
    if (u.startsWith("/api/background")) return json({ items: [] });
    if (u.startsWith("/api/notify")) {
      return json({ wlaczony: true, webhook: "https://discord.com/•••", zrodlo: "baza", prog: 2 });
    }
    if (u.startsWith("/api/instance")) {
      return json({ opcje: [
        { klucz: "addr", wartosc: ":8080", zrodlo: "domyslna" },
        { klucz: "config", wartosc: "/etc/shygarlic/shygarlic.yaml", zrodlo: "srodowisko" },
        { klucz: "data", wartosc: "/data/shygarlic.db", zrodlo: "srodowisko" },
        { klucz: "backgrounds", wartosc: "/data/bg", zrodlo: "domyslna" },
        { klucz: "docker", wartosc: "/var/run/docker.sock", zrodlo: "flaga" },
        { klucz: "trusted-proxy", wartosc: "192.168.0.0/16", zrodlo: "flaga" },
      ] });
    }
    if (u.startsWith("/api/users")) return json({ users: [{ id: 1, login: "mariusz" }] });
    if (u.startsWith("/api/versions")) return json({ versions: [] });
    if (u.startsWith("/api/config")) return json({ yaml: "# Shy Garlic \u2014 homelab\n#\n# This file is the source of truth. The panel writes to it\n# without touching your comments or formatting.\n\ntitle: Homelab\n\ndefaults: \u0026defaults\n  check:\n    interval: 30s\n    timeout: 6s\n    expect: [200, 301, 302, 401, 403]\n\nappearance:\n  background:\n    preset: aurora\n    blur: 10\n    dim: 50\n  view: grid\n\nnotify:\n  threshold: 2\n\ngroups:\n  - name: Infrastructure\n    services:\n      - name: Proxmox\n        url: https://10.0.0.2:8006\n        icon: proxmox\n        check:\n          \u003c\u003c: *defaults\n          insecure: true   # self-signed certificate, on purpose\n" });
    return Promise.resolve(new Response("{}", { status: 503 }));
  };
})();
</` + `script>`

var wzorzecIkony = regexp.MustCompile(`src="/icon/([A-Za-z0-9._-]+)"`)
var wzorzecPickera = regexp.MustCompile(`(<div class="picker-tla" id="picker-tla"[^>]*?) hidden>`)

// kolejnyKlik klika element, który pojawia się dopiero po wcześniejszym
// kliknięciu — zakładkę w oknie, którego przed chwilą nie było w drzewie,
// albo przełącznik wewnątrz tej zakładki. Opóźnienie rozstrzyga kolejność.
func kolejnyKlik(selektor string, opóźnienieMs int) string {
	return `<script>
addEventListener("load", () => setTimeout(() => {
  const el = document.querySelector(` + "`" + selektor + "`" + `);
  if (el) el.click();
}, ` + strconv.Itoa(opóźnienieMs) + `));
</` + `script>`
}

// klikPodglądu — skrypt, który po załadowaniu klika wskazany element. Tak
// otwierają się okna rysowane przez JS (np. szczegóły usługi), których
// inaczej w statycznym podglądzie nie da się zobaczyć.
func klikPodglądu(selektor, ikona string) string {
	return `<script>
addEventListener("load", () => setTimeout(() => {
  const el = document.querySelector(` + "`" + selektor + "`" + `);
  if (el) el.click();
  setTimeout(() => {
    // pod file:// /icon/<nazwa> nie istnieje, więc app.js zdążył podmienić
    // ikonę na kafelek z literą — wstawiamy plik, który leży obok podglądu
    const box = document.querySelector("#detale-ikona");
    if (box) {
      box.textContent = "";
      const img = document.createElement("img");
      img.src = "` + ikona + `";
      img.width = 28; img.height = 28;
      box.append(img);
    }
  }, 200);
}, 120));
</` + `script>`
}

// doPodglądu przerabia stronę z serwera na plik do otwarcia spod file://.
func doPodglądu(t *testing.T, html string, ikony map[string]string, stan string, otwórzPicker bool) string {
	t.Helper()
	html = strings.ReplaceAll(html, `"/assets/`, `"assets/`)
	html = wzorzecIkony.ReplaceAllStringFunc(html, func(m string) string {
		n := wzorzecIkony.FindStringSubmatch(m)[1]
		if p, ok := ikony[n]; ok {
			return `src="` + filepath.ToSlash(p) + `"`
		}
		return m
	})
	if stan != "" {
		const pusty = "window.STAN_POCZATKOWY = {};"
		if !strings.Contains(html, pusty) {
			t.Fatal("nie znalazłem window.STAN_POCZATKOWY = {}; — podmiana stanu demo nie zadziała")
		}
		html = strings.Replace(html, pusty, "window.STAN_POCZATKOWY = "+stan+";", 1)
	}
	if stan != "" {
		const wpięcie = `<script src="assets/app.js" defer></script>`
		if !strings.Contains(html, wpięcie) {
			t.Fatal("nie znalazłem wpięcia app.js — zaślepka podglądu nie zadziała")
		}
		html = strings.Replace(html, wpięcie, zaślepkaPodglądu+"\n"+wpięcie, 1)
	}
	if otwórzPicker {
		przed := html
		html = wzorzecPickera.ReplaceAllString(html, "$1>")
		if html == przed {
			t.Fatal("nie udało się otworzyć pickera tła w podglądzie")
		}
	}
	return html
}

// TestPodgladStron zapisuje bin/podglad/*.html — zestaw do zrzutów ekranu.
func TestPodgladStron(t *testing.T) {
	if !*flagaPodglądu {
		t.Skip("bez -podglad nic nie zapisuję")
	}
	// footer pokazuje Version; w teście to "dev", a zrzuty mają pokazywać
	// to, co stoi na garlic.zatto.pl
	stara := Version
	Version = "0.11.0"
	t.Cleanup(func() { Version = stara })

	korzeń := korzeńRepo(t)
	wyjście := filepath.Join(korzeń, "bin", "podglad")
	if err := os.RemoveAll(wyjście); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wyjście, 0o755); err != nil {
		t.Fatal(err)
	}

	s := serwerNaConfigu(t, filepath.Join(korzeń, "examples", "homelab.yaml"),
		filepath.Join(korzeń, "bin", "cache-ikon"))
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	assetyNaDysk(t, wyjście)
	ikony := ikonyNaDysk(t, s, cfg, wyjście)

	b, err := json.Marshal(stanDemo(cfg))
	if err != nil {
		t.Fatal(err)
	}
	stan := string(b)

	// ! ton i preset idą przez config, a nie przez podmianę atrybutu w pliku:
	// app.js na starcie i tak ustawia data-ton z window.WYGLAD, więc ręcznie
	// zmieniony atrybut zostałby nadpisany przy pierwszym malowaniu.
	// ! bento ma sens dopiero, gdy karty mają różne rozmiary. examples/homelab.yaml
	// nie ustawia `size`, więc wariant bento dostaje je na czas renderu —
	// inaczej zrzut pokazywałby siatkę udającą bento.
	ustawRozmiary := func(włącz bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for gi := range s.cfg.Groups {
			for si := range s.cfg.Groups[gi].Services {
				r := ""
				if włącz {
					switch {
					case si == 0:
						r = "lg"
					case si <= 2:
						r = "md"
					default:
						r = "sm"
					}
				}
				s.cfg.Groups[gi].Services[si].Size = r
			}
		}
	}

	warianty := []struct {
		nazwa    string
		preset   string
		ton      string
		widok    string
		picker   bool
		rozmiary bool
		klik     string
		klik2    string
		klik3    string
	}{
		{nazwa: "index-grid-ciemny", preset: "aurora", ton: "dark", widok: "grid"},
		{nazwa: "index-grid-jasny", preset: "dawn", ton: "light", widok: "grid"},
		{nazwa: "index-bento-ciemny", preset: "nebula", ton: "dark", widok: "bento", rozmiary: true},
		{nazwa: "index-lista-ciemny", preset: "graphite", ton: "dark", widok: "list"},
		{nazwa: "index-lista-jasny", preset: "fog", ton: "light", widok: "list"},
		{nazwa: "index-picker-ciemny", preset: "aurora", ton: "dark", widok: "grid", picker: true},
		{nazwa: "index-picker-jasny", preset: "dawn", ton: "light", widok: "grid", picker: true},
		{nazwa: "index-ustawienia-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: "#ustawienia"},
		{nazwa: "index-ustawienia-jasny", preset: "dawn", ton: "light", widok: "grid",
			klik: "#ustawienia"},
		{nazwa: "index-edycja-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: "#tryb-edycji"},
		{nazwa: "index-powiadomienia-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: "#ustawienia", klik2: `[aria-controls="panel-powiadomienia"]`},
		{nazwa: "index-instancja-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: "#ustawienia", klik2: `[aria-controls="panel-instancja"]`},
		{nazwa: "index-plik-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: "#ustawienia", klik2: `[aria-controls="panel-plik"]`},
		{nazwa: "index-plik-jasny", preset: "dawn", ton: "light", widok: "grid",
			klik: "#ustawienia", klik2: `[aria-controls="panel-plik"]`},
		{nazwa: "index-plik-pelny-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: "#ustawienia", klik2: `[aria-controls="panel-plik"]`, klik3: "#yaml-pelny-ekran"},
		{nazwa: "index-detale-ciemny", preset: "aurora", ton: "dark", widok: "grid",
			klik: ".karta .szczegoly"},
		{nazwa: "index-detale-jasny", preset: "dawn", ton: "light", widok: "grid",
			klik: ".karta .szczegoly"},
	}
	for _, w := range warianty {
		s.mu.Lock()
		s.cfg.Appearance = config.Appearance{
			Background: config.Background{Preset: w.preset, Blur: 10, Dim: config.DefaultDim, Tone: w.ton},
			View:       w.widok,
		}
		s.mu.Unlock()
		ustawRozmiary(w.rozmiary)

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), kluczUżytkownika,
			&store.User{ID: 1, Login: "admin"}))
		rec := httptest.NewRecorder()
		s.handleIndex(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: kod = %d", w.nazwa, rec.Code)
		}
		html := doPodglądu(t, rec.Body.String(), ikony, stan, w.picker)
		if w.klik != "" {
			html = strings.Replace(html, "</body>",
				klikPodglądu(w.klik, ikony["proxmox"])+"\n</body>", 1)
		}
		if w.klik2 != "" {
			html = strings.Replace(html, "</body>", kolejnyKlik(w.klik2, 400)+"\n</body>", 1)
		}
		if w.klik3 != "" {
			html = strings.Replace(html, "</body>", kolejnyKlik(w.klik3, 700)+"\n</body>", 1)
		}
		if err := os.WriteFile(filepath.Join(wyjście, w.nazwa+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// brama: preset bierze z configu, ale nigdy tapety — dlatego osobno
	// ustawiamy jasny i ciemny, tak jak zobaczy je niezalogowany
	bramy := []struct {
		nazwa  string
		preset string
		ton    string
		setup  bool
	}{
		{"login-ciemny", "aurora", "dark", false},
		{"login-jasny", "dawn", "light", false},
		{"login-setup-ciemny", "aurora", "dark", true},
	}
	for _, w := range bramy {
		s.mu.Lock()
		s.cfg.Appearance.Background.Preset = w.preset
		s.cfg.Appearance.Background.Tone = w.ton
		s.mu.Unlock()

		var buf bytes.Buffer
		// EN jak w domyślnym żądaniu bez Accept-Language — reszta zrzutów też
		// jest po angielsku, a oba README dzielą te same obrazki
		dane := s.zJęzykiem(i18n.EN, widokLogowania{Setup: w.setup, Wersja: "0.9.0",
			Tytuł: "Sign in"})
		if err := s.tmpl.ExecuteTemplate(&buf, "login.html", dane); err != nil {
			t.Fatal(err)
		}
		html := doPodglądu(t, buf.String(), ikony, "", false)
		if err := os.WriteFile(filepath.Join(wyjście, w.nazwa+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("podgląd zapisany w %s (ikon: %d)", wyjście, len(ikony))
}

// zakazaneIdiomy — klasy skasowane razem z wprowadzeniem bazy .btn. Idiomy
// przycisku wracają same, jeśli ich nie pilnować: test trzyma listę, żeby
// nawrót był czerwonym testem, a nie uwagą w review pół roku później.
var zakazaneIdiomy = []string{
	`class="przycisk`, `class="ikonka`, `class="grupa-akcja`,
	`class="menu-pozycja`, `class="picker-podmien`, `class="picker-domyslne`,
	`class="picker-zapisz`, `class="wybrany`,
}

// bezBazy — komponenty, które są <button>ami, ale nie są przyciskami w sensie
// wizualnym: .chip to plakietka z licznikiem, .szczegoly to pasek wykresu
// zajmujący całą szerokość karty w płaskim DOM-ie siatki. Obu nadanie .btn
// popsułoby układ, a nie ujednoliciło wygląd.
var bezBazy = []string{`class="chip`, `class="szczegoly`}

func TestSzablonyBezSkasowanychIdiomów(t *testing.T) {
	for _, nazwa := range []string{"index.html", "login.html"} {
		treść, err := files.ReadFile("templates/" + nazwa)
		if err != nil {
			t.Fatalf("czytam %s: %v", nazwa, err)
		}
		for _, z := range zakazaneIdiomy {
			if bytes.Contains(treść, []byte(z)) {
				t.Errorf("%s: wrócił skasowany idiom %q", nazwa, z)
			}
		}
	}
}

// Każdy <button> w szablonie ma nieść .btn. Bezklasowy przycisk to dokładnie
// ten błąd, który siedział w login.html i w akcjach karty.
func TestKażdyPrzyciskMaBazę(t *testing.T) {
	re := regexp.MustCompile(`(?s)<button\b[^>]*>`)
	for _, nazwa := range []string{"index.html", "login.html"} {
		treść, err := files.ReadFile("templates/" + nazwa)
		if err != nil {
			t.Fatalf("czytam %s: %v", nazwa, err)
		}
	tagi:
		for _, tag := range re.FindAllString(string(treść), -1) {
			if strings.Contains(tag, `"btn`) || strings.Contains(tag, ` btn`) {
				continue
			}
			for _, w := range bezBazy {
				if strings.Contains(tag, w) {
					continue tagi
				}
			}
			t.Errorf("%s: przycisk bez klasy .btn: %s", nazwa, strings.Join(strings.Fields(tag), " "))
		}
	}
}

// Okno ustawień ma sześć zakładek, nie siedem: „Usługi" z planu byłaby pusta,
// bo usługi edytuje się na miejscu w panelu. Drugi edytor tego samego to
// dokładnie ten problem, który ta przebudowa likwiduje.
func TestUstawieniaMająZakładki(t *testing.T) {
	treść, err := files.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("czytam index.html: %v", err)
	}
	for _, id := range []string{
		"panel-wyglad", "panel-powiadomienia", "panel-konta",
		"panel-historia", "panel-plik", "panel-instancja",
	} {
		if !bytes.Contains(treść, []byte(`id="`+id+`"`)) {
			t.Errorf("brak panelu %q", id)
		}
	}
	if !bytes.Contains(treść, []byte(`role="tablist"`)) {
		t.Error("zakładki mają być tablistem — inaczej czytnik ekranu widzi rząd przycisków bez związku")
	}
	// Każdy tab musi wskazywać panel, który istnieje.
	re := regexp.MustCompile(`role="tab"[^>]*aria-controls="([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(treść), -1) {
		if !bytes.Contains(treść, []byte(`id="`+m[1]+`"`)) {
			t.Errorf("zakładka wskazuje na nieistniejący panel %q", m[1])
		}
	}
}

// Edycja tytułu i zmiana kolejności (SHYG-39/40): uchwyty, pole tytułu i
// region aria-live muszą być w HTML-u — app.js szuka ich po tych selektorach.
func TestStronaMaUchwytyITytułDoEdycji(t *testing.T) {
	s := serwerTestowy(t)
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "index.html", pełnyWidokStrony()); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, chcę := range []string{
		`id="tytul-panelu"`, `id="tytul-edytuj"`, `id="blad-tytulu"`, `id="ogloszenia"`,
		`data-akcja="uchwyt" data-typ="usluga"`, `data-akcja="uchwyt" data-typ="grupa"`,
		`aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown"`,
	} {
		if !strings.Contains(html, chcę) {
			t.Errorf("w HTML-u brak %s", chcę)
		}
	}
	// jeden uchwyt na kartę i jeden na grupę
	if n := strings.Count(html, `data-typ="usluga"`); n != 1 {
		t.Errorf("uchwytów usług = %d, chcę 1 (jedna karta)", n)
	}
	if n := strings.Count(html, `data-typ="grupa"`); n != 1 {
		t.Errorf("uchwytów grup = %d, chcę 1", n)
	}
}

// Każdy tekst z nowych funkcji ma PL i EN — brak jednego dałby klucz zamiast
// napisu w interfejsie.
func TestTekstyKolejnościITytułuMająObaJęzyki(t *testing.T) {
	klucze := []string{
		"zmien_tytul", "tytul_pole_aria", "uchwyt_usluga", "uchwyt_grupa", "pozycja_usluga",
		"pozycja_grupa", "przenosz_filtr", "kolejnosc_ok", "blad_tytul_pusty",
		"blad_tytul_dlugi", "blad_tytul_znaki", "blad_przenoszenie", "opis_tytul",
		"opis_kolejnosc_uslugi", "opis_kolejnosc_grupy",
	}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range klucze {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: brak tekstu %q", l, k)
			}
		}
	}
}
