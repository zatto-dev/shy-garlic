package server

import (
	"archive/zip"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
// server either has the icon on disk, or pulls it once from the icon source
// (icons.zatto.dev by default, --icons-url for your own mirror) and saves it. The
// source never sees the user's browser — it sees one server with no context.
//
// What lives where (the point is a light panel):
//   - the index of every icon name (search, aliases, variants): a SQLite file, see
//     icons_index.go — not in memory;
//   - the files of the icons your SERVICES use, plus their dark/light variants:
//     <cache>/<name>.svg|png. Fetched when a service is added (and at start for
//     existing ones), removed when the service is gone. That is all that ever
//     lands in the data directory, apart from the index file;
//   - picker thumbnails and any other icon nobody saved: a small in-memory LRU
//     (thumbCacheBytes) — never on disk;
//   - `zip` mode (an optional icons.zip file) gives a set right away, without
//     downloading any index — for networks cut off from the internet. A name the zip
//     does not have is still looked up at the source when asked for, never ahead of
//     time.

const (
	maxIconBytes   = 2 << 20
	thumbCacheMax  = 256     // entries
	thumbCacheSize = 2 << 20 // bytes of icon data held for pickers
	staleTmpAge    = time.Hour
)

type Catalog struct {
	mu       sync.RWMutex
	idx      *iconIndex // nil until an index file loaded
	names    []string   // zip mode only: sorted names in the zip
	cacheDir string
	baseURL  string // icon source, no trailing slash
	mode     string // "zip" or "cdn"
	zipIdx   map[string]*zip.File
	client   *http.Client
	life     context.Context // set by Refresh; background downloads end with it

	keep     map[string]bool // file names (no extension) the services use: the only ones kept on disk
	wanted   []string        // the names last given to Retain, replayed when the index changes
	retained bool            // Retain was called at least once (before that nothing is known to be unused)
	retain   sync.Mutex      // one Retain at a time (prune + prefetch)

	thumbs thumbCache
	net    netGuard
}

// lifetime is the context background downloads run under.
func (k *Catalog) lifetime() context.Context {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.life == nil {
		return context.Background()
	}
	return k.life
}

// Authoritative tells whether name routing rests on a full index. Without it a name
// might resolve differently later, so browsers keep icons only briefly.
func (k *Catalog) Authoritative() bool { return k.index() != nil }

// badIconName rejects anything that could leave the cache directory.
func badIconName(name string) bool {
	return strings.ContainsAny(name, `/\`) || strings.Contains(name, "..")
}

// NewCatalog is NewCatalogAt with the default icon source.
func NewCatalog(zipPath, cacheDir string) *Catalog {
	return NewCatalogAt("", zipPath, cacheDir)
}

// NewCatalogAt builds the catalog for an icon source (empty = icons.zatto.dev). It
// opens the index file from the last run right away (milliseconds), so the panel can
// search and resolve names before the first network round trip.
func NewCatalogAt(baseURL, zipPath, cacheDir string) *Catalog {
	if b, err := NormalizeIconsURL(baseURL); err == nil {
		baseURL = b
	} else {
		slog.Warn("bad icon source, using the default", "err", err)
		baseURL = DefaultIconsURL
	}
	k := &Catalog{
		cacheDir: cacheDir,
		baseURL:  baseURL,
		mode:     "cdn",
		client:   newIconClient(),
	}
	if zipPath != "" {
		if err := k.loadZip(zipPath); err != nil {
			slog.Warn("could not load icons.zip, falling back to the icon source", "file", zipPath, "err", err)
		} else {
			k.mode = "zip"
		}
	}
	if k.cacheDir != "" {
		_ = os.MkdirAll(k.cacheDir, 0o755)
	}
	k.loadIndexFromDisk()
	return k
}

// Base is the icon source the credit links to.
func (k *Catalog) Base() string { return k.baseURL }

func (k *Catalog) Mode() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.mode
}

func (k *Catalog) Count() int {
	k.mu.RLock()
	x, n := k.idx, len(k.names)
	k.mu.RUnlock()
	if x != nil {
		return x.count
	}
	if n > 0 {
		return n
	}
	return len(k.cachedNames())
}

// cachedNames lists the icons on disk: the picker of a panel that never got an index
// still shows what its services use.
func (k *Catalog) cachedNames() []string {
	entries, err := os.ReadDir(k.cacheDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || e.IsDir() {
			continue
		}
		if b, ok := strings.CutSuffix(n, ".svg"); ok {
			out = append(out, b)
		} else if b, ok := strings.CutSuffix(n, ".png"); ok {
			out = append(out, b)
		}
	}
	sort.Strings(out)
	return slicesCompact(out)
}

func slicesCompact(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Variants returns the file names with dark and light ink for a given icon, when
// the catalog keeps such a pair. An empty string in both fields = this icon
// has no separate variants, the caller should use the plain base name.
func (k *Catalog) Variants(name string) (dark, light string) {
	n, ok := canonicalName(name)
	x := k.index()
	if !ok || x == nil {
		return "", ""
	}
	files, ok := x.variants(n)
	if !ok {
		return "", ""
	}
	return files[0].File, files[1].File
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
		k.names = append(k.names, strings.TrimSuffix(f.Name, ".svg"))
	}
	sort.Strings(k.names)
	slog.Info("icon catalog from file", "file", path, "icons", len(k.names))
	return nil
}

// ── reading a single icon ────────────────────────────────────────────────────

// fileFor resolves a name to the file that serves it. Without an index the name is
// taken literally with an unknown extension (svg first, then png), as the panel
// always did before an index was in.
func (k *Catalog) fileFor(name string) (iconFile, bool) {
	n, ok := canonicalName(name)
	if !ok {
		return iconFile{}, false
	}
	if x := k.index(); x != nil {
		if f, ok := x.resolve(n); ok {
			return f, true
		}
		return iconFile{}, false
	}
	return iconFile{File: n}, true
}

func (k *Catalog) Has(name string) bool {
	n, ok := canonicalName(name)
	if !ok {
		return false
	}
	if x := k.index(); x != nil {
		if _, ok := x.resolve(n); ok {
			return true
		}
	}
	_, _, ok = readCached(k.cacheDir, n, "")
	return ok
}

// Read returns the icon content and its MIME type.
// Order: zip → disk (the services' icons) → memory (thumbnails) → network (see
// icons_fetch.go: at most iconWaitBudget of waiting, the download itself goes on in
// the background).
func (k *Catalog) Read(ctx context.Context, name string) ([]byte, string, error) {
	n, ok := canonicalName(name)
	if !ok {
		return nil, "", fmt.Errorf("bad icon name")
	}

	k.mu.RLock()
	zipIdx := k.zipIdx
	k.mu.RUnlock()
	if zipIdx != nil {
		if f, ok := zipIdx[n+".svg"]; ok {
			rc, err := f.Open()
			if err == nil {
				defer rc.Close()
				data, err := io.ReadAll(io.LimitReader(rc, maxIconBytes+1))
				if err == nil && len(data) > maxIconBytes {
					err = fmt.Errorf("icons.zip: %s.svg is larger than %d bytes", n, maxIconBytes)
				}
				if err != nil {
					return nil, "", err
				}
				return data, "image/svg+xml", nil
			}
		}
	}

	f, ok := k.fileFor(n)
	if !ok {
		// the index forgot the name (or never had it), but an older download may still
		// be on disk and a service may still be using it
		if data, kind, ok := readCached(k.cacheDir, n, ""); ok {
			return data, kind, nil
		}
		return nil, "", errNotFound
	}
	if data, kind, ok := readCached(k.cacheDir, f.File, f.Ext); ok {
		return data, kind, nil
	}
	if data, kind, ok := k.thumbs.get(f.File); ok {
		return data, kind, nil
	}
	return k.readFromNetwork(ctx, f.File, k.sourcesFor(f))
}

// readCached looks for name in a cache directory, the expected extension first.
func readCached(dir, name, ext string) ([]byte, string, bool) {
	if dir == "" {
		return nil, "", false
	}
	exts := []string{"svg", "png"}
	if ext == "png" {
		exts = []string{"png", "svg"}
	}
	for _, e := range exts {
		// ! the file exists but has 0 bytes = a previous download left a poisoned
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

// writeFileAtomic puts data at dir/name: a temporary file in the same directory
// (unique per call, so parallel writers of one name never share a file), fsync, then
// rename. Readers see the old file or the whole new one, never a part.
func writeFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// fetchFrom downloads <base>/<ext>/<name>.<ext> and, when dir is not empty, stores it
// there. A 404 comes back as errNotFound; a host that does not answer (or answers
// 5xx) goes on cooldown, see icons_fetch.go.
func (k *Catalog) fetchFrom(ctx context.Context, base, dir, name, ext string) ([]byte, string, error) {
	host := hostOf(base)
	if k.net.hostDown(host) {
		return nil, "", fmt.Errorf("icon source %s on cooldown after a failure", host)
	}
	url := fmt.Sprintf("%s/%s/%s.%s", base, ext, neturl.PathEscape(name), ext)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)

	resp, err := k.client.Do(req)
	if err != nil {
		k.net.markDown(host)
		return nil, "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, "", fmt.Errorf("icon source: %s.%s: %w", name, ext, errNotFound)
	case resp.StatusCode >= 500:
		k.net.markDown(host)
		return nil, "", fmt.Errorf("icon source: %s", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("icon source: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		k.net.markDown(host)
		return nil, "", err
	}
	// ! A file over the limit is refused, never cut at the limit and cached as a
	// broken artwork.
	if len(data) > maxIconBytes {
		return nil, "", fmt.Errorf("icon source: %s.%s is larger than %d bytes", name, ext, maxIconBytes)
	}
	// ! A CDN is sometimes "warm" — status 200, but zero body bytes, until it has
	// itself fetched the file. Without this guard such an empty payload landed in the
	// on-disk cache FOR GOOD (icon files have no TTL), so one failed moment of the
	// CDN poisoned the icon forever.
	if len(data) == 0 {
		return nil, "", fmt.Errorf("icon source: empty response body for %s.%s", name, ext)
	}
	if dir != "" {
		// ! a unique temporary file per call (SHYG-59): a fixed ".<name>.tmp" was shared by
		// parallel downloads of one icon, which could rename a half-written file
		if err := writeFileAtomic(dir, name+"."+ext, data); err != nil {
			slog.Debug("icon not saved", "icon", name, "err", err)
		}
	}
	return data, iconKind(ext), nil
}

// Search returns the names matching the query: first those starting with the
// phrase, then those containing it in the middle or in an alias or display name.
func (k *Catalog) Search(q string, limit int) []string {
	q = cleanQuery(q)
	if x := k.index(); x != nil {
		out, err := x.search(q, limit)
		if err != nil {
			slog.Debug("icon search failed", "err", err)
			return []string{}
		}
		return out
	}
	k.mu.RLock()
	names := k.names
	k.mu.RUnlock()
	if names == nil {
		names = k.cachedNames()
	}
	var prefix, middle []string
	for _, n := range names {
		switch l := strings.ToLower(n); {
		case q == "" || strings.HasPrefix(l, q):
			prefix = append(prefix, n)
		case strings.Contains(l, q):
			middle = append(middle, n)
		}
	}
	out := append(prefix, middle...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ── what the services use ────────────────────────────────────────────────────

// filesOf lists the files an icon name needs: itself and, when it has them, its
// dark/light pair. Without an index the name is kept literally.
func (k *Catalog) filesOf(name string) []iconFile {
	n, ok := canonicalName(name)
	if !ok {
		return nil
	}
	x := k.index()
	if x == nil {
		return []iconFile{{File: n}}
	}
	f, ok := x.resolve(n)
	if !ok {
		return []iconFile{{File: n}} // unknown to the index: keep a file that may already be there
	}
	out := []iconFile{f}
	if pair, ok := x.variants(n); ok {
		out = append(out, pair[0], pair[1])
	}
	return out
}

// Retain tells the catalog which icons the services use. Those are fetched in the
// background when missing (a picker thumbnail the user just chose is moved from memory
// without a download), and every other icon file in the cache is removed — the data
// directory holds the icons of services that exist, nothing else.
func (k *Catalog) Retain(ctx context.Context, names []string) {
	k.retain.Lock()
	defer k.retain.Unlock()
	k.mu.Lock()
	k.wanted = append([]string(nil), names...)
	k.retained = true
	k.mu.Unlock()
	if k.cacheDir == "" || k.Mode() == "zip" {
		// zip mode is for networks without internet; nothing is fetched ahead of time
		k.mu.Lock()
		k.keep = map[string]bool{}
		k.mu.Unlock()
		return
	}

	keep := map[string]bool{}
	var todo []iconFile
	for _, n := range names {
		for _, f := range k.filesOf(n) {
			if !keep[f.File] {
				keep[f.File] = true
				todo = append(todo, f)
			}
		}
	}
	k.mu.Lock()
	k.keep = keep
	k.mu.Unlock()

	// pruning needs the index: before it loads, a name that is an alias would look
	// unused and its file would be deleted
	if k.index() != nil {
		k.prune(keep)
	}
	if k.index() == nil {
		return // names resolve only with the index; Refresh calls Retain again
	}

	var fetch []iconFile
	for _, f := range todo {
		if _, _, ok := readCached(k.cacheDir, f.File, f.Ext); ok {
			continue
		}
		if data, kind, ok := k.thumbs.get(f.File); ok {
			ext := "svg"
			if kind == "image/png" {
				ext = "png"
			}
			if writeFileAtomic(k.cacheDir, f.File+"."+ext, data) == nil {
				continue
			}
		}
		if !k.net.missing(f.File) {
			fetch = append(fetch, f)
		}
	}
	if len(fetch) == 0 {
		return
	}
	go func() {
		sem := make(chan struct{}, prefetchWorkers)
		var wg sync.WaitGroup
	loop:
		for _, f := range fetch {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break loop
			}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				fl := k.fetchIcon(f.File, k.sourcesFor(f))
				if fl == nil {
					return
				}
				<-fl.done
				if fl.err != nil && !errors.Is(fl.err, errNotFound) {
					slog.Debug("icon prefetch failed", "icon", f.File, "err", fl.err)
				}
			}()
		}
		wg.Wait()
	}()
}

// retainAgain replays the last Retain after the index changed (aliases may now
// resolve differently).
func (k *Catalog) retainAgain() {
	k.mu.RLock()
	names, retained, ctx := append([]string(nil), k.wanted...), k.retained, k.life
	k.mu.RUnlock()
	if !retained {
		return // the config has not said yet what it uses; pruning now would delete it all
	}
	if ctx == nil {
		ctx = context.Background()
	}
	k.Retain(ctx, names)
}

// isKept tells whether a file belongs to a service and so goes to disk.
func (k *Catalog) isKept(file string) bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.keep[file]
}

// prune removes from the cache directory everything a service does not use: icon
// files, files of the previous layout (index.json, spis.json, the selfhst/
// directory) and temporary files left by a crash. The index file is never touched.
func (k *Catalog) prune(keep map[string]bool) {
	entries, err := os.ReadDir(k.cacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		path := filepath.Join(k.cacheDir, n)
		switch {
		case e.IsDir():
			if n == "selfhst" { // layout of 0.15
				_ = os.RemoveAll(path)
			}
		case n == indexFileName || n == indexMetaName:
		case n == "index.json" || n == legacyIndexFile:
			_ = os.Remove(path)
		case strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".tmp"):
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleTmpAge {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(n, ".svg") || strings.HasSuffix(n, ".png"):
			if !keep[strings.TrimSuffix(strings.TrimSuffix(n, ".svg"), ".png")] {
				_ = os.Remove(path)
			}
		}
	}
}

// legacyIndexFile is the pre-0.14 name of the old name-index cache.
const legacyIndexFile = "spis.json"

// ── thumbnails ───────────────────────────────────────────────────────────────

// thumbCache is a small LRU of icon bytes: the picker's tiles (a page of 48 icons
// nobody has saved) live here for a while instead of in the data directory.
type thumbCache struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
	bytes int
}

type thumbItem struct {
	key  string
	data []byte
	kind string
}

func (c *thumbCache) get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	c.order.MoveToFront(el)
	it := el.Value.(*thumbItem)
	return it.data, it.kind, true
}

func (c *thumbCache) put(key string, data []byte, kind string) {
	if len(data) == 0 || len(data) > thumbCacheSize/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*list.Element{}
		c.order = list.New()
	}
	if el, ok := c.items[key]; ok {
		c.bytes -= len(el.Value.(*thumbItem).data)
		c.order.Remove(el)
		delete(c.items, key)
	}
	c.items[key] = c.order.PushFront(&thumbItem{key: key, data: data, kind: kind})
	c.bytes += len(data)
	for c.bytes > thumbCacheSize || c.order.Len() > thumbCacheMax {
		last := c.order.Back()
		it := last.Value.(*thumbItem)
		c.bytes -= len(it.data)
		delete(c.items, it.key)
		c.order.Remove(last)
	}
}
