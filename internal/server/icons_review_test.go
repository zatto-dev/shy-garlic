package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// ── the cache directory is deleted from only when it is ours ─────────────────

func TestPruneLeavesAForeignDirectoryAlone(t *testing.T) {
	m := newIconServer(t, fixtureIcons())
	dir := t.TempDir()
	foreign := map[string]string{
		"holiday.png": "photo", "logo.svg": "<svg/>", "index.json": `["not","ours"]`, "spis.json": "x",
		"selfhst/notes.txt": "mine", "notes.txt": "hello",
	}
	for f, body := range foreign {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for run := 1; run <= 2; run++ { // the second start must not mistake the first one's index file for proof
		k := NewCatalogAt(m.srv.URL, "", dir)
		k.Refresh(context.Background())
		k.Retain(context.Background(), []string{"jellyfin"})
		waitFor(t, "jellyfin.svg", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })
		k.Retain(context.Background(), nil)
		for f := range foreign {
			if !fileExists(filepath.Join(dir, f)) {
				t.Errorf("run %d: foreign file %s was deleted", run, f)
			}
		}
		if fileExists(filepath.Join(dir, ownerMarker)) {
			t.Errorf("run %d: a foreign directory got our marker", run)
		}
		if k.owned {
			t.Errorf("run %d: a foreign directory counts as ours", run)
		}
	}
}

func TestPruneAdoptsTheOldLayoutButOnlyWhatIsRecognizable(t *testing.T) {
	m := newIconServer(t, fixtureIcons())
	dir := t.TempDir()
	write := func(f, body string) {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.json", `{"jellyfin":"svg"}`) // the name index of 0.15: proof the directory is ours
	write("spis.json", `{"plex":"svg"}`)
	write("old.svg", "<svg/>")
	write("selfhst/a.svg", "<svg/>")
	write("selfhst/.b.svg.1.tmp", "x")

	k := NewCatalogAt(m.srv.URL, "", dir)
	k.Refresh(context.Background())
	k.Retain(context.Background(), []string{"jellyfin"})
	waitFor(t, "jellyfin.svg", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })
	for _, f := range []string{"index.json", "spis.json", "old.svg", "selfhst"} {
		if fileExists(filepath.Join(dir, f)) {
			t.Errorf("%s of the old layout was not cleaned up", f)
		}
	}
	if !fileExists(filepath.Join(dir, ownerMarker)) {
		t.Error("an adopted directory got no marker")
	}

	// the same, but the old selfhst/ holds something that is not an icon
	dir2 := t.TempDir()
	dir = dir2
	write("index.json", `{"jellyfin":"svg"}`)
	write("selfhst/a.svg", "<svg/>")
	write("selfhst/readme.md", "mine")
	k2 := NewCatalogAt(m.srv.URL, "", dir2)
	k2.Refresh(context.Background())
	k2.Retain(context.Background(), nil)
	if !fileExists(filepath.Join(dir2, "selfhst", "readme.md")) {
		t.Error("selfhst/ with a foreign file was removed")
	}
}

// ── Retain reads the config when its turn comes ──────────────────────────────

func TestRetainOfAStaleSnapshotCannotDeleteANewServicesIcon(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	var mu sync.Mutex
	current := []string{"jellyfin"}
	get := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(current) }
	k.RetainFrom(context.Background(), get)
	waitFor(t, "jellyfin.svg", func() bool { return fileExists(filepath.Join(dir, "jellyfin.svg")) })

	// an older Retain is waiting for its turn (the lock is held by a newer one) ...
	k.retain.Lock()
	done := make(chan struct{})
	go func() { k.RetainFrom(context.Background(), get); close(done) }()
	time.Sleep(50 * time.Millisecond)
	// ... while the config gains service B, whose file arrives
	mu.Lock()
	current = []string{"jellyfin", "plex"}
	mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "plex.svg"), []byte("<svg>plex</svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	k.retain.Unlock()
	<-done

	if !fileExists(filepath.Join(dir, "plex.svg")) {
		t.Fatal("a Retain that began before the config changed deleted the new service's icon")
	}
	if !k.isKept("plex") {
		t.Error("the keep list is from the stale config")
	}
}

// ── a download that began as a thumbnail ─────────────────────────────────────

func TestRetainSavesADownloadItJoinedThatWasMeantForTheThumbnailCache(t *testing.T) {
	k, m, dir := loadedCatalog(t)
	m.mu.Lock()
	m.delay["/svg/plex.svg"] = 400 * time.Millisecond
	m.mu.Unlock()
	go func() { _, _, _ = k.Read(context.Background(), "plex") }() // nobody uses plex yet: no file
	waitFor(t, "the download to start", func() bool { return m.hitsOf("/svg/plex.svg") > 0 })

	k.Retain(context.Background(), []string{"plex"}) // the service is added while it runs
	waitFor(t, "plex.svg", func() bool { return fileExists(filepath.Join(dir, "plex.svg")) })
	if got, _ := os.ReadFile(filepath.Join(dir, "plex.svg")); string(got) != "<svg>plex</svg>" {
		t.Errorf("plex.svg = %q", got)
	}
}

// ── names of old configs ─────────────────────────────────────────────────────

func TestCatalogNameAcceptsTheSpellingsOfOldConfigs(t *testing.T) {
	for in, want := range map[string]string{
		"Home Assistant": "home-assistant", "Home_Assistant": "home_assistant", "Uptime Kuma": "uptime-kuma",
		"selfhst:Nginx Proxy Manager": "nginx-proxy-manager", "Pi-Hole.SVG": "pi-hole", "jellyfin": "jellyfin",
		"  Paperless NGX ": "paperless-ngx",
	} {
		if got, ok := catalogName(in); !ok || got != want {
			t.Errorf("catalogName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "../x", "a/b", `a\b`, "a..b", "ż", "   ", "selfhst:"} {
		if got, ok := catalogName(in); ok {
			t.Errorf("catalogName(%q) = %q, want a refusal", in, got)
		}
	}
}

func TestOldConfigNamesFindTheirIcon(t *testing.T) {
	k, _, dir := loadedCatalog(t)
	if !k.Has("Home Assistant") {
		t.Error("Has(Home Assistant) = false")
	}
	if data, _, err := k.Read(context.Background(), "Uptime Kuma"); err != nil || string(data) != "<svg>uptime-kuma</svg>" {
		t.Errorf("Read(Uptime Kuma) = %q, %v", data, err)
	}
	k.Retain(context.Background(), []string{"Home Assistant", "Pi_hole"})
	for _, f := range []string{"home-assistant.svg", "home-assistant-dark.svg", "pi-hole.svg"} {
		waitFor(t, f, func() bool { return fileExists(filepath.Join(dir, f)) })
	}
}

// ── redirects ────────────────────────────────────────────────────────────────

func redirectChain(n int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var i int
		_, _ = fmt.Sscanf(r.URL.Path, "/r/%d", &i)
		if i >= n {
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/r/%d", i+1), http.StatusFound)
	})
	return httptest.NewServer(mux)
}

func TestIconClientFollowsAtMostThreeRedirects(t *testing.T) {
	for n, wantErr := range map[int]bool{3: false, 4: true} {
		srv := redirectChain(n)
		resp, err := newIconClient().Get(srv.URL + "/r/0")
		if err == nil {
			_ = resp.Body.Close()
		}
		srv.Close()
		if (err != nil) != wantErr {
			t.Errorf("%d redirects: err = %v, want error = %v", n, err, wantErr)
		}
	}
}

func TestIconClientRefusesHTTPSToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	pool := x509.NewCertPool()
	pool.AddCert(secure.Certificate())
	c := newIconClient()
	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	resp, err := c.Get(secure.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("followed a redirect from https to http")
	}
	// http → https stays allowed
	sec2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer sec2.Close()
	pool.AddCert(sec2.Certificate())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sec2.URL, http.StatusFound) }))
	defer up.Close()
	resp, err = c.Get(up.URL)
	if err != nil {
		t.Fatalf("http → https: %v", err)
	}
	_ = resp.Body.Close()
}

// ── the refresh clock survives a read-only directory ─────────────────────────

func TestNotModifiedWithAReadOnlyDirectoryDoesNotLoop(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	k, m, dir := loadedCatalog(t)
	ageIndex(t, k)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)

	before := m.hitsOf("/index.json")
	if _, err := k.updateIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.hitsOf("/index.json") != before+1 {
		t.Fatalf("expected one conditional request, got %d", m.hitsOf("/index.json")-before)
	}
	if time.Since(k.lastChecked()) > time.Minute {
		t.Error("the check time was not kept although the file could not be written")
	}
	if _, err := k.updateIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.hitsOf("/index.json") != before+1 {
		t.Error("the source was asked again right after a 304")
	}
}

// ── a replaced index stays open for its readers ──────────────────────────────

func TestReplacedIndexIsClosedOnlyAfterItsLastReader(t *testing.T) {
	k, _, _ := loadedCatalog(t)
	x := k.acquire()
	if x == nil {
		t.Fatal("no index")
	}
	k.swapIndex(nil) // an index replaced while a query is on its way
	if _, ok := x.resolve("jellyfin"); !ok {
		t.Fatal("the index was closed under a reader")
	}
	x.release()
	if err := x.db.Ping(); err == nil {
		t.Error("the replaced index was not closed after its last reader left")
	}

	// no reader: closed at once
	k2, _, _ := loadedCatalog(t)
	y := k2.index()
	k2.swapIndex(nil)
	if err := y.db.Ping(); err == nil {
		t.Error("an unused replaced index stayed open")
	}
}
