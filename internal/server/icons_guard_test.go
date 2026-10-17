package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond for up to 5 s — background downloads finish on their own time.
func waitFor(t *testing.T, what string, cond func() bool) {
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

// A Dashboard Icons list scanned from the cache directory is not a full index:
// offline, jellyfin may simply not be cached yet. It must still go to Dashboard
// Icons, not to selfh.st (which also has it), and get no selfh.st variants.
func TestScannedCacheIsNotAuthoritative(t *testing.T) {
	m := &iconMock{files: map[string]string{
		selfhstIdxURL:               selfhstIndexJSON(t),
		homarrSVG + "jellyfin.svg":  "<svg>homarr jellyfin</svg>",
		selfhstSVG + "jellyfin.svg": "<svg>selfhst jellyfin</svg>",
	}}
	k, dir := newCatalogWithMockCDN(t, m.handler)
	// no index.json and every Dashboard Icons index source answers 404 → indexCache
	if err := os.WriteFile(filepath.Join(dir, "other.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	k.Refresh(context.Background())
	if k.homarrAuthoritative {
		t.Fatal("a list scanned from the cache must not count as a full index")
	}

	data, _, err := k.Read(context.Background(), "jellyfin")
	if err != nil || string(data) != "<svg>homarr jellyfin</svg>" {
		t.Fatalf("Read(jellyfin) = %q, %v; want the Dashboard Icons file", data, err)
	}
	if m.asked(selfhstSVG + "jellyfin.svg") {
		t.Error("selfh.st was asked although Dashboard Icons had the file")
	}
	if d, l := k.Variants("jellyfin"); d != "" || l != "" {
		t.Errorf("Variants(jellyfin) = (%q, %q); selfh.st variants must not apply without a full Dashboard Icons index", d, l)
	}
}

func TestUnknownExtensionTriesPNG(t *testing.T) {
	m := &iconMock{files: map[string]string{selfhstPNGPath + "13-feet-ladder.png": "PNG13"}}
	k, _ := newCatalogWithMockCDN(t, m.handler)
	data, kind, err := k.Read(context.Background(), "13-feet-ladder")
	if err != nil || string(data) != "PNG13" || kind != "image/png" {
		t.Fatalf("Read = %q %q %v; want the selfh.st png found after the svg 404", data, kind, err)
	}
}

type failingTransport struct{ calls atomic.Int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, errors.New("connection refused")
}

// A network failure is not a 404: no detour to the next catalog, and the host
// rests for hostCooldown so the next icons answer at once.
func TestNetworkFailureStopsAndCoolsDown(t *testing.T) {
	ft := &failingTransport{}
	k := NewCatalog("", t.TempDir())
	k.client = &http.Client{Transport: ft}

	if _, _, err := k.Read(context.Background(), "llama-cpp"); err == nil {
		t.Fatal("Read should fail with the network down")
	}
	if n := ft.calls.Load(); n != 1 {
		t.Errorf("%d requests, want 1 — no png retry and no selfh.st fallback after a network error", n)
	}
	start := time.Now()
	if _, _, err := k.Read(context.Background(), "jellyfin"); err == nil {
		t.Fatal("Read should fail with the network down")
	}
	if n := ft.calls.Load(); n != 1 {
		t.Errorf("%d requests, want still 1 — the CDN host is on cooldown", n)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Read on a cooled-down host took %v, want instant", d)
	}
}

// A slow CDN does not hold the request: past iconWaitBudget it gets an error
// (the card shows its letter), the download finishes in the background, and
// concurrent requests share one download.
func TestSlowCDNDoesNotBlockAndFinishesInBackground(t *testing.T) {
	var calls atomic.Int32
	k, dir := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(iconWaitBudget + 500*time.Millisecond)
		_, _ = w.Write([]byte("<svg>slow</svg>"))
	})
	k.ext = map[string]string{"slow": "svg"}
	k.homarrAuthoritative = true

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	start := time.Now()
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := k.Read(context.Background(), "slow")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	if d := time.Since(start); d > iconWaitBudget+300*time.Millisecond {
		t.Errorf("Read waited %v, want at most about %v", d, iconWaitBudget)
	}
	for err := range errs {
		if !errors.Is(err, errIconPending) {
			t.Errorf("err = %v, want errIconPending", err)
		}
	}
	waitFor(t, "the background download", func() bool {
		_, err := os.Stat(filepath.Join(dir, "slow.svg"))
		return err == nil
	})
	if n := calls.Load(); n != 1 {
		t.Errorf("%d downloads for 5 concurrent requests, want 1", n)
	}
	if data, _, err := k.Read(context.Background(), "slow"); err != nil || string(data) != "<svg>slow</svg>" {
		t.Errorf("next Read = %q, %v; want the file from disk", data, err)
	}
}

func TestMissingNameIsRemembered(t *testing.T) {
	m := &iconMock{files: map[string]string{}}
	k, _ := newCatalogWithMockCDN(t, m.handler)
	if _, _, err := k.Read(context.Background(), "nope"); !errors.Is(err, errNotFound) {
		t.Fatalf("err = %v, want errNotFound", err)
	}
	asked := m.count()
	if _, _, err := k.Read(context.Background(), "nope"); !errors.Is(err, errNotFound) {
		t.Fatalf("err = %v, want errNotFound", err)
	}
	if m.count() != asked {
		t.Errorf("a remembered miss went to the network again (%d → %d requests)", asked, m.count())
	}
	// a new index may route the name elsewhere — misses are forgotten
	if err := k.setSelfhstFrom(strings.NewReader(selfhstIndexJSON(t))); err != nil {
		t.Fatal(err)
	}
	if k.net.missing("nope") {
		t.Error("misses should be forgotten when an index changes")
	}
}

func TestHasBothCatalogs(t *testing.T) {
	k, dir, _ := twoCatalogs(t)
	if err := os.WriteFile(filepath.Join(dir, "cached-only.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		want bool
	}{
		{"jellyfin", true},               // Dashboard Icons index
		{"llama-cpp", true},              // selfh.st index
		{"selfhst:llama-cpp", true},      // explicit
		{"selfhst:llama-cpp-dark", true}, // explicit variant file
		{"llama-cpp-dark", false},        // variant files are not names
		{"selfhst:jellyfin", true},       // selfh.st has it too
		{"selfhst:cached-only", false},   // root cache is Dashboard Icons, not selfh.st
		{"cached-only", true},            // in the root cache
		{"nothing", false},               //
		{"selfhst:", false},              //
		{"selfhst:../index", false},      //
		{"../x", false},                  //
	} {
		if got := k.Has(c.name); got != c.want {
			t.Errorf("Has(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSelfhstIndexRejectsHostileReferences(t *testing.T) {
	entries := []map[string]string{}
	for _, ref := range []string{"ok", "../etc", "a/b", `a\b`, "UPPER", "", "x?y", "x#y", "a..b", "-lead",
		"sp ace", strings.Repeat("a", 129), "fine.name_1-2"} {
		entries = append(entries, map[string]string{"Reference": ref, "SVG": "Yes", "Light": "Yes", "Dark": "Yes"})
	}
	b, _ := json.Marshal(entries)
	idx, err := parseSelfhstIndex(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for n := range idx.icons {
		got = append(got, n)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"fine.name_1-2", "ok"}) {
		t.Errorf("accepted references = %v, want [fine.name_1-2 ok]", got)
	}
	if _, err := parseSelfhstIndex(strings.NewReader(`{"not":"a list"}`)); err == nil {
		t.Error("an index that is not a list should be rejected")
	}
	if _, err := parseSelfhstIndex(strings.NewReader(`[]`)); err == nil {
		t.Error("an empty index should be rejected")
	}
}

func TestIndexSelfhstCacheListsCachedFiles(t *testing.T) {
	m := &iconMock{files: map[string]string{}} // no index anywhere
	k, dir := newCatalogWithMockCDN(t, m.handler)
	sdir := filepath.Join(dir, selfhstDir)
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"llama-cpp.svg", "13-feet-ladder.png", ".llama-cpp.svg.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(sdir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	k.Refresh(context.Background())

	k.mu.RLock()
	idx := k.selfhst
	k.mu.RUnlock()
	if idx.loaded {
		t.Error("a scan of cached files is not a loaded index")
	}
	if len(idx.icons) != 2 || idx.icons["llama-cpp"].ext() != "svg" || idx.icons["13-feet-ladder"].ext() != "png" {
		t.Errorf("icons = %v, want llama-cpp (svg) and 13-feet-ladder (png)", idx.icons)
	}
	if !slices.Contains(k.Search("llama", 10), "llama-cpp") {
		t.Error("the picker should see the cached selfh.st icon")
	}
}

// Through a real ServeMux, as in production: %3A and %2F are decoded into the
// {name} value, and a slash must still be refused.
func TestIconRouteDecodesNames(t *testing.T) {
	k, _, _ := twoCatalogs(t)
	s := &Server{icons: k}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /icon/{name}", s.handleIcon)

	for _, c := range []struct {
		path string
		code int
	}{
		{"/icon/selfhst%3Allama-cpp-dark", http.StatusOK},
		{"/icon/selfhst:llama-cpp-light", http.StatusOK},
		{"/icon/llama-cpp.svg", http.StatusOK},
		{"/icon/..%2Findex", http.StatusNotFound},
		{"/icon/selfhst%3A..%2Findex", http.StatusNotFound},
		{"/icon/selfhst%3A", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("GET %s = %d, want %d", c.path, rec.Code, c.code)
		}
		if c.code == http.StatusOK && rec.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
			t.Errorf("GET %s: CSP = %q", c.path, rec.Header().Get("Content-Security-Policy"))
		}
	}
}

func TestPrefetchFillsTheCache(t *testing.T) {
	k, dir, _ := twoCatalogs(t)
	k.Prefetch(context.Background(), []string{"llama-cpp", "jellyfin", "nothing-here"})
	for _, f := range []string{"jellyfin.svg", "selfhst/llama-cpp.svg", "selfhst/llama-cpp-dark.svg", "selfhst/llama-cpp-light.svg"} {
		waitFor(t, f, func() bool {
			_, err := os.Stat(filepath.Join(dir, f))
			return err == nil
		})
	}
}

// Only a 404 sends the request on to the next catalog; any other failure of
// the first source ends the walk (and is not remembered as a miss).
func TestOnlyNotFoundFallsThrough(t *testing.T) {
	var selfhstAsked atomic.Bool
	k, _ := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/gh/selfhst/") {
			selfhstAsked.Store(true)
			_, _ = w.Write([]byte("<svg/>"))
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	if _, _, err := k.Read(context.Background(), "llama-cpp"); err == nil {
		t.Fatal("Read should fail when Dashboard Icons answers 403")
	}
	if selfhstAsked.Load() {
		t.Error("selfh.st was asked after a non-404 failure")
	}
	if k.net.missing("llama-cpp") {
		t.Error("a non-404 failure must not be remembered as a missing icon")
	}
}

// metadata.json lists base names only; the variant files it points to
// (dagster-light) are Dashboard Icons files too, even though the name index
// built from metadata does not list them. selfh.st also has dagster with a light
// variant — it must not win.
func TestHomarrVariantFilesStayWithHomarr(t *testing.T) {
	idx, _ := json.Marshal([]map[string]string{{"Reference": "dagster", "SVG": "Yes", "Light": "Yes", "Dark": "Yes"}})
	m := &iconMock{files: map[string]string{
		homarrMeta:                       string(sampleMetadata(t)),
		selfhstIdxURL:                    string(idx),
		homarrSVG + "dagster-light.svg":  "<svg>homarr dagster light</svg>",
		selfhstSVG + "dagster-light.svg": "<svg>selfhst dagster light</svg>",
	}}
	k, dir := newCatalogWithMockCDN(t, m.handler)
	index, _ := json.Marshal(map[string]string{"dagster": "svg", "jellyfin": "svg"})
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	k.Refresh(context.Background())

	data, _, err := k.Read(context.Background(), "dagster-light")
	if err != nil || string(data) != "<svg>homarr dagster light</svg>" {
		t.Fatalf("Read(dagster-light) = %q, %v; want the Dashboard Icons variant", data, err)
	}
}
