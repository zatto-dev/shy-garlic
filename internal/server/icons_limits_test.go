package server

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

// With both indexes loaded, a name neither has is answered from memory: random
// names in /icon/ must not turn into downloads (or goroutines).
func TestUnknownNameWithFullIndexesStaysOffTheNetwork(t *testing.T) {
	k, _, m := twoCatalogs(t)
	before := m.count()
	goroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 2000 {
		if _, _, err := k.Read(ctx, fmt.Sprintf("random-%d", i)); err == nil {
			t.Fatal("a random name should not be found")
		}
	}
	if n := m.count() - before; n != 0 {
		t.Errorf("%d CDN requests for names no index has, want 0", n)
	}
	if d := runtime.NumGoroutine() - goroutines; d > 10 {
		t.Errorf("%d goroutines left behind by 2000 unknown names", d)
	}
}

func creditParagraphs(html string) []string {
	var out []string
	for _, part := range strings.Split(html, `<p class="icon-credit">`)[1:] {
		out = append(out, part[:strings.Index(part, "</p>")])
	}
	return out
}

// Without indexes the panel cannot tell, so it asks — but never more than
// maxIconFlights downloads at once; the rest get "pending" right away.
func TestDownloadsAreCapped(t *testing.T) {
	var now, peak atomic.Int32
	k, _ := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
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
	})
	goroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 2000 {
		_, _, _ = k.Read(ctx, fmt.Sprintf("random-%d", i))
	}
	// each running download is its own goroutine plus the client and server
	// connection goroutines of the in-process mock CDN
	if d := runtime.NumGoroutine() - goroutines; d > 6*maxIconFlights {
		t.Errorf("%d goroutines after 2000 random names, want at most %d", d, 6*maxIconFlights)
	}
	waitFor(t, "the downloads to drain", func() bool {
		k.net.mu.Lock()
		defer k.net.mu.Unlock()
		return len(k.net.flights) == 0
	})
	if p := peak.Load(); p > maxIconFlights {
		t.Errorf("%d concurrent CDN requests, want at most %d", p, maxIconFlights)
	}
}

func TestIconClientLimitsConnectionsPerHost(t *testing.T) {
	tr, ok := newIconClient().Transport.(*http.Transport)
	if !ok || tr.MaxConnsPerHost == 0 || tr.MaxConnsPerHost > 8 {
		t.Errorf("MaxConnsPerHost = %v, want 1..8", tr.MaxConnsPerHost)
	}
}

// Offline without any index: the icon sits in the selfh.st cache, Dashboard
// Icons does not answer. The cached file must be served, not the network error.
func TestOfflineServesLaterSourceCache(t *testing.T) {
	ft := &failingTransport{}
	dir := t.TempDir()
	k := NewCatalog("", dir)
	k.client = &http.Client{Transport: ft}
	if err := os.MkdirAll(filepath.Join(dir, selfhstDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, selfhstDir, "llama-cpp.svg"), []byte("<svg>cached</svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 2 { // the second time the CDN host is already on cooldown
		data, kind, err := k.Read(context.Background(), "llama-cpp")
		if err != nil || string(data) != "<svg>cached</svg>" || kind != "image/svg+xml" {
			t.Fatalf("Read #%d = %q %q %v; want the cached selfh.st file", i+1, data, kind, err)
		}
	}

	// without a full index the routing may still change — browsers keep it briefly
	s := &Server{icons: k}
	req := httptest.NewRequest(http.MethodGet, "/icon/llama-cpp", nil)
	req.SetPathValue("name", "llama-cpp")
	rec := httptest.NewRecorder()
	s.handleIcon(rec, req)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=300") {
		t.Errorf("Cache-Control = %q, want a short max-age without a full index", cc)
	}
}

func TestZipRefusesOversizedIcon(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "icons.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("huge.svg")
	_, _ = w.Write(bytes.Repeat([]byte("x"), maxIconBytes+1))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	k := NewCatalog(zipPath, t.TempDir())
	k.client = &http.Client{Transport: &failingTransport{}}
	if data, _, err := k.Read(context.Background(), "huge"); err == nil {
		t.Fatalf("Read returned %d bytes of an oversized zip entry, want an error", len(data))
	}
}

// In zip mode nothing is fetched by "this server", so the credit says only
// what is true there.
func TestIconCreditInZipMode(t *testing.T) {
	s := testServer(t)
	for _, l := range []i18n.Lang{i18n.EN, i18n.PL} {
		d := fullPageView()
		d.T, d.TJSON, d.SecondLang, d.SecondLangName, d.SecondLangShort = fillLang(l)
		d.IconsZip = true
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, "index.html", d); err != nil {
			t.Fatal(err)
		}
		m := i18n.Map(l)
		// only the credit paragraphs: the page also embeds every text as JSON for app.js
		credits := creditParagraphs(buf.String())
		if len(credits) != 2 {
			t.Fatalf("%s: %d credit paragraphs, want 2", l, len(credits))
		}
		for _, c := range credits {
			if !strings.Contains(c, m["icon_credit_changes_zip"]) || strings.Contains(c, m["icon_credit_changes"]) {
				t.Errorf("%s: zip mode credit = %q, want the zip text only", l, c)
			}
		}

	}
}
