package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// cdnRedirector swaps the host in every request for a local httptest.Server —
// it lets us test fetch()/Read(), which build the URL straight from the constant
// cdnIcons, without going out to the network.
type cdnRedirector struct {
	target *url.URL
}

func (p cdnRedirector) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = p.target.Scheme
	req.URL.Host = p.target.Host
	req.Host = p.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func newCatalogWithMockCDN(t *testing.T, handler http.HandlerFunc) (*Catalog, string) {
	t.Helper()
	mock := httptest.NewServer(handler)
	t.Cleanup(mock.Close)
	targetURL, err := url.Parse(mock.URL)
	if err != nil {
		t.Fatalf("bad mock URL: %v", err)
	}
	dir := t.TempDir()
	k := NewCatalog("", dir)
	k.client = &http.Client{Transport: cdnRedirector{target: targetURL}}
	return k, dir
}

// ── defect no. 1: an empty body from the CDN poisoned the cache forever ──────

func TestFetchRejectsEmptyResponse(t *testing.T) {
	var calls int
	k, dir := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "image/svg+xml")
		w.WriteHeader(http.StatusOK)
		// we deliberately write NOTHING to the body — this is exactly the jsDelivr
		// "200 OK, zero bytes" case that used to permanently poison the on-disk cache.
	})
	k.ext = map[string]string{"pusta-ikona": "svg"}
	k.names = []string{"pusta-ikona"}

	data, _, err := k.Read(context.Background(), "pusta-ikona")
	if err == nil {
		t.Fatalf("Read should have returned an error for an empty CDN response, got data len=%d", len(data))
	}
	if calls != 1 {
		t.Fatalf("expected 1 fetch attempt, got %d", calls)
	}

	// ! the heart of the defect: without the fix this file existed on disk with 0 bytes and
	// every following request served the same emptiness forever.
	if _, err := os.Stat(filepath.Join(dir, "pusta-ikona.svg")); err == nil {
		t.Fatal("an empty CDN response must NOT be written to the on-disk cache")
	}
}

func TestReadDoesNotHealPoisonedCache(t *testing.T) {
	// Recreates the production state: the file already lies in the cache as 0 bytes (e.g. from
	// the time before this fix), but the CDN has valid content today. The catalog should
	// heal itself from this on the nearest request, instead of serving emptiness
	// from disk forever.
	realData := []byte(`<svg>prawdziwa zawartość</svg>`)
	k, dir := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(realData)
	})
	k.ext = map[string]string{"zatruta-ikona": "svg"}
	k.names = []string{"zatruta-ikona"}

	if err := os.WriteFile(filepath.Join(dir, "zatruta-ikona.svg"), nil, 0o644); err != nil {
		t.Fatalf("cannot prepare the poisoned cache file: %v", err)
	}

	data, kind, err := k.Read(context.Background(), "zatruta-ikona")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != string(realData) {
		t.Fatalf("Read returned %q (from a poisoned 0B file?), expected %q", data, realData)
	}
	if kind != "image/svg+xml" {
		t.Errorf("kind = %q, want image/svg+xml", kind)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "zatruta-ikona.svg"))
	if err != nil || len(onDisk) == 0 {
		t.Errorf("the cache file after self-healing should have real content, err=%v len=%d", err, len(onDisk))
	}
}

func TestFetchSavesToCache(t *testing.T) {
	// control: a normal, successful download is still cached (not
	// over-eagerly blocked by the empty-body guard)
	realData := []byte(`<svg>ok</svg>`)
	k, dir := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(realData)
	})
	k.ext = map[string]string{"ok-ikona": "svg"}
	k.names = []string{"ok-ikona"}

	data, _, err := k.Read(context.Background(), "ok-ikona")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != string(realData) {
		t.Fatalf("data = %q, want %q", data, realData)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "ok-ikona.svg"))
	if err != nil || string(onDisk) != string(realData) {
		t.Fatalf("the cache file lacks the fetched content: err=%v data=%q", err, onDisk)
	}
}

// ── defect no. 2: light/dark variants from metadata.json ────────────────────

func sampleMetadata(t *testing.T) []byte {
	t.Helper()
	m := map[string]any{
		"dagster": map[string]any{
			"base":    "svg",
			"aliases": []string{},
			"colors":  map[string]string{"dark": "dagster-dark", "light": "dagster-light"},
		},
		"jellyfin": map[string]any{
			"base":    "svg",
			"aliases": []string{},
			// no "colors" — this icon has no separate variants
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return b
}

// TestRefreshFetchesVariantsAlsoWithCachedIndex recreates the most common
// production case: the process restarts with a warm cache (spis.json
// fresher than a week), so Refresh takes the name list from disk without
// going out to the network — but the light/dark variants have no cache file of
// their own and MUST still be fetched from metadata.json, otherwise after a restart
// the panel goes back to a single, fixed artwork (the same defect #2 all over again).
func TestRefreshFetchesVariantsAlsoWithCachedIndex(t *testing.T) {
	var metadataCalls int
	k, dir := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		metadataCalls++
		w.Write(sampleMetadata(t))
	})

	// spis.json already lies on disk and is fresh — Refresh should use it without the
	// network for the LIST OF NAMES, but that does not excuse it from fetching the variants.
	index, _ := json.Marshal(map[string]string{"dagster": "svg", "jellyfin": "svg"})
	if err := os.WriteFile(filepath.Join(dir, "spis.json"), index, 0o644); err != nil {
		t.Fatalf("preparing spis.json: %v", err)
	}

	k.Refresh(context.Background())

	if k.Count() != 2 {
		t.Fatalf("Count() = %d, want 2 (from the on-disk cache, no network)", k.Count())
	}
	if metadataCalls == 0 {
		t.Fatal("Refresh did NOT try to fetch metadata.json despite a fresh spis.json on disk — variants would stay empty after every restart")
	}
	if dark, light := k.Variants("dagster"); dark != "dagster-dark" || light != "dagster-light" {
		t.Errorf("Variants(dagster) after Refresh() = (%q, %q), want (dagster-dark, dagster-light)", dark, light)
	}
}

func TestMetadataBuildsVariants(t *testing.T) {
	k, _ := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(sampleMetadata(t))
	})

	if _, err := k.fromMetadata(context.Background()); err != nil {
		t.Fatalf("zMetadanych: %v", err)
	}

	if dark, light := k.Variants("dagster"); dark != "dagster-dark" || light != "dagster-light" {
		t.Errorf("Variants(dagster) = (%q, %q), want (dagster-dark, dagster-light)", dark, light)
	}
	if dark, light := k.Variants("jellyfin"); dark != "" || light != "" {
		t.Errorf("Variants(jellyfin) = (%q, %q), want both empty — this icon has no variants", dark, light)
	}
	if dark, light := k.Variants("nieznana"); dark != "" || light != "" {
		t.Errorf("Variants(unknown) = (%q, %q), want both empty", dark, light)
	}
}

func TestResolveIconUsesVariantsWhenPresent(t *testing.T) {
	k, _ := newCatalogWithMockCDN(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(sampleMetadata(t))
	})
	if _, err := k.fromMetadata(context.Background()); err != nil {
		t.Fatalf("zMetadanych: %v", err)
	}
	s := &Server{icons: k}

	ref := s.resolveIcon("dagster", "Dagster")
	if ref.Src != "" {
		t.Errorf("for an icon with variants Src should be empty (we render both variants), got %q", ref.Src)
	}
	if ref.VariantDark != "/icon/dagster-dark" || ref.VariantLight != "/icon/dagster-light" {
		t.Errorf("variants = (%q, %q), want (/icon/dagster-dark, /icon/dagster-light)", ref.VariantDark, ref.VariantLight)
	}

	refWithoutVariants := s.resolveIcon("jellyfin", "Jellyfin")
	if refWithoutVariants.Src != "/icon/jellyfin" {
		t.Errorf("without variants I expect a plain Src, got %q", refWithoutVariants.Src)
	}
	if refWithoutVariants.VariantDark != "" || refWithoutVariants.VariantLight != "" {
		t.Errorf("without variants the WariantCiemny/WariantJasny fields should be empty, got (%q, %q)",
			refWithoutVariants.VariantDark, refWithoutVariants.VariantLight)
	}
}
