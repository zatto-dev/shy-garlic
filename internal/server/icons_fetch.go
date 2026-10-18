package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Getting an icon from the network never holds up the page.
//
// ! The panel's promise is to be lighter and faster than anything else, so the
// network sits behind three guards:
//   - a request for an icon that is not on disk waits at most iconWaitBudget for
//     the download; past that it gets a 404 (the card
//     shows its letter) while the download finishes in the background, so the
//     next render has the file. Concurrent requests for one name share one
//     download.
//   - a name the source answered 404 for is not asked again for notFoundTTL;
//   - a host that failed (timeout, refused, 5xx) is skipped for hostCooldown, so
//     an offline panel answers at once instead of after a connect timeout per
//     icon. Before this, a firewall dropping packets cost 40 s per icon.
const (
	iconWaitBudget  = 1500 * time.Millisecond
	iconFetchBudget = 8 * time.Second  // one background download, all sources
	indexBudget     = 30 * time.Second // the index is about a megabyte, fetched in the background
	notFoundTTL     = 10 * time.Minute
	hostCooldown    = 2 * time.Minute
	maxNotFound     = 4096 // bound on remembered misses; names come from requests
	prefetchWorkers = 4
	maxIconFlights  = 32 // downloads running at once; above that a request gets "pending"
	maxConnsPerHost = 8
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
	t.MaxConnsPerHost = maxConnsPerHost
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

// sourcesFor says where one icon file comes from. A file a service uses is stored in
// the cache directory; any other (a picker thumbnail) is only handed back and kept
// in memory, so the data directory never fills with icons nobody saved.
func (k *Catalog) sourcesFor(f iconFile) []iconSource {
	dir := ""
	if k.isKept(f.File) {
		dir = k.cacheDir
	}
	return []iconSource{{base: k.baseURL, dir: dir, name: f.File, ext: f.Ext}}
}

// fetchIcon is one background download, shared by everyone asking for the same name.
// nil = maxIconFlights downloads are already running.
func (k *Catalog) fetchIcon(name string, sources []iconSource) *iconFlight {
	g := &k.net
	g.mu.Lock()
	g.init()
	if f, ok := g.flights[name]; ok {
		g.mu.Unlock()
		return f
	}
	if len(g.flights) >= maxIconFlights {
		g.mu.Unlock()
		return nil
	}
	f := &iconFlight{done: make(chan struct{})}
	g.flights[name] = f
	g.mu.Unlock()

	go func() {
		// tied to the catalog's lifetime, not to the request that started it
		ctx, cancel := context.WithTimeout(k.lifetime(), iconFetchBudget)
		defer cancel()
		f.data, f.kind, f.err = k.walkSources(ctx, name, sources)
		if f.err == nil && len(sources) > 0 && sources[0].dir == "" {
			k.thumbs.put(name, f.data, f.kind)
		}
		g.mu.Lock()
		delete(g.flights, name)
		g.mu.Unlock()
		close(f.done)
	}()
	return f
}

// walkSources fetches the first source that answers. There is one source today (the
// icon server, or a mirror of it); the list stays a list so a second one is a data
// change, not a rewrite. A 404 is the only answer that moves on to the next source
// and the only one remembered as a miss; a network failure is neither.
func (k *Catalog) walkSources(ctx context.Context, key string, sources []iconSource) ([]byte, string, error) {
	for _, src := range sources {
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
	if f == nil {
		return nil, "", errIconPending
	}
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
