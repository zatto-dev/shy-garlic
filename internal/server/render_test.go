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

var presetAttr = regexp.MustCompile(`data-preset="([^"]*)"`)

// checkTemplateOutput guards three things at once: that no value from outside the allowlist
// leaked into an attribute, that the template has no inline styles
// (CSP has style-src 'self', so they would be dead) and that there is no <style> tag.
func checkTemplateOutput(t *testing.T, html string) {
	t.Helper()
	for _, m := range presetAttr.FindAllStringSubmatch(html, -1) {
		if !config.Allowed(m[1], config.BackgroundPresets) {
			t.Errorf("data-preset=%q — a value outside the allowlist in the attribute", m[1])
		}
	}
	if strings.Contains(html, "style=") {
		t.Error("the output has a style= attribute — CSP style-src 'self' will block it")
	}
	if strings.Contains(html, "<style") {
		t.Error("the output has a <style> tag — the same, CSP will block it")
	}
}

func fullPageView() pageView {
	d := pageView{
		Title: "Homelab", Version: "test", User: "admin", IconCount: 3363,
		State: template.JS("{}"), Lang: "pl",
		Appearance:     appearanceView{Preset: "aurora", Blur: 10, Tone: "dark", View: "grid"},
		AppearanceJSON: template.JS(`{"preset":"aurora","image":"","blur":10,"tone":"dark","view":"grid"}`),
		CanWrite:       true, BackgroundLimit: BackgroundLimitMB,
		Groups: []groupView{{Name: "Media", Services: []serviceView{{
			Key: "Media/Jellyfin", Group: "Media", Name: "Jellyfin",
			URL: "http://jellyfin", Host: "jellyfin", IconName: "jellyfin",
			Icon: iconRef{Src: "/icon/jellyfin", Letter: "J"}, Size: "md",
		}}}},
	}
	d.T, d.TJSON, d.SecondLang, d.SecondLangName, d.SecondLangShort = fillLang(i18n.PL)
	d.SearchIconsPlaceholder = "Szukaj ikony"
	return d
}

// TestTemplatesExecute catches a typo in {{.Field}} that ParseFS does not see
// — the error only comes out at execution, i.e. at the user's.
func TestTemplatesExecute(t *testing.T) {
	s := testServer(t)
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "index.html", fullPageView()); err != nil {
		t.Fatalf("index.html: %v", err)
	}
	checkTemplateOutput(t, buf.String())

	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		for _, setup := range []bool{false, true} {
			buf.Reset()
			data := s.withLang(l, loginView{Setup: setup, Version: "test", Title: "Logowanie"})
			if err := s.tmpl.ExecuteTemplate(&buf, "login.html", data); err != nil {
				t.Fatalf("login.html (%s, setup=%v): %v", l, setup, err)
			}
			checkTemplateOutput(t, buf.String())
		}
	}
}

func TestHandleIndexRendersWithoutPanic(t *testing.T) {
	variants := []struct {
		name string
		set  func(*config.Config)
	}{
		{"from file", func(*config.Config) {}},
		{"empty appearance", func(c *config.Config) { c.Appearance = config.Appearance{} }},
		{"appearance full of junk", func(c *config.Config) {
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
	for _, w := range variants {
		s := testServer(t)
		s.mu.Lock()
		w.set(s.cfg)
		s.mu.Unlock()

		rec := httptest.NewRecorder()
		s.handleIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", w.name, rec.Code)
		}
		body := rec.Body.String()
		checkTemplateOutput(t, body)
		if strings.Contains(body, "onload=") || strings.Contains(body, "<script>alert") {
			t.Errorf("%s: junk from the config came out unescaped", w.name)
		}
	}
}

func TestLoginDoesNotLeakWallpaper(t *testing.T) {
	s := testServer(t)
	s.mu.Lock()
	s.cfg.Appearance.Background.Preset = "photo"
	s.cfg.Appearance.Background.Image = "prywatne-zdjecie.jpg"
	s.mu.Unlock()

	w := s.gateAppearance()
	if w.Preset == config.PresetPhoto || w.Image != "" {
		t.Fatalf("the gate got a wallpaper: %+v", w)
	}

	var buf bytes.Buffer
	data := s.withLang(i18n.PL, loginView{Version: "test", Title: "Logowanie"})
	if err := s.tmpl.ExecuteTemplate(&buf, "login.html", data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "/bg/") || strings.Contains(buf.String(), "prywatne-zdjecie") {
		t.Fatal("the wallpaper name leaked onto the login page")
	}
	checkTemplateOutput(t, buf.String())
}

// ══════════════════════════════════════════════════════════════════════════════
// Offline preview
//
// The panel sits behind a login, so screenshots are taken from files, not from a
// browser let onto the server. This test renders the real templates with the
// real handler on examples/homelab.yaml and writes ready pages to
// bin/podglad/ together with assets and icons. The /assets/… and /icon/… paths
// become relative so everything works from file:// without a single request.
//
// ! Writing is behind a flag — plain `./x test` (i.e. `go test ./...`) does not
// pass the flag, so nothing is produced. To run the preview: `./x podglad`.
// ══════════════════════════════════════════════════════════════════════════════

var previewFlag = flag.Bool("podglad", false,
	"write the rendered pages to bin/podglad/ (preview without a server and without login)")

// repoRoot looks for the directory with go.mod — the test runs in internal/server, and the files
// must land in bin/podglad/ in the root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repo root (go.mod)")
		}
		dir = parent
	}
}

// serverOnConfig sets the panel up on the given YAML file.
//
// ! It deliberately does not call reload() — SetTargets would start the monitor, and examples/homelab.yaml
// has real addresses from a home network. The preview is meant to render, not to scan.
func serverOnConfig(t *testing.T, source, iconCache string) *Server {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "shygarlic.yaml")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(path, st, Options{CacheDir: iconCache, BackgroundDir: filepath.Join(dir, "bg")})
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	cfg, err := config.Load(raw)
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

// demoState — health results fed into the preview. The monitor does not run in the test
// (see serverOnConfig), and a page with nothing but "—" shows neither the status
// colors nor the counters in the bar. The values are made up and only for the preview.
func demoState(cfg *config.Config) map[string]health.Result {
	type entry struct {
		state health.State
		ms    int64
		code  int
		err   string
	}
	exceptions := map[string]entry{
		"Media/Immich":                  {health.StateDegraded, 812, 502, ""},
		"Tools/Paperless-ngx":           {health.StateDown, 0, 0, "connection refused"},
		"Monitoring/Grafana":            {health.StateUnknown, 0, 0, ""},
		"Infrastructure/Home Assistant": {health.StateUp, 118, 200, ""},
	}
	defaults := []int64{34, 47, 61, 72, 96, 128, 153}
	out := map[string]health.Result{}
	i := 0
	for _, g := range cfg.Groups {
		for _, svc := range g.Services {
			k := key(g.Name, svc.Name)
			when := time.Now().Add(-time.Duration(7+3*i) * time.Second)
			if w, ok := exceptions[k]; ok {
				out[k] = health.Result{State: w.state, MS: w.ms, Code: w.code, Err: w.err, At: when}
			} else {
				out[k] = health.Result{State: health.StateUp, MS: defaults[i%len(defaults)],
					Code: 200, At: when}
			}
			i++
		}
	}
	return out
}

// iconsOnDisk pulls the icons used in the config into <dir>/icon/ and returns a map
// name → relative path. Without it the cards in the preview would be empty: /icon/…
// is served by the server, which does not exist under file://.
func iconsOnDisk(t *testing.T, s *Server, cfg *config.Config, dir string) map[string]string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "icon"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.icons.Refresh(context.Background())
	out := map[string]string{}
	fetch := func(n string) {
		n = strings.TrimSuffix(strings.TrimSuffix(n, ".svg"), ".png")
		if n == "" || strings.Contains(n, "://") {
			return
		}
		if _, have := out[n]; have {
			return
		}
		data, kind, err := s.icons.Read(context.Background(), n)
		if err != nil {
			t.Logf("icon %q unavailable (%v) — the card will show a letter", n, err)
			return
		}
		ext := "svg"
		if kind == "image/png" {
			ext = "png"
		}
		file := filepath.Join("icon", n+"."+ext)
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
		out[n] = file
	}
	for _, g := range cfg.Groups {
		for _, svc := range g.Services {
			n := strings.TrimSuffix(strings.TrimSuffix(svc.Icon, ".svg"), ".png")
			fetch(n)
			// an icon with separate light/dark variants renders as TWO
			// <img>, so the offline preview must have both files on disk, not
			// only the base name from the config.
			if dark, light := s.icons.Variants(n); dark != "" {
				fetch(dark)
				fetch(light)
			}
		}
	}
	return out
}

// assetsOnDisk dumps the embed.FS to <dir>/assets/. style.css gets relative
// paths so that @font-face hits the files and not the disk root.
func assetsOnDisk(t *testing.T, dir string) {
	t.Helper()
	err := fs.WalkDir(files, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".css") {
			data = []byte(strings.ReplaceAll(string(data), `url("/assets/`, `url("`))
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// previewStub — the only thing in the preview that does not exist in production.
// Under file:// there is neither SSE nor /api/*, so without it the footer would immediately light up
// "no connection" and the charts on the cards would stay empty — the screenshot
// would show an outage, not the appearance. The history data is synthetic and computed
// from the injected demo state.
const previewStub = `<script>
/* OFFLINE PREVIEW — does not reach production (test render_test.go) */
(() => {
  class StrumienPodgladu { close() {} addEventListener() {} }
  window.EventSource = StrumienPodgladu;

  const historia = (klucz) => {
    const teraz = (window.INITIAL_STATE || {})[klucz] || { state: "up", ms: 60 };
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
      const el = document.querySelector('.card[data-key="' + klucz + '"]');
      const h = historia(klucz);
      const teraz = (window.INITIAL_STATE || {})[klucz] || { state: "up", ms: 60 };
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
        key: klucz, name: (el && el.dataset.name) || klucz,
        url: (el && el.dataset.address) || "", icon: (el && el.dataset.icon) || "",
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
      return json({ enabled: true, webhook: "https://discord.com/•••", source: "database", threshold: 2 });
    }
    if (u.startsWith("/api/instance")) {
      return json({ options: [
        { key: "addr", value: ":8080", source: "default" },
        { key: "config", value: "/etc/shygarlic/shygarlic.yaml", source: "env" },
        { key: "data", value: "/data/shygarlic.db", source: "env" },
        { key: "backgrounds", value: "/data/bg", source: "default" },
        { key: "docker", value: "/var/run/docker.sock", source: "flag" },
        { key: "trusted-proxy", value: "192.168.0.0/16", source: "flag" },
      ] });
    }
    if (u.startsWith("/api/users")) return json({ users: [{ id: 1, login: "mariusz" }] });
    if (u.startsWith("/api/versions")) return json({ versions: [] });
    if (u.startsWith("/api/config")) return json({ yaml: "# Shy Garlic \u2014 homelab\n#\n# This file is the source of truth. The panel writes to it\n# without touching your comments or formatting.\n\ntitle: Homelab\n\ndefaults: \u0026defaults\n  check:\n    interval: 30s\n    timeout: 6s\n    expect: [200, 301, 302, 401, 403]\n\nappearance:\n  background:\n    preset: aurora\n    blur: 10\n    dim: 50\n  view: grid\n\nnotify:\n  threshold: 2\n\ngroups:\n  - name: Infrastructure\n    services:\n      - name: Proxmox\n        url: https://10.0.0.2:8006\n        icon: proxmox\n        check:\n          \u003c\u003c: *defaults\n          insecure: true   # self-signed certificate, on purpose\n" });
    return Promise.resolve(new Response("{}", { status: 503 }));
  };
})();
</` + `script>`

var iconPattern = regexp.MustCompile(`src="/icon/([A-Za-z0-9._-]+)"`)
var pickerPattern = regexp.MustCompile(`(<div class="picker-backgrounds" id="picker-backgrounds"[^>]*?) hidden>`)

// nextClick clicks an element that appears only after an earlier
// click — a tab in a window that a moment ago was not in the tree,
// or a switch inside that tab. The delay decides the order.
func nextClick(selector string, delayMs int) string {
	return `<script>
addEventListener("load", () => setTimeout(() => {
  const el = document.querySelector(` + "`" + selector + "`" + `);
  if (el) el.click();
}, ` + strconv.Itoa(delayMs) + `));
</` + `script>`
}

// previewClick — a script that clicks the given element after loading. This is how
// windows drawn by JS (e.g. service details) open, which otherwise
// cannot be seen in a static preview.
func previewClick(selector, icon string) string {
	return `<script>
addEventListener("load", () => setTimeout(() => {
  const el = document.querySelector(` + "`" + selector + "`" + `);
  if (el) el.click();
  setTimeout(() => {
    // under file:// /icon/<name> does not exist, so app.js already replaced
    // the icon with a letter tile — we insert the file that lies next to the preview
    const box = document.querySelector("#detail-icon");
    if (box) {
      box.textContent = "";
      const img = document.createElement("img");
      img.src = "` + icon + `";
      img.width = 28; img.height = 28;
      box.append(img);
    }
  }, 200);
}, 120));
</` + `script>`
}

// toPreview turns a page from the server into a file to open from file://.
func toPreview(t *testing.T, html string, icons map[string]string, state string, openPicker bool) string {
	t.Helper()
	html = strings.ReplaceAll(html, `"/assets/`, `"assets/`)
	html = iconPattern.ReplaceAllStringFunc(html, func(m string) string {
		n := iconPattern.FindStringSubmatch(m)[1]
		if p, ok := icons[n]; ok {
			return `src="` + filepath.ToSlash(p) + `"`
		}
		return m
	})
	if state != "" {
		const empty = "window.INITIAL_STATE = {};"
		if !strings.Contains(html, empty) {
			t.Fatal("could not find window.INITIAL_STATE = {}; — swapping in the demo state will not work")
		}
		html = strings.Replace(html, empty, "window.INITIAL_STATE = "+state+";", 1)
	}
	if state != "" {
		const hook = `<script src="assets/app.js" defer></script>`
		if !strings.Contains(html, hook) {
			t.Fatal("could not find the app.js hook — the preview stub will not work")
		}
		html = strings.Replace(html, hook, previewStub+"\n"+hook, 1)
	}
	if openPicker {
		before := html
		html = pickerPattern.ReplaceAllString(html, "$1>")
		if html == before {
			t.Fatal("could not open the background picker in the preview")
		}
	}
	return html
}

// TestPreviewPages writes bin/podglad/*.html — a set for screenshots.
func TestPreviewPages(t *testing.T) {
	if !*previewFlag {
		t.Skip("without -podglad I write nothing")
	}
	// the footer shows Version; in the test it is "dev", and the screenshots should show
	// what stands on garlic.zatto.pl
	old := Version
	Version = "0.11.0"
	t.Cleanup(func() { Version = old })

	root := repoRoot(t)
	output := filepath.Join(root, "bin", "podglad")
	if err := os.RemoveAll(output); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}

	s := serverOnConfig(t, filepath.Join(root, "examples", "homelab.yaml"),
		filepath.Join(root, "bin", "cache-ikon"))
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	assetsOnDisk(t, output)
	icons := iconsOnDisk(t, s, cfg, output)

	b, err := json.Marshal(demoState(cfg))
	if err != nil {
		t.Fatal(err)
	}
	state := string(b)

	// ! tone and preset go through the config, not through swapping an attribute in the file:
	// app.js sets data-tone from window.APPEARANCE at start anyway, so a
	// hand-changed attribute would be overwritten at the first paint.
	// ! bento only makes sense when the cards have different sizes. examples/homelab.yaml
	// does not set `size`, so the bento variant gets them for the time of rendering —
	// otherwise the screenshot would show a grid pretending to be bento.
	setSizes := func(enabled bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for gi := range s.cfg.Groups {
			for si := range s.cfg.Groups[gi].Services {
				r := ""
				if enabled {
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

	variants := []struct {
		name   string
		preset string
		tone   string
		view   string
		picker bool
		sizes  bool
		click  string
		click2 string
		click3 string
	}{
		{name: "index-grid-ciemny", preset: "aurora", tone: "dark", view: "grid"},
		{name: "index-grid-jasny", preset: "dawn", tone: "light", view: "grid"},
		{name: "index-bento-ciemny", preset: "nebula", tone: "dark", view: "bento", sizes: true},
		{name: "index-lista-ciemny", preset: "graphite", tone: "dark", view: "list"},
		{name: "index-lista-jasny", preset: "fog", tone: "light", view: "list"},
		{name: "index-picker-ciemny", preset: "aurora", tone: "dark", view: "grid", picker: true},
		{name: "index-picker-jasny", preset: "dawn", tone: "light", view: "grid", picker: true},
		{name: "index-ustawienia-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: "#settings"},
		{name: "index-ustawienia-jasny", preset: "dawn", tone: "light", view: "grid",
			click: "#settings"},
		{name: "index-edycja-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: "#edit-mode"},
		{name: "index-powiadomienia-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: "#settings", click2: `[aria-controls="panel-notifications"]`},
		{name: "index-instancja-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: "#settings", click2: `[aria-controls="panel-instance"]`},
		{name: "index-plik-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: "#settings", click2: `[aria-controls="panel-file"]`},
		{name: "index-plik-jasny", preset: "dawn", tone: "light", view: "grid",
			click: "#settings", click2: `[aria-controls="panel-file"]`},
		{name: "index-plik-pelny-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: "#settings", click2: `[aria-controls="panel-file"]`, click3: "#yaml-fullscreen"},
		{name: "index-detale-ciemny", preset: "aurora", tone: "dark", view: "grid",
			click: ".card .details"},
		{name: "index-detale-jasny", preset: "dawn", tone: "light", view: "grid",
			click: ".card .details"},
	}
	for _, w := range variants {
		s.mu.Lock()
		s.cfg.Appearance = config.Appearance{
			Background: config.Background{Preset: w.preset, Blur: 10, Dim: config.DefaultDim, Tone: w.tone},
			View:       w.view,
		}
		s.mu.Unlock()
		setSizes(w.sizes)

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), userKey,
			&store.User{ID: 1, Login: "admin"}))
		rec := httptest.NewRecorder()
		s.handleIndex(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", w.name, rec.Code)
		}
		html := toPreview(t, rec.Body.String(), icons, state, w.picker)
		if w.click != "" {
			html = strings.Replace(html, "</body>",
				previewClick(w.click, icons["proxmox"])+"\n</body>", 1)
		}
		if w.click2 != "" {
			html = strings.Replace(html, "</body>", nextClick(w.click2, 400)+"\n</body>", 1)
		}
		if w.click3 != "" {
			html = strings.Replace(html, "</body>", nextClick(w.click3, 700)+"\n</body>", 1)
		}
		if err := os.WriteFile(filepath.Join(output, w.name+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// gate: the preset comes from the config, but never the wallpaper — that is why we set
	// light and dark separately, the way a logged-out user will see them
	gate := []struct {
		name   string
		preset string
		tone   string
		setup  bool
	}{
		{"login-ciemny", "aurora", "dark", false},
		{"login-jasny", "dawn", "light", false},
		{"login-setup-ciemny", "aurora", "dark", true},
	}
	for _, w := range gate {
		s.mu.Lock()
		s.cfg.Appearance.Background.Preset = w.preset
		s.cfg.Appearance.Background.Tone = w.tone
		s.mu.Unlock()

		var buf bytes.Buffer
		// EN as in a default request without Accept-Language — the other screenshots
		// are also in English, and both READMEs share the same pictures
		data := s.withLang(i18n.EN, loginView{Setup: w.setup, Version: "0.9.0",
			Title: "Sign in"})
		if err := s.tmpl.ExecuteTemplate(&buf, "login.html", data); err != nil {
			t.Fatal(err)
		}
		html := toPreview(t, buf.String(), icons, "", false)
		if err := os.WriteFile(filepath.Join(output, w.name+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Logf("preview written to %s (icons: %d)", output, len(icons))
}

// forbiddenIdioms — classes removed together with the introduction of the .btn base. The button
// idioms come back on their own if nobody guards them: the test holds the list so
// that a regression is a red test, not a review remark half a year later.
var forbiddenIdioms = []string{
	`class="button`, `class="mini-icon`, `class="group-action`,
	`class="menu-item`, `class="picker-replace`, `class="picker-default`,
	`class="picker-save`, `class="selected`,
}

// noBase — components that are <button>s but are not buttons in the visual
// sense: .chip is a badge with a counter, .details is the chart bar
// taking the whole card width in the grid's flat DOM. Giving either .btn
// would break the layout, not unify the appearance.
var noDB = []string{`class="chip`, `class="details`}

func TestTemplatesWithoutRemovedIdioms(t *testing.T) {
	for _, name := range []string{"index.html", "login.html"} {
		content, err := files.ReadFile("templates/" + name)
		if err != nil {
			t.Fatalf("czytam %s: %v", name, err)
		}
		for _, z := range forbiddenIdioms {
			if bytes.Contains(content, []byte(z)) {
				t.Errorf("%s: a removed idiom came back %q", name, z)
			}
		}
	}
}

// Every <button> in the template must carry .btn. A class-less button is exactly
// the bug that sat in login.html and in the card actions.
func TestEveryButtonHasBase(t *testing.T) {
	re := regexp.MustCompile(`(?s)<button\b[^>]*>`)
	for _, name := range []string{"index.html", "login.html"} {
		content, err := files.ReadFile("templates/" + name)
		if err != nil {
			t.Fatalf("czytam %s: %v", name, err)
		}
	tags:
		for _, tag := range re.FindAllString(string(content), -1) {
			if strings.Contains(tag, `"btn`) || strings.Contains(tag, ` btn`) {
				continue
			}
			for _, w := range noDB {
				if strings.Contains(tag, w) {
					continue tags
				}
			}
			t.Errorf("%s: button without the .btn class: %s", name, strings.Join(strings.Fields(tag), " "))
		}
	}
}

// The settings window has six tabs, not seven: "Services" from the plan would be empty,
// because services are edited in place in the panel. A second editor of the same thing is
// exactly the problem this rebuild removes.
func TestSettingsHaveTabs(t *testing.T) {
	content, err := files.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("czytam index.html: %v", err)
	}
	for _, id := range []string{
		"panel-appearance", "panel-notifications", "panel-account",
		"panel-history", "panel-file", "panel-instance",
	} {
		if !bytes.Contains(content, []byte(`id="`+id+`"`)) {
			t.Errorf("missing panel %q", id)
		}
	}
	if !bytes.Contains(content, []byte(`role="tablist"`)) {
		t.Error("the tabs must be a tablist — otherwise a screen reader sees a row of unrelated buttons")
	}
	// Every tab must point to a panel that exists.
	re := regexp.MustCompile(`role="tab"[^>]*aria-controls="([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(content), -1) {
		if !bytes.Contains(content, []byte(`id="`+m[1]+`"`)) {
			t.Errorf("a tab points to a non-existent panel %q", m[1])
		}
	}
}

// Title editing and reordering (SHYG-39/40): handles, the title field and
// the aria-live region must be in the HTML — app.js looks for them by these selectors.
func TestPageHasHandlesAndTitleForEditing(t *testing.T) {
	s := testServer(t)
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "index.html", fullPageView()); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`id="panel-title"`, `id="edit-title"`, `id="title-error"`, `id="announcements"`,
		`data-action="handle" data-type="service"`, `data-action="handle" data-type="group"`,
		`aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %s", want)
		}
	}
	// one handle per card and one per group
	if n := strings.Count(html, `data-type="service"`); n != 1 {
		t.Errorf("service handles = %d, want 1 (one card)", n)
	}
	if n := strings.Count(html, `data-type="group"`); n != 1 {
		t.Errorf("group handles = %d, want 1", n)
	}
}

// Every text of the new features has PL and EN — a missing one would give a key instead of
// a label in the interface.
func TestOrderAndTitleTextsHaveBothLanguages(t *testing.T) {
	keys := []string{
		"edit_title", "title_field_aria", "handle_service", "handle_group", "position_service",
		"position_group", "move_filter_active", "order_saved", "error_title_empty",
		"error_title_long", "error_title_chars", "error_move", "desc_title_changed",
		"desc_service_reordered", "desc_group_reordered",
	}
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range keys {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: missing text %q", l, k)
			}
		}
	}
}
