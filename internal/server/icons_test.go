package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

// fixtureIcon mirrors one entry of icons.zatto.dev/index.json.
type fixtureIcon struct {
	Slug     string
	Name     string
	Aliases  []string
	Format   string
	Variants map[string]string
}

func fixtureIcons() []fixtureIcon {
	both := map[string]string{"dark": "svg", "light": "svg"}
	return []fixtureIcon{
		{"8311", "8311", []string{"8311 community"}, "svg", nil},
		{"13-feet-ladder", "13 Feet Ladder", nil, "png", nil},
		{"1password", "1Password", []string{"Passwords"}, "svg", map[string]string{"dark": "svg"}}, // half a pair
		{"20i", "20i", []string{"twenty", "twenty-i"}, "svg", both},
		{"adguard-home", "AdGuard Home", []string{"adguard"}, "svg", nil},
		{"dagster", "Dagster", nil, "svg", both},
		{"grafana", "Grafana", nil, "svg", nil},
		{"home-assistant", "Home Assistant", []string{"hass"}, "svg", both},
		{"homepage", "Homepage", nil, "svg", nil},
		{"immich", "Immich", nil, "svg", nil},
		{"jellyfin", "Jellyfin", nil, "svg", nil},
		{"llama-cpp", "llama.cpp", nil, "svg", both},
		{"nextcloud", "Nextcloud", nil, "svg", nil},
		{"nginx-proxy-manager", "Nginx Proxy Manager", []string{"npm"}, "svg", nil},
		{"paperless-ngx", "Paperless-ngx", []string{"paperless"}, "svg", nil},
		{"pi-hole", "Pi-hole", []string{"pihole"}, "svg", nil},
		{"plex", "Plex", nil, "svg", nil},
		{"portainer", "Portainer", nil, "png", nil},
		{"prometheus", "Prometheus", nil, "svg", nil},
		{"proxmox", "Proxmox", nil, "svg", nil},
		{"radarr", "Radarr", nil, "svg", nil},
		{"sonarr", "Sonarr", nil, "svg", nil},
		{"tailscale", "Tailscale", nil, "svg", nil},
		{"traefik", "Traefik", nil, "svg", nil},
		{"truenas-core", "TrueNAS Core", nil, "svg", nil},
		{"truenas-scale", "TrueNAS Scale", nil, "svg", nil},
		{"uptime-kuma", "Uptime Kuma", nil, "svg", nil},
		{"vaultwarden", "Vaultwarden", []string{"Bitwarden"}, "svg", nil},
		{"wireguard", "WireGuard", nil, "svg", nil},
	}
}

// fixtureAliasMap is what the server publishes; it also carries two rows the importer
// must drop: one that shadows a real slug and one that points nowhere.
func fixtureAliasMap() map[string]string {
	return map[string]string{
		"passwords": "1password", "twenty": "20i", "twenty-i": "20i", "bitwarden": "vaultwarden",
		"pihole": "pi-hole", "npm": "nginx-proxy-manager", "paperless": "paperless-ngx", "hass": "home-assistant",
		"adguard": "adguard-home",
		"plex":    "jellyfin", // shadows a real slug
		"ghost":   "does-not-exist",
	}
}

func indexJSON(t testing.TB, icons []fixtureIcon, aliases map[string]string, schema int) string {
	t.Helper()
	m := map[string]any{}
	im := map[string]any{}
	for _, i := range icons {
		v := i.Variants
		if v == nil {
			v = map[string]string{}
		}
		a := i.Aliases
		if a == nil {
			a = []string{}
		}
		im[i.Slug] = map[string]any{"name": i.Name, "aliases": a, "categories": []string{"x"}, "format": i.Format,
			"variants": v, "source": "homarr", "license": "Apache-2.0", "size": 100}
	}
	m["schema"] = schema
	m["generated"] = "2026-10-17T22:31:49.910Z"
	m["note"] = "test"
	m["sources"] = map[string]any{"homarr": map[string]any{"icons": len(icons)}}
	m["counts"] = map[string]any{"icons": len(icons)}
	m["icons"] = im
	m["aliasMap"] = aliases
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// iconServer stands in for icons.zatto.dev.
type iconServer struct {
	t     testing.TB
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string]string // URL path → body
	etag  string
	delay map[string]time.Duration
	code  map[string]int // URL path → forced status
	hits  map[string]int
	conds []string // If-None-Match values the index was asked with
}

func newIconServer(t testing.TB, icons []fixtureIcon) *iconServer {
	m := &iconServer{t: t, files: map[string]string{}, delay: map[string]time.Duration{}, code: map[string]int{}, hits: map[string]int{}}
	m.etag = `"v1"`
	m.files["/index.json"] = indexJSON(t, icons, fixtureAliasMap(), 1)
	for _, i := range icons {
		m.files["/"+i.Format+"/"+i.Slug+"."+i.Format] = "<svg>" + i.Slug + "</svg>"
		for tone, ext := range i.Variants {
			m.files["/"+ext+"/"+i.Slug+"-"+tone+"."+ext] = "<svg>" + i.Slug + "-" + tone + "</svg>"
		}
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *iconServer) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.hits[r.URL.Path]++
	body, ok := m.files[r.URL.Path]
	d, code, etag := m.delay[r.URL.Path], m.code[r.URL.Path], m.etag
	if r.URL.Path == "/index.json" {
		m.conds = append(m.conds, r.Header.Get("If-None-Match"))
	}
	m.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	if code != 0 {
		http.Error(w, "forced", code)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/index.json" {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
	}
	_, _ = w.Write([]byte(body))
}

func (m *iconServer) lastCond() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conds[len(m.conds)-1]
}

func (m *iconServer) set(path, body string) {
	m.mu.Lock()
	m.files[path] = body
	m.mu.Unlock()
}

func (m *iconServer) hitsOf(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[path]
}

func (m *iconServer) totalHits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, h := range m.hits {
		n += h
	}
	return n
}

// loadedCatalog is a catalog with the fixture index imported.
func loadedCatalog(t testing.TB) (*Catalog, *iconServer, string) {
	t.Helper()
	m := newIconServer(t, fixtureIcons())
	dir := t.TempDir()
	k := NewCatalogAt(m.srv.URL, "", dir)
	k.Refresh(context.Background())
	if k.index() == nil {
		t.Fatal("the fixture index did not load")
	}
	return k, m, dir
}

// waitFor polls cond for up to 5 s — background downloads finish on their own time.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func dirNames(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// ── import ───────────────────────────────────────────────────────────────────

func TestImportBuildsTheIndex(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	if got, want := k.Count(), len(fixtureIcons()); got != want {
		t.Errorf("Count() = %d, want %d", got, want)
	}
	if !fileExists(filepath.Join(dir, indexFileName)) {
		t.Error("the index was not written to disk")
	}
	x := k.index()
	// aliases: real, shadowing and dangling
	for _, c := range []struct {
		name string
		want string
	}{
		{"bitwarden", "vaultwarden"}, {"npm", "nginx-proxy-manager"}, {"twenty-i", "20i"},
		{"plex", "plex"},                     // the alias "plex" → jellyfin must not shadow the real icon
		{"ghost", ""},                        // points at nothing
		{"Home_Assistant", "home-assistant"}, // the catalog's normalization
		{"Pi.hole", "pi-hole"},
	} {
		f, ok := x.resolve(c.name)
		if c.want == "" {
			if ok {
				t.Errorf("resolve(%q) = %v, want no match", c.name, f)
			}
			continue
		}
		if !ok || f.File != c.want {
			t.Errorf("resolve(%q) = %v, %v; want %s", c.name, f, ok, c.want)
		}
	}
}

func TestImportRejectsBadDocuments(t *testing.T) {
	good := fixtureIcons()
	for name, doc := range map[string]string{
		"schema 2":        indexJSON(t, good, nil, 2),
		"no icons":        indexJSON(t, nil, nil, 1),
		"truncated":       indexJSON(t, good, nil, 1)[:600],
		"not an object":   `[1,2,3]`,
		"empty":           ``,
		"icons not a map": `{"schema":1,"icons":[1]}`,
		"no schema":       `{"icons":{"a":{"name":"a","format":"svg"}}}`,
	} {
		tmp := filepath.Join(t.TempDir(), "x.db")
		if _, err := importIndex(context.Background(), strings.NewReader(doc), tmp); err == nil {
			t.Errorf("%s: import succeeded, want an error", name)
		}
	}
}

func TestImportSkipsHostileSlugs(t *testing.T) {
	icons := []fixtureIcon{{"ok", "Ok", nil, "svg", nil}, {"fine.name_1-2", "Fine", nil, "png", nil}}
	for _, s := range []string{"../etc", "a/b", `a\b`, "UPPER", "x?y", "x#y", "a..b", "-lead", "sp ace", strings.Repeat("a", 129), "ok-but-bad-ext"} {
		f := "svg"
		if s == "ok-but-bad-ext" {
			f = "exe"
		}
		icons = append(icons, fixtureIcon{s, s, nil, f, nil})
	}
	tmp := filepath.Join(t.TempDir(), "x.db")
	n, err := importIndex(context.Background(), strings.NewReader(indexJSON(t, icons, map[string]string{"../x": "ok", "fine": "ok"}, 1)), tmp)
	if err != nil {
		t.Fatal(err)
	}
	// "a..b" passes the shape check but is refused as a path; the rest never gets in either
	if n != 2 {
		t.Errorf("imported %d icons, want 2 (ok, fine.name_1-2)", n)
	}
	x, err := openIconIndex(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	got, _ := x.search("", 100)
	if !slices.Equal(got, []string{"fine.name_1-2", "ok"}) {
		t.Errorf("slugs = %v", got)
	}
	if _, ok := x.resolve("fine"); !ok {
		t.Error("a valid alias was dropped")
	}
}

// The catalog is not in the heap: importing a 20 000-icon document leaves almost
// nothing allocated afterwards.
func TestIndexIsNotHeldInMemory(t *testing.T) {
	icons := make([]fixtureIcon, 0, 20000)
	for i := range 20000 {
		icons = append(icons, fixtureIcon{fmt.Sprintf("icon-%05d", i), fmt.Sprintf("Icon number %d", i),
			[]string{fmt.Sprintf("alias %d", i)}, "svg", map[string]string{"dark": "svg", "light": "svg"}})
	}
	doc := indexJSON(t, icons, nil, 1)
	icons = nil
	t.Logf("document: %d KiB", len(doc)/1024)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	tmp := filepath.Join(t.TempDir(), "big.db")
	if _, err := importIndex(context.Background(), strings.NewReader(doc), tmp); err != nil {
		t.Fatal(err)
	}
	x, err := openIconIndex(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	if got, _ := x.search("icon number 1999", 10); len(got) == 0 {
		t.Fatal("the big index does not answer")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("heap after import and a query: %+d KiB (document %d KiB)", grew/1024, len(doc)/1024)
	if grew > 2<<20 {
		t.Errorf("heap grew by %d KiB, want under 2048 KiB — is the index being kept in maps again?", grew/1024)
	}
}

// ── search ───────────────────────────────────────────────────────────────────

// refSearch is the search of 0.15, kept here as the specification: names starting with
// the phrase first, then those containing it, a display name or an alias containing it.
func refSearch(icons []fixtureIcon, q string, limit int) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	sorted := slices.Clone(icons)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	if q == "" {
		var out []string
		for _, i := range sorted {
			out = append(out, i.Slug)
		}
		return out[:min(limit, len(out))]
	}
	var prefix, middle []string
	for _, i := range sorted {
		l := strings.ToLower(i.Slug)
		switch {
		case strings.HasPrefix(l, q):
			prefix = append(prefix, i.Slug)
		case strings.Contains(l, q):
			middle = append(middle, i.Slug)
		default:
			found := strings.Contains(strings.ToLower(i.Name), q)
			for _, a := range i.Aliases {
				found = found || strings.Contains(strings.ToLower(a), q)
			}
			if found {
				middle = append(middle, i.Slug)
			}
		}
	}
	out := append(prefix, middle...)
	return out[:min(limit, len(out))]
}

func TestSearchMatchesTheOldBehavior(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	queries := []string{"", "j", "jelly", "ella", "pass", "twenty", "bitwarden", "pi", "pihole", "llama", "llama.cpp",
		"dark", "truenas", "8311", "community", "ngx", "-", "x", "zzz", "assistant", "home assistant", "  HOME  ", "Plex", "ar"}
	for _, q := range queries {
		for _, limit := range []int{5, 60} {
			want := refSearch(fixtureIcons(), q, limit)
			got := k.Search(q, limit)
			if len(want) == 0 && len(got) == 0 {
				continue
			}
			if !slices.Equal(got, want) {
				t.Errorf("Search(%q, %d) = %v, want %v", q, limit, got, want)
			}
		}
	}
}

func TestSearchPhraseIsData(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	n := k.Count()
	junk := []string{
		`"`, `""`, `" OR 1=1 --`, `x" OR "a`, `NEAR(a b)`, `NEAR("a" "b", 2)`, `*`, `a*`, `-a`, `a AND b`, `a OR b`,
		`%`, `_`, `\`, `'; DROP TABLE icons; --`, "a\x00b", "a\nb", "\u0000", `'`, `)(`, `{`, `^a`, `col:val`, `icons:a`,
		strings.Repeat("a", 10<<10), strings.Repeat(`" OR `, 2000), strings.Repeat("%", 10<<10),
	}
	for _, q := range junk {
		got := k.Search(q, 60) // must not panic or error
		for _, g := range got {
			if !strings.Contains(g, strings.ToLower(cleanQuery(q))) && len(cleanQuery(q)) > 0 {
				// a hit must really contain the phrase (in the slug here; the fixture has no such alias)
				t.Errorf("Search(%.20q) returned %q, which does not contain the phrase", q, g)
			}
		}
	}
	if k.Count() != n {
		t.Errorf("the index changed: %d → %d icons", n, k.Count())
	}
	// % and _ are not wildcards
	if got := k.Search("%", 60); len(got) != 0 {
		t.Errorf("Search(%%) = %v, want nothing", got)
	}
	if got := k.Search("j_llyfin", 60); len(got) != 0 {
		t.Errorf("Search(j_llyfin) = %v, want nothing", got)
	}
	if got := k.Search(strings.Repeat("a", 10<<10), 60); len(got) != 0 {
		t.Errorf("a 10 KB phrase matched %v", got)
	}
	if q := cleanQuery(strings.Repeat("ż", 200)); len([]rune(q)) != maxQueryRunes {
		t.Errorf("cleanQuery keeps %d runes, want %d", len([]rune(q)), maxQueryRunes)
	}
	// a phrase must not match across two aliases that the column joins with a newline
	if got := k.Search("twenty\ntwenty-i", 60); len(got) != 0 {
		t.Errorf("a newline matched across aliases: %v", got)
	}
}

func TestVariants(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	for _, c := range []struct{ name, dark, light string }{
		{"dagster", "dagster-dark", "dagster-light"},
		{"Dagster.svg", "dagster-dark", "dagster-light"},
		{"selfhst:llama-cpp", "llama-cpp-dark", "llama-cpp-light"},
		{"hass", "home-assistant-dark", "home-assistant-light"}, // via an alias
		{"jellyfin", "", ""},
		{"1password", "", ""}, // half a pair is no pair
		{"dagster-dark", "", ""},
		{"unknown", "", ""},
		{"../x", "", ""},
	} {
		if d, l := k.Variants(c.name); d != c.dark || l != c.light {
			t.Errorf("Variants(%q) = (%q, %q), want (%q, %q)", c.name, d, l, c.dark, c.light)
		}
	}
	if f, ok := k.index().resolve("dagster-dark"); !ok || f.Ext != "svg" {
		t.Errorf("variant file not resolvable: %v %v", f, ok)
	}
}

func TestResolveIconUsesVariantsWhenPresent(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	s := &Server{icons: k}

	ref := s.resolveIcon("dagster", "Dagster")
	if ref.Src != "" {
		t.Errorf("for an icon with variants Src should be empty (we render both variants), got %q", ref.Src)
	}
	if ref.VariantDark != "/icon/dagster-dark" || ref.VariantLight != "/icon/dagster-light" {
		t.Errorf("variants = (%q, %q), want (/icon/dagster-dark, /icon/dagster-light)", ref.VariantDark, ref.VariantLight)
	}
	plain := s.resolveIcon("jellyfin", "Jellyfin")
	if plain.Src != "/icon/jellyfin" || plain.VariantDark != "" || plain.VariantLight != "" {
		t.Errorf("without variants I expect a plain Src, got %+v", plain)
	}
}

func TestCanonicalName(t *testing.T) {
	for in, want := range map[string]string{
		"jellyfin": "jellyfin", "Jellyfin.SVG": "jellyfin", "x.png": "x", "selfhst:llama-cpp": "llama-cpp",
		" jellyfin ": "jellyfin", "a.b_c-d": "a.b_c-d",
		"": "", "selfhst:": "", "../x": "", "a/b": "", `a\b`: "", "a..b": "", "-x": "", ".x": "", "a b": "",
		"a:b": "", strings.Repeat("a", 129): "", "ż": "",
	} {
		got, ok := canonicalName(in)
		if got != want || ok != (want != "") {
			t.Errorf("canonicalName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestNormalizeIconsURL(t *testing.T) {
	for in, want := range map[string]string{
		"": DefaultIconsURL, "https://icons.zatto.dev": "https://icons.zatto.dev", "https://icons.zatto.dev/": "https://icons.zatto.dev",
		"http://10.0.0.5:8080/icons/": "http://10.0.0.5:8080/icons", " https://mirror.example.org ": "https://mirror.example.org",
	} {
		if got, err := NormalizeIconsURL(in); err != nil || got != want {
			t.Errorf("NormalizeIconsURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"ftp://x", "icons.zatto.dev", "https://", "https://u:p@x", "https://x?a=1", "https://x#f", "javascript:alert(1)", "//x"} {
		if got, err := NormalizeIconsURL(in); err == nil {
			t.Errorf("NormalizeIconsURL(%q) = %q, want an error", in, got)
		}
	}
}

// ── reading, storing, cleaning up ────────────────────────────────────────────

func TestAliasesOfOldNamesServeTheIcon(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	for _, name := range []string{"vaultwarden", "bitwarden", "Bitwarden", "selfhst:vaultwarden"} {
		data, kind, err := k.Read(context.Background(), name)
		if err != nil || string(data) != "<svg>vaultwarden</svg>" || kind != "image/svg+xml" {
			t.Errorf("Read(%q) = %q %q %v", name, data, kind, err)
		}
	}
	data, kind, err := k.Read(context.Background(), "portainer") // png-only icon
	if err != nil || kind != "image/png" || string(data) != "<svg>portainer</svg>" {
		t.Errorf("Read(portainer) = %q %q %v", data, kind, err)
	}
}

func TestHas(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	if err := os.WriteFile(filepath.Join(dir, "cached-only.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"jellyfin": true, "bitwarden": true, "selfhst:llama-cpp": true, "dagster-dark": true,
		"cached-only": true, "nothing": false, "": false, "selfhst:": false, "../x": false, "dagster-sepia": false,
	} {
		if got := k.Has(name); got != want {
			t.Errorf("Has(%q) = %v, want %v", name, got, want)
		}
	}
}

// Only the icons of services are kept on disk. A picker thumbnail is served and
// remembered in memory, never written to the data directory.
func TestThumbnailsStayOutOfTheDataDirectory(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	before := dirNames(t, dir)
	for _, n := range []string{"plex", "radarr", "sonarr", "immich", "dagster-dark"} {
		if _, _, err := k.Read(context.Background(), n); err != nil {
			t.Fatalf("Read(%s): %v", n, err)
		}
	}
	if after := dirNames(t, dir); !slices.Equal(before, after) {
		t.Errorf("thumbnails touched the data directory: %v → %v", before, after)
	}
	// the second read is served from memory
	hits := m.hitsOf("/svg/plex.svg")
	if data, _, err := k.Read(context.Background(), "plex"); err != nil || string(data) != "<svg>plex</svg>" {
		t.Fatalf("second Read = %q %v", data, err)
	}
	if m.hitsOf("/svg/plex.svg") != hits {
		t.Error("the thumbnail was fetched again")
	}
}

func TestThumbnailCacheIsBounded(t *testing.T) {
	var c thumbCache
	for i := range 1000 {
		c.put(fmt.Sprint("k", i), bytes.Repeat([]byte("x"), 10<<10), "image/svg+xml")
	}
	if c.order.Len() > thumbCacheMax || c.bytes > thumbCacheSize {
		t.Errorf("%d entries, %d bytes: over the bounds (%d, %d)", c.order.Len(), c.bytes, thumbCacheMax, thumbCacheSize)
	}
	c.put("huge", bytes.Repeat([]byte("x"), thumbCacheSize), "image/svg+xml")
	if _, _, ok := c.get("huge"); ok {
		t.Error("an oversized entry was cached")
	}
	if _, _, ok := c.get("k999"); !ok {
		t.Error("the newest entry is gone")
	}
}

func TestRetainFetchesServiceIconsAndTheirVariants(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	k.Retain(context.Background(), []string{"jellyfin", "dagster", "bitwarden", "nothing-here"})
	for _, f := range []string{"jellyfin.svg", "dagster.svg", "dagster-dark.svg", "dagster-light.svg", "vaultwarden.svg"} {
		waitFor(t, f, func() bool { return fileExists(filepath.Join(dir, f)) })
	}
	if fileExists(filepath.Join(dir, "bitwarden.svg")) {
		t.Error("an alias got its own file; the canonical name is the file")
	}
}

func TestRetainRemovesWhatServicesDoNotUse(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	old := time.Now().Add(-2 * time.Hour)
	for f, mod := range map[string]time.Time{
		"leftover.svg": {}, "leftover.png": {}, "index.json": {}, "spis.json": {}, "selfhst/llama-cpp.svg": {},
		".stale.svg.123.tmp": old, ".fresh.svg.456.tmp": {}, "notes.txt": {},
	} {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !mod.IsZero() {
			_ = os.Chtimes(p, mod, mod)
		}
	}
	k.Retain(context.Background(), []string{"jellyfin"})
	waitFor(t, "jellyfin.svg", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })
	want := []string{".fresh.svg.456.tmp", "icons-index.db", "icons-index.meta.json", "jellyfin.svg", "notes.txt"}
	if got := dirNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("cache directory = %v, want %v", got, want)
	}

	// the service is removed from the config: its icon goes, the index stays
	k.Retain(context.Background(), nil)
	if got := dirNames(t, dir); slices.Contains(got, "jellyfin.svg") || !slices.Contains(got, indexFileName) {
		t.Errorf("after removing the service: %v", got)
	}
	if k.Count() == 0 || len(k.Search("jelly", 5)) == 0 {
		t.Error("the picker lost the index")
	}
}

// Before the index is loaded a name cannot be resolved, so an icon file that belongs
// to an alias must not be taken for garbage.
func TestRetainWithoutIndexDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vaultwarden.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := NewCatalogAt("http://127.0.0.1:1", "", dir)
	k.Retain(context.Background(), []string{"bitwarden"})
	if !fileExists(filepath.Join(dir, "vaultwarden.svg")) {
		t.Error("an icon was deleted although no index could say it is still in use")
	}
}

func TestRetainPromotesAThumbnailWithoutDownloading(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	if _, _, err := k.Read(context.Background(), "plex"); err != nil {
		t.Fatal(err)
	}
	hits := m.hitsOf("/svg/plex.svg")
	k.Retain(context.Background(), []string{"plex"})
	if !fileExists(filepath.Join(dir, "plex.svg")) {
		t.Fatal("the chosen thumbnail was not saved for the service")
	}
	if m.hitsOf("/svg/plex.svg") != hits {
		t.Error("the icon was downloaded again")
	}
}

// ── offline ──────────────────────────────────────────────────────────────────

func TestOfflinePickerAndServiceIcons(t *testing.T) {
	m := newIconServer(t, fixtureIcons())
	dir := t.TempDir()
	k := NewCatalogAt(m.srv.URL, "", dir)
	k.Refresh(context.Background())
	k.Retain(context.Background(), []string{"jellyfin", "dagster"})
	waitFor(t, "dagster-light.svg", func() bool { return fileExists(filepath.Join(dir, "dagster-light.svg")) })

	// the icon server goes away; the panel restarts
	m.srv.Close()
	ageIndex(t, k) // due for a refresh, so the panel really tries the dead server
	k2 := NewCatalogAt(m.srv.URL, "", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k2.Refresh(ctx) // fails quietly
	k2.Retain(ctx, []string{"jellyfin", "dagster"})

	if got := k2.Search("jelly", 10); !slices.Equal(got, []string{"jellyfin"}) {
		t.Errorf("offline Search(jelly) = %v", got)
	}
	if got := k2.Search("bitwarden", 10); !slices.Equal(got, []string{"vaultwarden"}) {
		t.Errorf("offline Search(bitwarden) = %v", got)
	}
	if k2.Count() != len(fixtureIcons()) {
		t.Errorf("offline Count() = %d", k2.Count())
	}
	if !k2.Authoritative() {
		t.Error("an index from disk should count as authoritative")
	}
	for _, n := range []string{"jellyfin", "dagster-dark", "dagster-light"} {
		start := time.Now()
		data, _, err := k2.Read(ctx, n)
		if err != nil || string(data) != "<svg>"+n+"</svg>" {
			t.Errorf("offline Read(%s) = %q %v", n, data, err)
		}
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("offline Read(%s) took %v", n, d)
		}
	}
	// an icon nobody saved is simply unavailable, at once
	start := time.Now()
	if _, _, err := k2.Read(ctx, "plex"); err == nil {
		t.Error("an icon that was never fetched cannot be served offline")
	}
	if d := time.Since(start); d > iconWaitBudget+300*time.Millisecond {
		t.Errorf("offline miss took %v", d)
	}
	k3 := NewCatalogAt(m.srv.URL, "", dir)
	if k3.Count() != len(fixtureIcons()) {
		t.Error("a cold start did not open the index from disk before any network")
	}
}

// ── refresh ──────────────────────────────────────────────────────────────────

func ageIndex(t *testing.T, k *Catalog) {
	t.Helper()
	meta := k.readMeta()
	meta.CheckedAt = time.Now().Add(-48 * time.Hour)
	k.writeMeta(meta)
}

func TestRefreshIsConditionalAndKeepsTheOldIndexOnFailure(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	ctx := context.Background()
	idx := m.hitsOf("/index.json")

	// fresh: no request at all
	k.Refresh(ctx)
	if m.hitsOf("/index.json") != idx {
		t.Error("a fresh index was asked for again")
	}

	// stale and unchanged: one conditional request, a 304, nothing re-imported
	ageIndex(t, k)
	before := k.index()
	k.Refresh(ctx)
	if m.hitsOf("/index.json") != idx+1 {
		t.Fatalf("index requests = %d, want %d", m.hitsOf("/index.json"), idx+1)
	}
	if last := m.lastCond(); last != `"v1"` {
		t.Errorf("If-None-Match = %q, want the stored ETag", last)
	}
	if k.index() != before {
		t.Error("a 304 re-imported the index")
	}
	if time.Since(k.readMeta().CheckedAt) > time.Minute {
		t.Error("a 304 did not renew the check time")
	}

	// changed upstream: new index, new ETag
	m.mu.Lock()
	m.etag = `"v2"`
	m.mu.Unlock()
	m.set("/index.json", indexJSON(t, append(fixtureIcons(), fixtureIcon{"newcomer", "Newcomer", nil, "svg", nil}), fixtureAliasMap(), 1))
	ageIndex(t, k)
	k.Refresh(ctx)
	if k.Count() != len(fixtureIcons())+1 || k.readMeta().ETag != `"v2"` {
		t.Errorf("after a change: Count = %d, etag %q", k.Count(), k.readMeta().ETag)
	}

	// the server breaks: the old index stays, no temp files are left behind
	for name, set := range map[string]func(){
		"5xx": func() { m.mu.Lock(); m.code["/index.json"] = 503; m.mu.Unlock() },
		"truncated": func() {
			m.mu.Lock()
			delete(m.code, "/index.json")
			m.etag = `"v3"`
			m.mu.Unlock()
			m.set("/index.json", `{"schema":1,"icons":{"a":`)
		},
		"schema": func() { m.set("/index.json", indexJSON(t, fixtureIcons(), nil, 9)) },
	} {
		set()
		k.net.mu.Lock()
		clear(k.net.downTill)
		k.net.mu.Unlock()
		ageIndex(t, k)
		n := k.Count()
		k.Refresh(ctx)
		if k.Count() != n || len(k.Search("jelly", 5)) == 0 {
			t.Errorf("%s: the index was lost", name)
		}
		for _, f := range dirNames(t, dir) {
			if strings.HasSuffix(f, ".tmp") {
				t.Errorf("%s: temporary file %s left behind", name, f)
			}
		}
	}
}

func TestDamagedIndexIsRebuilt(t *testing.T) {
	_, m, good := loadedCatalog(t)
	for name, damage := range map[string]func(path string){
		"garbage":   func(p string) { _ = os.WriteFile(p, bytes.Repeat([]byte("not sqlite "), 1000), 0o644) },
		"truncated": func(p string) { _ = os.Truncate(p, 5000) },
		"empty":     func(p string) { _ = os.WriteFile(p, nil, 0o644) },
		"no tables": func(p string) { _ = os.WriteFile(p, []byte("SQLite format 3\x00"), 0o644) },
	} {
		dir := t.TempDir()
		for _, f := range []string{indexFileName, indexMetaName} {
			b, err := os.ReadFile(filepath.Join(good, f))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		damage(filepath.Join(dir, indexFileName))

		k := NewCatalogAt(m.srv.URL, "", dir)
		if k.index() != nil {
			t.Fatalf("%s: a damaged index was opened", name)
		}
		if fileExists(filepath.Join(dir, indexFileName)) {
			t.Errorf("%s: the damaged file was kept", name)
		}
		k.Refresh(context.Background())
		if k.index() == nil || k.Count() != len(fixtureIcons()) {
			t.Errorf("%s: the index was not rebuilt (count %d)", name, k.Count())
		}
	}
}

func TestRefreshTurnsToTheNewSourceWhenItChanges(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	other := newIconServer(t, fixtureIcons()[:3])
	other.mu.Lock()
	other.etag = `"v1"` // the same validator as the first server: must not be reused
	other.mu.Unlock()
	k2 := NewCatalogAt(other.srv.URL, "", dir)
	_ = k
	_ = m
	k2.Refresh(context.Background())
	if k2.Count() != 3 {
		t.Errorf("after pointing at another source Count() = %d, want 3", k2.Count())
	}
}

// ── network guards (the behavior of the old CDN client, kept) ───────────────

func TestFetchRejectsEmptyResponse(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	m.set("/svg/plex.svg", "") // a CDN that says 200 with no body
	k.Retain(context.Background(), []string{"plex"})
	time.Sleep(200 * time.Millisecond)
	if _, _, err := k.Read(context.Background(), "plex"); err == nil {
		t.Fatal("Read should fail for an empty response")
	}
	// ! the heart of the old defect: an empty body written to the cache poisoned the icon forever
	if st, err := os.Stat(filepath.Join(dir, "plex.svg")); err == nil {
		t.Fatalf("an empty response was written to the cache (%d bytes)", st.Size())
	}
}

func TestReadHealsAPoisonedCacheFile(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	k.Retain(context.Background(), []string{"plex"})
	waitFor(t, "plex.svg", func() bool { return fileExists(filepath.Join(dir, "plex.svg")) })
	if err := os.WriteFile(filepath.Join(dir, "plex.svg"), nil, 0o644); err != nil { // the state left by 0.13
		t.Fatal(err)
	}
	data, _, err := k.Read(context.Background(), "plex")
	if err != nil || string(data) != "<svg>plex</svg>" {
		t.Fatalf("Read = %q, %v; want the real file", data, err)
	}
	waitFor(t, "the healed file", func() bool { b, _ := os.ReadFile(filepath.Join(dir, "plex.svg")); return len(b) > 0 })
}

func TestFetchRefusesOversizedIconInsteadOfTruncating(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	m.set("/svg/plex.svg", strings.Repeat("x", maxIconBytes+1))
	k.Retain(context.Background(), []string{"plex"})
	if data, _, err := k.Read(context.Background(), "plex"); err == nil {
		t.Fatalf("Read returned %d bytes of a file over the limit, want an error", len(data))
	}
	time.Sleep(100 * time.Millisecond)
	if fileExists(filepath.Join(dir, "plex.svg")) {
		t.Error("a truncated icon was written to the cache")
	}
}

// SHYG-59: parallel downloads of one icon used one fixed ".tmp" name and could
// rename each other's half-written file.
func TestParallelFetchesOfOneIconDoNotCollide(t *testing.T) {
	body := strings.Repeat("<path d='M0 0h10v10z'/>", 3000)
	m := newIconServer(t, fixtureIcons())
	m.set("/svg/plex.svg", "<svg>"+body+"</svg>")
	dir := t.TempDir()
	k := NewCatalogAt(m.srv.URL, "", dir)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, _, err := k.fetchFrom(context.Background(), k.baseURL, dir, "plex", "svg")
			if err == nil && string(data) != "<svg>"+body+"</svg>" {
				err = fmt.Errorf("short body: %d bytes", len(data))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "plex.svg"))
	if err != nil || string(got) != "<svg>"+body+"</svg>" {
		t.Errorf("cached file is damaged: %d bytes, %v", len(got), err)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"plex.svg"}) {
		t.Errorf("directory = %v, want only plex.svg (no temporary files)", names)
	}
}

func TestWriteFileAtomicWritersDoNotShareATempFile(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writeFileAtomic(dir, "x.svg", bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(dir, "x.svg"))
	if len(b) != 64<<10 || len(bytes.Trim(b, string(b[:1]))) != 0 {
		t.Errorf("the file mixes writers (%d bytes)", len(b))
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"x.svg"}) {
		t.Errorf("directory = %v", names)
	}
}

// A slow icon server does not hold the request: past iconWaitBudget it gets an error
// (the card shows its letter), the download finishes in the background, and
// concurrent requests share one download.
func TestSlowSourceDoesNotBlockAndFinishesInBackground(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	k.Retain(context.Background(), []string{"jellyfin"}) // so the file is kept on disk
	waitFor(t, "jellyfin", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })
	_ = os.Remove(filepath.Join(dir, "jellyfin.svg"))
	m.mu.Lock()
	m.delay["/svg/jellyfin.svg"] = iconWaitBudget + 500*time.Millisecond
	m.mu.Unlock()
	hits := m.hitsOf("/svg/jellyfin.svg")

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	start := time.Now()
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := k.Read(context.Background(), "jellyfin")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	if d := time.Since(start); d > iconWaitBudget+300*time.Millisecond {
		t.Errorf("Read waited %v, want at most about %v", d, iconWaitBudget)
	}
	for err := range errs {
		if err != errIconPending {
			t.Errorf("err = %v, want errIconPending", err)
		}
	}
	waitFor(t, "the background download", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })
	if n := m.hitsOf("/svg/jellyfin.svg") - hits; n != 1 {
		t.Errorf("%d downloads for 5 concurrent requests, want 1", n)
	}
}

func TestNetworkFailureStopsAndCoolsDown(t *testing.T) {
	ft := &failingTransport{}
	k := NewCatalogAt("", "", t.TempDir())
	k.client = &http.Client{Transport: ft}

	if _, _, err := k.Read(context.Background(), "llama-cpp"); err == nil {
		t.Fatal("Read should fail with the network down")
	}
	if n := ft.calls.Load(); n != 1 {
		t.Errorf("%d requests, want 1 — no png retry after a network error", n)
	}
	start := time.Now()
	if _, _, err := k.Read(context.Background(), "jellyfin"); err == nil {
		t.Fatal("Read should fail with the network down")
	}
	if n := ft.calls.Load(); n != 1 {
		t.Errorf("%d requests, want still 1 — the host is on cooldown", n)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Read on a cooled-down host took %v, want instant", d)
	}
}

type failingTransport struct{ calls atomic.Int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, fmt.Errorf("connection refused")
}

func TestMissingNameIsRemembered(t *testing.T) {
	m := newIconServer(t, nil)
	k := NewCatalogAt(m.srv.URL, "", t.TempDir()) // no index: the panel has to ask
	if _, _, err := k.Read(context.Background(), "nope"); err != errNotFound {
		t.Fatalf("err = %v, want errNotFound", err)
	}
	asked := m.totalHits()
	if _, _, err := k.Read(context.Background(), "nope"); err != errNotFound {
		t.Fatalf("err = %v, want errNotFound", err)
	}
	if m.totalHits() != asked {
		t.Errorf("a remembered miss went to the network again (%d → %d requests)", asked, m.totalHits())
	}
	// a new index may route the name elsewhere — misses are forgotten
	k.swapIndex(nil)
	if k.net.missing("nope") {
		t.Error("misses should be forgotten when the index changes")
	}
}

func TestOnlyNotFoundIsRemembered(t *testing.T) {
	m := newIconServer(t, nil)
	m.mu.Lock()
	m.code["/svg/llama-cpp.svg"] = http.StatusForbidden
	m.mu.Unlock()
	k := NewCatalogAt(m.srv.URL, "", t.TempDir())
	if _, _, err := k.Read(context.Background(), "llama-cpp"); err == nil {
		t.Fatal("Read should fail on a 403")
	}
	if k.net.missing("llama-cpp") {
		t.Error("a non-404 failure must not be remembered as a missing icon")
	}
	if m.hitsOf("/png/llama-cpp.png") != 0 {
		t.Error("png was tried after a non-404 failure")
	}
}

// With the index loaded, a name the index does not have is answered from memory:
// random names in /icon/ must not turn into downloads (or goroutines).
func TestUnknownNameWithAnIndexStaysOffTheNetwork(t *testing.T) {
	k, m, _ := loadedCatalog(t)
	before := m.totalHits()
	goroutines := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 2000 {
		if _, _, err := k.Read(ctx, fmt.Sprintf("random-%d", i)); err == nil {
			t.Fatal("a random name should not be found")
		}
	}
	if n := m.totalHits() - before; n != 0 {
		t.Errorf("%d requests for names the index does not have, want 0", n)
	}
	if d := runtime.NumGoroutine() - goroutines; d > 10 {
		t.Errorf("%d goroutines left behind by 2000 unknown names", d)
	}
}

// Without an index the panel cannot tell, so it asks — but never more than
// maxIconFlights downloads at once; the rest get "pending" right away.
func TestDownloadsAreCapped(t *testing.T) {
	var now, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := now.Add(1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
		now.Add(-1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	k := NewCatalogAt(srv.URL, "", t.TempDir())
	goroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 2000 {
		_, _, _ = k.Read(ctx, fmt.Sprintf("random-%d", i))
	}
	if d := runtime.NumGoroutine() - goroutines; d > 6*maxIconFlights {
		t.Errorf("%d goroutines after 2000 random names, want at most %d", d, 6*maxIconFlights)
	}
	waitFor(t, "the downloads to drain", func() bool {
		k.net.mu.Lock()
		defer k.net.mu.Unlock()
		return len(k.net.flights) == 0
	})
	if p := peak.Load(); p > maxIconFlights {
		t.Errorf("%d concurrent requests, want at most %d", p, maxIconFlights)
	}
}

func TestIconClientLimitsConnectionsPerHost(t *testing.T) {
	tr, ok := newIconClient().Transport.(*http.Transport)
	if !ok || tr.MaxConnsPerHost == 0 || tr.MaxConnsPerHost > 8 {
		t.Errorf("MaxConnsPerHost = %v, want 1..8", tr.MaxConnsPerHost)
	}
}

// ── HTTP route ───────────────────────────────────────────────────────────────

// Through a real ServeMux, as in production: %3A and %2F are decoded into the
// {name} value, and a slash must still be refused.
func TestIconRoute(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	s := &Server{icons: k}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /icon/{name}", s.handleIcon)

	for _, c := range []struct {
		path string
		code int
	}{
		{"/icon/jellyfin", http.StatusOK},
		{"/icon/jellyfin.svg", http.StatusOK},
		{"/icon/Jellyfin", http.StatusOK},
		{"/icon/bitwarden", http.StatusOK},
		{"/icon/dagster-light", http.StatusOK},
		{"/icon/selfhst%3Allama-cpp-dark", http.StatusOK},
		{"/icon/selfhst:llama-cpp-light", http.StatusOK},
		{"/icon/portainer.png", http.StatusOK},
		{"/icon/..%2Findex", http.StatusNotFound},
		{"/icon/selfhst%3A..%2Findex", http.StatusNotFound},
		{"/icon/selfhst%3A", http.StatusNotFound},
		{"/icon/a%20b", http.StatusNotFound},
		{"/icon/unknown-icon", http.StatusNotFound},
		{"/icon/" + strings.Repeat("a", 5000), http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("GET %s = %d, want %d", c.path[:min(len(c.path), 40)], rec.Code, c.code)
		}
		if c.code == http.StatusOK && rec.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
			t.Errorf("GET %s: CSP = %q", c.path, rec.Header().Get("Content-Security-Policy"))
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: missing X-Content-Type-Options: nosniff", c.path[:min(len(c.path), 40)])
		}
		if c.code == http.StatusNotFound && rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s: a miss must not be cached", c.path[:min(len(c.path), 40)])
		}
	}
	// the type follows the file
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/icon/portainer", nil))
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=604800") {
		t.Errorf("Cache-Control = %q, want a week with a full index", cc)
	}
}

// The icon hot path stays fast: a file on disk is a single directory read plus an
// indexed lookup.
func TestIconCacheHitIsFast(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	k.Retain(context.Background(), []string{"jellyfin"})
	waitFor(t, "jellyfin", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })
	start := time.Now()
	const n = 500
	for range n {
		if _, _, err := k.Read(context.Background(), "jellyfin"); err != nil {
			t.Fatal(err)
		}
	}
	if per := time.Since(start) / n; per > 2*time.Millisecond {
		t.Errorf("cache hit took %v on average", per)
	}
}

// Without an index and without the network, the picker shows what services use.
func TestPickerWithoutAnyIndexListsCachedIcons(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"llama-cpp.svg", "13-feet-ladder.png", ".llama-cpp.svg.1.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	k := NewCatalogAt("http://127.0.0.1:1", "", dir)
	k.Refresh(context.Background())
	if got := k.Search("", 10); !slices.Equal(got, []string{"13-feet-ladder", "llama-cpp"}) {
		t.Errorf("Search = %v", got)
	}
	if got := k.Search("llama", 10); !slices.Equal(got, []string{"llama-cpp"}) {
		t.Errorf("Search(llama) = %v", got)
	}
	if k.Authoritative() {
		t.Error("a scan of cached files is not a full index")
	}
	data, _, err := k.Read(context.Background(), "llama-cpp")
	if err != nil || string(data) != "x" {
		t.Errorf("Read = %q %v", data, err)
	}
}
