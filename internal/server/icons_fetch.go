package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Getting an icon from the network never holds up the page.
//
// ! The panel's promise is to be lighter and faster than anything else, so the
// network sits behind three guards:
//   - a request for an icon that is not on disk waits at most iconWaitBudget for
//     the download, for ALL sources together; past that it gets a 404 (the card
//     shows its letter) while the download finishes in the background, so the
//     next render has the file. Concurrent requests for one name share one
//     download.
//   - a name that every source answered 404 for is not asked again for
//     notFoundTTL;
//   - a CDN host that failed (timeout, refused, 5xx) is skipped for
//     hostCooldown, so an offline panel answers at once instead of after a
//     connect timeout per icon. Before this, a firewall dropping packets cost
//     40 s per icon (20 s client timeout × two catalogs).
const (
	iconWaitBudget  = 1500 * time.Millisecond
	iconFetchBudget = 8 * time.Second  // one background download, all sources
	indexBudget     = 30 * time.Second // index files are megabytes, fetched in the background
	notFoundTTL     = 10 * time.Minute
	hostCooldown    = 2 * time.Minute
	maxNotFound     = 4096 // bound on remembered misses; names come from requests
	prefetchWorkers = 4
)

// errNotFound means the source answered and does not have the file — the only
// answer after which the next source is asked.
var errNotFound = errors.New("icon not found")

// errIconPending means the download did not finish within iconWaitBudget; it
// goes on in the background.
var errIconPending = errors.New("icon download in progress")

// newIconClient gives short connection-level timeouts; the overall limit comes
// from the context of each call (iconFetchBudget / indexBudget).
func newIconClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 3 * time.Second
	t.ResponseHeaderTimeout = 5 * time.Second
	return &http.Client{Transport: t}
}

type iconSource struct {
	base string // CDN prefix, <base>/<ext>/<name>.<ext>
	dir  string // cache directory of this source
	name string // file name in the source
	ext  string // "" = unknown, try svg then png
}

type iconFlight struct {
	done chan struct{}
	data []byte
	kind string
	err  error
}

// netGuard holds the shared state of the network side: running downloads,
// remembered misses and hosts on cooldown.
type netGuard struct {
	mu       sync.Mutex
	flights  map[string]*iconFlight
	notFound map[string]time.Time // key → until
	downTill map[string]time.Time // host → until
}

func (g *netGuard) init() {
	if g.flights == nil {
		g.flights = map[string]*iconFlight{}
		g.notFound = map[string]time.Time{}
		g.downTill = map[string]time.Time{}
	}
}

func (g *netGuard) missing(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	until, ok := g.notFound[key]
	return ok && time.Now().Before(until)
}

func (g *netGuard) rememberMissing(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	if len(g.notFound) >= maxNotFound {
		clear(g.notFound)
	}
	g.notFound[key] = time.Now().Add(notFoundTTL)
}

// forgetMisses runs when an index changes: a name missing a moment ago may now
// route to a source that has it.
func (g *netGuard) forgetMisses() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	clear(g.notFound)
}

func (g *netGuard) hostDown(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	return time.Now().Before(g.downTill[host])
}

func (g *netGuard) markDown(host string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	g.downTill[host] = time.Now().Add(hostCooldown)
}

// sourcesFor decides where a name comes from, in order. Dashboard Icons first;
// selfh.st when its name, or as a fallback when the indexes cannot tell.
func (k *Catalog) sourcesFor(name string) []iconSource {
	homarr := func(n, ext string) iconSource {
		return iconSource{base: cdnIcons, dir: k.cacheDir, name: n, ext: ext}
	}
	selfhst := func(n, ext string) iconSource {
		return iconSource{base: selfhstCDN, dir: k.selfhstCacheDir(), name: n, ext: ext}
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	if ref, ok := strings.CutPrefix(name, selfhstPrefix); ok {
		ext, _ := k.selfhst.fileExt(ref)
		return []iconSource{selfhst(ref, ext)}
	}
	if ext, ok := k.homarrFileLocked(name); ok {
		return []iconSource{homarr(name, ext)}
	}
	sExt, inSelfhst := k.selfhst.fileExt(name)
	if inSelfhst && k.homarrAuthoritative {
		return []iconSource{selfhst(name, sExt)}
	}
	out := []iconSource{homarr(name, "")}
	if inSelfhst || !k.selfhst.loaded {
		out = append(out, selfhst(name, sExt))
	}
	return out
}

// fetchIcon is one background download along the sources, shared by everyone
// asking for the same name.
func (k *Catalog) fetchIcon(name string, sources []iconSource) *iconFlight {
	g := &k.net
	g.mu.Lock()
	g.init()
	if f, ok := g.flights[name]; ok {
		g.mu.Unlock()
		return f
	}
	f := &iconFlight{done: make(chan struct{})}
	g.flights[name] = f
	g.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), iconFetchBudget)
		defer cancel()
		f.data, f.kind, f.err = k.walkSources(ctx, name, sources)
		g.mu.Lock()
		delete(g.flights, name)
		g.mu.Unlock()
		close(f.done)
	}()
	return f
}

func (k *Catalog) walkSources(ctx context.Context, key string, sources []iconSource) ([]byte, string, error) {
	for i, src := range sources {
		// a later source's cache counts only once the earlier sources said 404 —
		// otherwise a selfh.st file could shadow a Dashboard Icons one of the same name
		if i > 0 {
			if data, kind, ok := readCached(src.dir, src.name, src.ext); ok {
				return data, kind, nil
			}
		}
		exts := []string{src.ext}
		if src.ext == "" {
			exts = []string{"svg", "png"}
		}
		for _, ext := range exts {
			data, kind, err := k.fetchFrom(ctx, src.base, src.dir, src.name, ext)
			if err == nil {
				return data, kind, nil
			}
			if !errors.Is(err, errNotFound) {
				// network trouble or a broken answer: not a reason to try another
				// catalog, and not a miss to remember
				return nil, "", err
			}
		}
	}
	k.net.rememberMissing(key)
	return nil, "", errNotFound
}

// readFromNetwork starts (or joins) the download and waits at most
// iconWaitBudget for it.
func (k *Catalog) readFromNetwork(ctx context.Context, name string, sources []iconSource) ([]byte, string, error) {
	if k.net.missing(name) {
		return nil, "", errNotFound
	}
	f := k.fetchIcon(name, sources)
	wait := time.NewTimer(iconWaitBudget)
	defer wait.Stop()
	select {
	case <-f.done:
		return f.data, f.kind, f.err
	case <-wait.C:
		return nil, "", errIconPending
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

// Prefetch downloads, in the background, the icons the config uses (and their
// dark/light variants) that are not on disk yet, so the first render has them.
func (k *Catalog) Prefetch(ctx context.Context, names []string) {
	if k.cacheDir == "" {
		return // nothing to keep the files in, so nothing gained
	}
	var todo []string
	seen := map[string]bool{}
	add := func(n string) {
		if n == "" || seen[n] || badIconName(n) {
			return
		}
		seen[n] = true
		todo = append(todo, n)
	}
	for _, n := range names {
		add(n)
		if dark, light := k.Variants(n); dark != "" {
			add(dark)
			add(light)
		}
	}
	if len(todo) == 0 {
		return
	}
	go func() {
		sem := make(chan struct{}, prefetchWorkers)
		var wg sync.WaitGroup
		for _, n := range todo {
			if ctx.Err() != nil {
				break
			}
			if k.onDiskOrZip(n) || k.net.missing(n) {
				continue
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				f := k.fetchIcon(n, k.sourcesFor(n))
				<-f.done
				if f.err != nil && !errors.Is(f.err, errNotFound) {
					slog.Debug("icon prefetch failed", "icon", n, "err", f.err)
				}
			}()
		}
		wg.Wait()
	}()
}

// onDiskOrZip tells whether Read can answer without the network.
func (k *Catalog) onDiskOrZip(name string) bool {
	k.mu.RLock()
	zipIdx := k.zipIdx
	k.mu.RUnlock()
	if zipIdx != nil {
		if _, ok := zipIdx[name+".svg"]; ok {
			return true
		}
	}
	src := k.sourcesFor(name)[0]
	_, _, ok := readCached(src.dir, src.name, src.ext)
	return ok
}
