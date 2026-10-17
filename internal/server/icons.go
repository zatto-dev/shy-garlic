package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Icons are never fetched by the user's browser.
//
// ! The whole competition inserts <img src="https://cdn.jsdelivr.net/..."> into
// the HTML. That means the browser asks a foreign CDN about the *names of the
// services you have at home* — jsDelivr (and Cloudflare in front of it) sees that
// someone from your IP is pulling vaultwarden.svg and proxmox.svg. On top of that
// a panel without internet loses its icons.
//
// Here it is different: the browser asks only our server (/icon/<name>), and the
// server either has the icon in the on-disk cache, or pulls it once from the CDN
// and saves it. After first use it works offline, and the CDN never sees the
// user's browser — it sees one server with no context.
//
// `zip` mode (an optional icons.zip file) gives the full set right away, without a
// single outbound request — for networks cut off from the internet.
//
// Two catalogs feed it: Dashboard Icons (homarr-labs, below) first, and
// selfh.st/icons for the names Dashboard Icons lacks — see icons_selfhst.go.

const (
	cdnIcons = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons@main"
	// ! tree.json in the repo is sometimes stale — an audit on 2026-08-14 showed 2799 entries
	// in tree.json against 3363 SVG files in the directory. That is why we ask the git
	// API for the real tree first, and tree.json stays as a fallback.
	gitTreeURL    = "https://api.github.com/repos/homarr-labs/dashboard-icons/git/trees/main?recursive=1"
	metadataURL   = "https://raw.githubusercontent.com/homarr-labs/dashboard-icons/main/metadata.json"
	treeURL       = "https://raw.githubusercontent.com/homarr-labs/dashboard-icons/main/tree.json"
	indexValidity = 7 * 24 * time.Hour
	maxIconBytes  = 2 << 20
)

// toneVariant is two separate artworks of the same icon — with dark and with light
// ink — which Dashboard Icons keeps for some entries precisely so that the right one
// can be picked for the background. Dark ink disappears on the dark theme and
// vice versa, so the file name matters:
//   - Dark  — file with dark ink, readable on a LIGHT background (that is
//     metadata.json → colors.dark)
//   - Light — file with light ink, readable on a DARK background (metadata.json →
//     colors.light)
type toneVariant struct {
	Dark  string
	Light string
}

type Catalog struct {
	mu       sync.RWMutex
	names    []string          // sorted, without extensions; both catalogs, each name once
	ext      map[string]string // Dashboard Icons (or icons.zip): name → "svg" | "png"
	cacheDir string
	mode     string // "zip" or "cdn"

	aliases  map[string][]string    // name → other names by which it can also be found
	variants map[string]toneVariant // name → separate files for the dark/light panel theme (when present)
	selfhst  selfhstIndex           // the second catalog, used for names Dashboard Icons lacks
	zipIdx   map[string]*zip.File
	client   *http.Client
}

// rebuildNamesLocked merges both catalogs into the sorted name list. The caller
// holds k.mu for writing.
func (k *Catalog) rebuildNamesLocked() {
	k.names = make([]string, 0, len(k.ext)+len(k.selfhst.names))
	for n := range k.ext {
		k.names = append(k.names, n)
	}
	for n := range k.selfhst.names {
		if _, inHomarr := k.ext[n]; !inHomarr {
			k.names = append(k.names, n)
		}
	}
	sort.Strings(k.names)
}

// badIconName rejects anything that could leave the cache directory.
func badIconName(name string) bool {
	return strings.ContainsAny(name, `/\`) || strings.Contains(name, "..")
}

func NewCatalog(zipPath, cacheDir string) *Catalog {
	k := &Catalog{
		ext:      map[string]string{},
		cacheDir: cacheDir,
		mode:     "cdn",
		client:   &http.Client{Timeout: 20 * time.Second},
	}
	if zipPath != "" {
		if err := k.loadZip(zipPath); err != nil {
			slog.Warn("could not load icons.zip, falling back to CDN", "file", zipPath, "err", err)
		} else {
			k.mode = "zip"
		}
	}
	if k.cacheDir != "" {
		_ = os.MkdirAll(k.cacheDir, 0o755)
	}
	return k
}

func (k *Catalog) Mode() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.mode
}

func (k *Catalog) Count() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.names)
}

// Variants returns the file names with dark and light ink for a given icon, when
// its catalog keeps such a pair. An empty string in both fields = this icon
// has no separate variants, the caller should use the plain base name.
// The pair follows the catalog that serves the name: a name Dashboard Icons has
// never gets selfh.st's variants.
func (k *Catalog) Variants(name string) (dark, light string) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	var w toneVariant
	if ref, ok := strings.CutPrefix(name, selfhstPrefix); ok {
		w = k.selfhst.variants[ref]
	} else if hw, ok := k.variants[name]; ok {
		w = hw
	} else if _, inHomarr := k.ext[name]; !inHomarr {
		w = k.selfhst.variants[name]
	}
	if w.Dark == "" || w.Light == "" {
		return "", ""
	}
	return w.Dark, w.Light
}

// ── local mode (zip) ─────────────────────────────────────────────────────────

func (k *Catalog) loadZip(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.zipIdx = make(map[string]*zip.File, len(r.File))
	k.names = nil
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, ".svg") {
			continue
		}
		k.zipIdx[f.Name] = f
		name := strings.TrimSuffix(f.Name, ".svg")
		k.names = append(k.names, name)
		k.ext[name] = "svg"
	}
	sort.Strings(k.names)
	slog.Info("icon catalog from file", "file", path, "icons", len(k.names))
	return nil
}

// ── CDN mode ─────────────────────────────────────────────────────────────────

type tree struct {
	SVG []string `json:"svg"`
	PNG []string `json:"png"`
}

// Refresh loads the icon index: from disk if fresh, otherwise from the network.
// No internet is not a critical error — the panel keeps working, and the picker
// shows what already lies in the cache.
func (k *Catalog) Refresh(ctx context.Context) {
	if k.Mode() == "zip" {
		return
	}
	k.refreshNameIndex(ctx)

	// The light/dark variants (for the theme switch) live only in
	// metadata.json — they have no cache file of their own and we try to fetch them
	// ALWAYS, regardless of whether the name index above came from disk or from
	// the network (the most common branch — the index TTL is a week — otherwise the variants
	// would never get set after an ordinary restart of the process with a warm
	// cache). raw.githubusercontent.com has no limit like api.github.com
	// (git-tree), so it is a cheap, extra request on every start.
	if _, err := k.fromMetadata(ctx); err != nil {
		slog.Debug("light/dark variants unavailable", "err", err)
	}

	k.refreshSelfhst(ctx)
}

// legacyIndexFile is the pre-0.14 name of the name-index cache.
const legacyIndexFile = "spis.json"

func (k *Catalog) refreshNameIndex(ctx context.Context) {
	file := filepath.Join(k.cacheDir, "index.json")
	if fi, err := os.Stat(file); err == nil && time.Since(fi.ModTime()) < indexValidity {
		if data, err := os.ReadFile(file); err == nil && k.setFromIndex(data) == nil {
			slog.Info("icon catalog from cache", "icons", k.Count())
			return
		}
	}

	if names, source, err := k.fetchIndex(ctx); err == nil && len(names) > 0 {
		k.setNames(names)
		if b, err := json.Marshal(names); err == nil {
			_ = os.WriteFile(file, b, 0o644)
			// the cache used to be called spis.json; it is only a cache, so the old file
			// is ignored on read and removed once the new one is written
			_ = os.Remove(filepath.Join(k.cacheDir, legacyIndexFile))
		}
		slog.Info("icon catalog fetched", "icons", k.Count(), "source", source)
		return
	}

	slog.Warn("icon index unavailable, keeping what is on disk")
	k.fallbackFromDisk(file)
}

// fetchIndex goes from the most precise source to the weakest.
// The git tree knows the truth about the directory, but has a limit of 60 requests
// per hour without a token. metadata.json is fresh and carries aliases. tree.json
// is sometimes stale, but always responds.
func (k *Catalog) fetchIndex(ctx context.Context) (map[string]string, string, error) {
	if names, err := k.fromGitTree(ctx); err == nil && len(names) > 0 {
		return names, "git-tree", nil
	}
	if names, err := k.fromMetadata(ctx); err == nil && len(names) > 0 {
		return names, "metadata.json", nil
	}
	names, err := k.fromTreeJSON(ctx)
	return names, "tree.json", err
}

// fromMetadata reads metadata.json — it also gives the aliases the picker searches by.
func (k *Catalog) fromMetadata(ctx context.Context) (map[string]string, error) {
	data, err := k.fetchJSON(ctx, metadataURL, 16<<20)
	if err != nil {
		return nil, err
	}
	var m map[string]struct {
		Base    string            `json:"base"`
		Aliases []string          `json:"aliases"`
		Colors  map[string]string `json:"colors"` // "dark"/"light" → file name with that ink
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("empty metadata")
	}
	out := make(map[string]string, len(m))
	aliases := make(map[string][]string, len(m)/4)
	variantMap := make(map[string]toneVariant, len(m)/6)
	for name, w := range m {
		ext := w.Base
		if ext != "png" {
			ext = "svg"
		}
		out[name] = ext
		if len(w.Aliases) > 0 {
			aliases[name] = w.Aliases
		}
		if dark, light := w.Colors["dark"], w.Colors["light"]; dark != "" && light != "" {
			variantMap[name] = toneVariant{Dark: dark, Light: light}
		}
	}
	k.mu.Lock()
	k.aliases = aliases
	k.variants = variantMap
	k.mu.Unlock()
	return out, nil
}

func (k *Catalog) fetchJSON(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (k *Catalog) fromGitTree(ctx context.Context) (map[string]string, error) {
	data, err := k.fetchJSON(ctx, gitTreeURL, 16<<20)
	if err != nil {
		return nil, err
	}
	var d struct {
		Tree []struct {
			Path string `json:"path"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Truncated || len(d.Tree) == 0 {
		return nil, fmt.Errorf("git tree incomplete")
	}
	out := map[string]string{}
	for _, t := range d.Tree {
		switch {
		case strings.HasPrefix(t.Path, "svg/") && strings.HasSuffix(t.Path, ".svg"):
			out[strings.TrimSuffix(strings.TrimPrefix(t.Path, "svg/"), ".svg")] = "svg"
		case strings.HasPrefix(t.Path, "png/") && strings.HasSuffix(t.Path, ".png"):
			n := strings.TrimSuffix(strings.TrimPrefix(t.Path, "png/"), ".png")
			if _, is := out[n]; !is {
				out[n] = "png"
			}
		}
	}
	return out, nil
}

func (k *Catalog) fromTreeJSON(ctx context.Context) (map[string]string, error) {
	data, err := k.fetchJSON(ctx, treeURL, 8<<20)
	if err != nil {
		return nil, err
	}
	var d tree
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range d.SVG {
		out[strings.TrimSuffix(f, ".svg")] = "svg"
	}
	for _, f := range d.PNG {
		n := strings.TrimSuffix(f, ".png")
		if _, is := out[n]; !is {
			out[n] = "png"
		}
	}
	return out, nil
}

func (k *Catalog) setNames(ext map[string]string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ext = ext
	k.rebuildNamesLocked()
}

func (k *Catalog) setFromIndex(data []byte) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil || len(m) == 0 {
		return fmt.Errorf("bad icon index")
	}
	k.setNames(m)
	return nil
}

func (k *Catalog) fallbackFromDisk(file string) {
	if data, err := os.ReadFile(file); err == nil && k.setFromIndex(data) == nil {
		return
	}
	k.indexCache()
}

// indexCache builds the index from the files we already have — a panel cut off from
// the network still shows what it once downloaded.
func (k *Catalog) indexCache() {
	entries, err := os.ReadDir(k.cacheDir)
	if err != nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, e := range entries {
		n := e.Name()
		switch {
		case strings.HasSuffix(n, ".svg"):
			k.ext[strings.TrimSuffix(n, ".svg")] = "svg"
		case strings.HasSuffix(n, ".png"):
			b := strings.TrimSuffix(n, ".png")
			if _, is := k.ext[b]; !is {
				k.ext[b] = "png"
			}
		}
	}
	k.rebuildNamesLocked()
}

// ── reading a single icon ────────────────────────────────────────────────────

func (k *Catalog) Has(name string) bool {
	if badIconName(name) {
		return false
	}
	ref, explicit := strings.CutPrefix(name, selfhstPrefix)
	k.mu.RLock()
	_, inHomarr := k.ext[name]
	_, inSelfhst := k.selfhst.names[ref]
	k.mu.RUnlock()
	if (inHomarr && !explicit) || inSelfhst {
		return true
	}
	if k.cacheDir == "" {
		return false
	}
	dirs := []string{k.cacheDir, k.selfhstCacheDir()}
	if explicit {
		dirs = dirs[1:]
	}
	for _, dir := range dirs {
		for _, ext := range []string{"svg", "png"} {
			if _, err := os.Stat(filepath.Join(dir, ref+"."+ext)); err == nil {
				return true
			}
		}
	}
	return false
}

// Read returns the icon content and its MIME type.
// Order: zip → on-disk cache → CDN (and a write to the cache). A name Dashboard
// Icons does not know goes to selfh.st; `selfhst:<name>` goes there directly.
func (k *Catalog) Read(ctx context.Context, name string) ([]byte, string, error) {
	if badIconName(name) {
		return nil, "", fmt.Errorf("bad icon name")
	}
	if ref, ok := strings.CutPrefix(name, selfhstPrefix); ok {
		if ref == "" {
			return nil, "", fmt.Errorf("bad icon name")
		}
		return k.readSelfhst(ctx, ref)
	}

	k.mu.RLock()
	ext, known := k.ext[name]
	homarrIndexed := len(k.ext) > 0
	_, inSelfhst := k.selfhst.files[name]
	selfhstIndexed := k.selfhst.loaded
	zipIdx := k.zipIdx
	k.mu.RUnlock()
	// both indexes agree the name is selfh.st's alone — no detour through Dashboard
	// Icons. Without the Dashboard Icons index we cannot know that it lacks the
	// name, so it is still asked first (the name might be in both).
	if !known && inSelfhst && homarrIndexed {
		return k.readSelfhst(ctx, name)
	}
	if !known {
		ext = "svg"
	}

	if zipIdx != nil {
		if f, ok := zipIdx[name+".svg"]; ok {
			rc, err := f.Open()
			if err == nil {
				defer rc.Close()
				data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
				return data, "image/svg+xml", err
			}
		}
	}

	if data, kind, ok := readCached(k.cacheDir, name, ext); ok {
		return data, kind, nil
	}

	data, kind, err := k.fetchFrom(ctx, cdnIcons, k.cacheDir, name, ext)
	// a name Dashboard Icons does not list: selfh.st is the last resort when it has
	// the name, or when its index is missing (zip mode, no network at start)
	if err != nil && !known && (inSelfhst || !selfhstIndexed) {
		if sData, sKind, sErr := k.readSelfhst(ctx, name); sErr == nil {
			return sData, sKind, nil
		}
	}
	return data, kind, err
}

// readCached looks for name in a cache directory, the expected extension first.
func readCached(dir, name, ext string) ([]byte, string, bool) {
	if dir == "" {
		return nil, "", false
	}
	for _, e := range []string{ext, "svg", "png"} {
		// ! the file exists but has 0 bytes = the previous download left a poisoned
		// cache (see the comment in fetchFrom). We skip such a file and try to download
		// again, instead of serving emptiness over and over.
		if data, err := os.ReadFile(filepath.Join(dir, name+"."+e)); err == nil && len(data) > 0 {
			return data, iconKind(e), true
		}
	}
	return nil, "", false
}

// iconKind maps an icon file extension to a MIME type.
func iconKind(ext string) string {
	if ext == "png" {
		return "image/png"
	}
	return "image/svg+xml"
}

// fetchFrom downloads <base>/<ext>/<name>.<ext> and stores it in dir.
func (k *Catalog) fetchFrom(ctx context.Context, base, dir, name, ext string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/%s/%s.%s", base, ext, name, ext)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("CDN: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return nil, "", err
	}
	// ! The panel credits the icon collections as served unmodified, so a file over
	// the limit is refused, never cut at the limit and cached as a broken artwork.
	if len(data) > maxIconBytes {
		return nil, "", fmt.Errorf("CDN: %s.%s is larger than %d bytes", name, ext, maxIconBytes)
	}
	// ! jsDelivr is sometimes "warm" — status 200, but zero body bytes, until it has
	// itself fetched the file from GitHub. Without this guard such an empty payload
	// landed in the on-disk cache FOR GOOD (icon files have no TTL like
	// index.json), so one failed moment of the CDN poisoned the icon forever —
	// that was the real reason for "broken" tiles in the icon search.
	if len(data) == 0 {
		return nil, "", fmt.Errorf("CDN: empty response body for %s.%s", name, ext)
	}
	if dir != "" && os.MkdirAll(dir, 0o755) == nil {
		tmp := filepath.Join(dir, "."+name+"."+ext+".tmp")
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(dir, name+"."+ext))
		}
	}
	return data, iconKind(ext), nil
}

// Search returns the names matching the query: first those starting with the
// phrase, then those containing it in the middle.
func (k *Catalog) Search(q string, limit int) []string {
	k.mu.RLock()
	defer k.mu.RUnlock()

	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		if len(k.names) < limit {
			limit = len(k.names)
		}
		return append([]string(nil), k.names[:limit]...)
	}
	var prefix, middle []string
	for _, n := range k.names {
		l := strings.ToLower(n)
		switch {
		case strings.HasPrefix(l, q):
			prefix = append(prefix, n)
		case strings.Contains(l, q):
			middle = append(middle, n)
		default:
			// alias: "8311 community" should find the icon "8311"
			// (selfh.st's display name plays the alias: "llama.cpp" → llama-cpp)
			aliases := k.aliases[n]
			if aliases == nil {
				aliases = k.selfhst.aliases[n]
			}
			for _, a := range aliases {
				if strings.Contains(strings.ToLower(a), q) {
					middle = append(middle, n)
					break
				}
			}
		}
		if len(prefix) >= limit {
			break
		}
	}
	out := append(prefix, middle...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ── HTTP layer ───────────────────────────────────────────────────────────────

// iconRef tells the template how to render a service icon.
//
// When an icon has separate variants for the dark/light theme, Src stays empty and
// the template renders BOTH images at once (WariantCiemny/WariantJasny) — which one
// shows is decided by pure CSS on the data-tone attribute on <html>. The theme is a
// choice of the BROWSER (it can be switched without reloading the page, see
// appearance.tone in the config), so the server does not know which to show when
// rendering the page — it must give both and leave the decision to CSS, with zero
// extra JS or requests when the theme changes.
type iconRef struct {
	Src          string // path on our server, empty when missing or when there are variants
	Letter       string // fallback: a tile with a letter
	VariantDark  string // path to the variant for the DARK theme (light ink), empty = none
	VariantLight string // path to the variant for the LIGHT theme (dark ink), empty = none
}

func (s *Server) resolveIcon(icon, name string) iconRef {
	letter := firstLetter(name)
	switch {
	case icon == "":
		return iconRef{Letter: letter}
	case strings.HasPrefix(icon, "http://"), strings.HasPrefix(icon, "https://"):
		return iconRef{Src: "/icon/proxy?u=" + urlEscape(icon), Letter: letter}
	default:
		n := strings.TrimSuffix(strings.TrimSuffix(icon, ".svg"), ".png")
		if dark, light := s.icons.Variants(n); dark != "" && light != "" {
			return iconRef{VariantDark: "/icon/" + dark, VariantLight: "/icon/" + light, Letter: letter}
		}
		// even when the index did not load, try — the worst case is a 404
		// and the card shows a letter
		return iconRef{Src: "/icon/" + n, Letter: letter}
	}
}

func urlEscape(s string) string {
	r := strings.NewReplacer("&", "%26", "?", "%3F", " ", "%20", "#", "%23")
	return r.Replace(s)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "proxy" {
		s.handleIconProxy(w, r)
		return
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".svg"), ".png")

	data, kind, err := s.icons.Read(r.Context(), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	// an icon can be SVG, i.e. a document from our origin — and script-src has
	// 'unsafe-inline'. The sandbox closes this without taking away the icons' ability to work.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// an icon under a given name is immutable, so a week in the cache is
	// fine — unlike /bg/, where the file is sometimes replaced
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(data)
}

// iconKinds — the only thing allowed to be passed on from a foreign server.
var iconKinds = map[string]bool{
	"image/svg+xml": true, "image/png": true, "image/jpeg": true,
	"image/webp": true, "image/gif": true, "image/x-icon": true,
	"image/vnd.microsoft.icon": true,
}

// externalIconClient fetches icons from outside the panel.
//
// ! The block on private addresses sits in Dialer.Control, i.e. it WORKS ON THE
// ADDRESS THAT WAS ACTUALLY CHOSEN — checking the host before the request is
// bypassed with DNS that returns a public address once and a local one the next time.
// Without this, `icon: http://192.168.0.1/…` in the YAML turns the panel into a
// home network scanner.
func externalIconClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: blockLocalAddresses}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func blockLocalAddresses(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("unrecognized address %q", host)
	}
	if !publicAddress(ip) {
		return fmt.Errorf("local address %s is blocked", ip)
	}
	return nil
}

// notPublic lists ranges that look routable but are not the open internet — or
// that tunnel to an address the filter below would block if it saw it directly.
// The stdlib helpers (IsPrivate, IsLoopback, …) do not know them.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"; 0.x.x.x connects to the local host on Linux
	netip.MustParsePrefix("100.64.0.0/10"),  // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking, used for fake-IP DNS by some VPNs
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, incl. 255.255.255.255
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64: embeds an IPv4 address (10.0.0.1 → 64:ff9b::a00:1)
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4: embeds an IPv4 address
	netip.MustParsePrefix("2001::/32"),      // Teredo: embeds an IPv4 address
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// handleIconProxy handles icons given as a full address — the server fetches
// them, not the browser.
func (s *Server) handleIconProxy(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("u")
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		plainError(w, r, http.StatusBadRequest, "error_icon_url")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src, nil)
	if err != nil {
		plainError(w, r, http.StatusBadRequest, "error_icon_url")
		return
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	client := s.proxyClient
	if client == nil {
		client = externalIconClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("icon proxy: fetch failed", "err", err)
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("icon proxy: upstream refused", "status", resp.Status)
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	// ! The Content-Type from the upstream used to be copied here VERBATIM, so
	// `icon: https://bad/x` returned text/html from OUR origin — i.e. XSS
	// with script-src 'unsafe-inline'. Now only the allowlist passes.
	kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !iconKinds[kind] {
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		slog.Warn("icon proxy: read failed", "err", err)
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(data)
}
