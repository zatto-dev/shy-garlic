package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// Paths as the two catalogs are reached through cdnRedirector: the host is
// swapped for the mock, the path stays exactly what the real URL carries.
const (
	homarrSVG     = "/gh/homarr-labs/dashboard-icons@main/svg/"
	homarrMeta    = "/homarr-labs/dashboard-icons/main/metadata.json"
	selfhstSVG    = "/gh/selfhst/icons@main/svg/"
	selfhstPNG    = "/gh/selfhst/icons@main/png/"
	selfhstIdxURL = "/selfhst/icons/main/index.json"
)

// iconMock is a fake CDN for both catalogs: path → body, everything else 404.
// It records which paths were asked for, so a test can prove that a source was
// NOT contacted.
type iconMock struct {
	mu    sync.Mutex
	files map[string]string
	hits  []string
}

func (m *iconMock) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.hits = append(m.hits, r.URL.Path)
	body, ok := m.files[r.URL.Path]
	m.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(body))
}

func (m *iconMock) asked(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.hits, path)
}

func (m *iconMock) set(files map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files = files
}

// selfhstIndexJSON mirrors the shape of selfhst/icons index.json (checked
// against the real file on 2026-10-17): a list of objects, "Yes"/"No" flags.
func selfhstIndexJSON(t *testing.T) string {
	t.Helper()
	entry := func(name, ref, svg, light, dark string) map[string]string {
		return map[string]string{"Name": name, "Reference": ref, "SVG": svg, "PNG": "Yes",
			"WebP": "Yes", "Light": light, "Dark": dark, "Category": "Other", "Tags": ""}
	}
	b, err := json.Marshal([]map[string]string{
		entry("llama.cpp", "llama-cpp", "Yes", "Yes", "Yes"),
		entry("Jellyfin", "jellyfin", "Yes", "Yes", "Yes"),
		entry("13 Feet Ladder", "13-feet-ladder", "No", "No", "No"),
		entry("Half Pair", "half-pair", "Yes", "Yes", "No"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// twoCatalogs builds a catalog whose Dashboard Icons index (fresh on disk)
// knows only "jellyfin", while the selfh.st index (from the mock) also knows
// llama-cpp — the situation from SHYG-57.
func twoCatalogs(t *testing.T) (*Catalog, string, *iconMock) {
	t.Helper()
	m := &iconMock{files: map[string]string{
		homarrMeta:                         `{"jellyfin":{"base":"svg","aliases":[]}}`,
		selfhstIdxURL:                      selfhstIndexJSON(t),
		homarrSVG + "jellyfin.svg":         "<svg>homarr jellyfin</svg>",
		selfhstSVG + "jellyfin.svg":        "<svg>selfhst jellyfin</svg>",
		selfhstSVG + "llama-cpp.svg":       "<svg>selfhst llama</svg>",
		selfhstSVG + "llama-cpp-dark.svg":  "<svg>selfhst llama dark ink</svg>",
		selfhstSVG + "llama-cpp-light.svg": "<svg>selfhst llama light ink</svg>",
		selfhstPNG + "13-feet-ladder.png":  "PNG13",
		selfhstSVG + "half-pair.svg":       "<svg>half</svg>",
		selfhstSVG + "half-pair-light.svg": "<svg>half light</svg>",
	}}
	k, dir := newCatalogWithMockCDN(t, m.handler)
	index, _ := json.Marshal(map[string]string{"jellyfin": "svg"})
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	k.Refresh(context.Background())
	return k, dir, m
}

func TestSelfhstOnlyNameIsServed(t *testing.T) {
	k, dir, m := twoCatalogs(t)

	data, kind, err := k.Read(context.Background(), "llama-cpp")
	if err != nil {
		t.Fatalf("Read(llama-cpp): %v", err)
	}
	if string(data) != "<svg>selfhst llama</svg>" || kind != "image/svg+xml" {
		t.Fatalf("Read(llama-cpp) = %q %q, want the selfh.st svg", data, kind)
	}
	if m.asked(homarrSVG + "llama-cpp.svg") {
		t.Error("the index says Dashboard Icons has no llama-cpp, yet its CDN was asked")
	}
	if _, err := os.Stat(filepath.Join(dir, "selfhst", "llama-cpp.svg")); err != nil {
		t.Errorf("a selfh.st icon must be cached under selfhst/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "llama-cpp.svg")); err == nil {
		t.Error("a selfh.st icon must NOT land in the Dashboard Icons cache (root)")
	}
}

func TestNameInBothCatalogsComesFromHomarr(t *testing.T) {
	k, dir, m := twoCatalogs(t)

	data, _, err := k.Read(context.Background(), "jellyfin")
	if err != nil || string(data) != "<svg>homarr jellyfin</svg>" {
		t.Fatalf("Read(jellyfin) = %q, %v; want the Dashboard Icons file", data, err)
	}
	if m.asked(selfhstSVG + "jellyfin.svg") {
		t.Error("selfh.st was asked for a name Dashboard Icons has")
	}
	// the selfh.st artwork of the same name stays reachable explicitly…
	data, _, err = k.Read(context.Background(), "selfhst:jellyfin")
	if err != nil || string(data) != "<svg>selfhst jellyfin</svg>" {
		t.Fatalf("Read(selfhst:jellyfin) = %q, %v; want the selfh.st file", data, err)
	}
	// …and the two files of one name do not overwrite each other in the cache:
	// with the network gone, each source still serves its own artwork.
	m.set(map[string]string{})
	if data, _, _ := k.Read(context.Background(), "jellyfin"); string(data) != "<svg>homarr jellyfin</svg>" {
		t.Errorf("cached jellyfin = %q, want the Dashboard Icons file", data)
	}
	if data, _, _ := k.Read(context.Background(), "selfhst:jellyfin"); string(data) != "<svg>selfhst jellyfin</svg>" {
		t.Errorf("cached selfhst:jellyfin = %q, want the selfh.st file", data)
	}
	for _, f := range []string{filepath.Join(dir, "jellyfin.svg"), filepath.Join(dir, "selfhst", "jellyfin.svg")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("missing cache file %s: %v", f, err)
		}
	}
}

func TestSelfhstVariants(t *testing.T) {
	k, _, _ := twoCatalogs(t)

	if d, l := k.Variants("llama-cpp"); d != "selfhst:llama-cpp-dark" || l != "selfhst:llama-cpp-light" {
		t.Errorf("Variants(llama-cpp) = (%q, %q)", d, l)
	}
	if d, l := k.Variants("selfhst:llama-cpp"); d != "selfhst:llama-cpp-dark" || l != "selfhst:llama-cpp-light" {
		t.Errorf("Variants(selfhst:llama-cpp) = (%q, %q)", d, l)
	}
	// Dashboard Icons wins the name and has no pair there — selfh.st's pair must not leak in
	if d, l := k.Variants("jellyfin"); d != "" || l != "" {
		t.Errorf("Variants(jellyfin) = (%q, %q), want none (the Dashboard Icons artwork has no pair)", d, l)
	}
	// only one side of the pair exists → no pair
	if d, l := k.Variants("half-pair"); d != "" || l != "" {
		t.Errorf("Variants(half-pair) = (%q, %q), want none", d, l)
	}

	data, _, err := k.Read(context.Background(), "selfhst:llama-cpp-light")
	if err != nil || string(data) != "<svg>selfhst llama light ink</svg>" {
		t.Fatalf("Read(selfhst:llama-cpp-light) = %q, %v", data, err)
	}

	s := &Server{icons: k}
	ref := s.resolveIcon("llama-cpp", "llama.cpp")
	if ref.VariantDark != "/icon/selfhst:llama-cpp-dark" || ref.VariantLight != "/icon/selfhst:llama-cpp-light" {
		t.Errorf("resolveIcon variants = (%q, %q)", ref.VariantDark, ref.VariantLight)
	}
}

func TestSelfhstPNGOnlyIcon(t *testing.T) {
	k, _, _ := twoCatalogs(t)
	data, kind, err := k.Read(context.Background(), "13-feet-ladder")
	if err != nil || string(data) != "PNG13" || kind != "image/png" {
		t.Fatalf("Read(13-feet-ladder) = %q %q %v, want the selfh.st png", data, kind, err)
	}
}

func TestSearchSeesBothCatalogs(t *testing.T) {
	k, _, _ := twoCatalogs(t)

	// jellyfin is in both, so it counts once
	if got := k.Count(); got != 4 {
		t.Errorf("Count() = %d, want 4 (jellyfin, llama-cpp, 13-feet-ladder, half-pair)", got)
	}
	if got := k.Search("llama", 10); !slices.Contains(got, "llama-cpp") {
		t.Errorf("Search(llama) = %v, want llama-cpp", got)
	}
	// selfh.st's display name works like an alias
	if got := k.Search("llama.cpp", 10); !slices.Equal(got, []string{"llama-cpp"}) {
		t.Errorf("Search(llama.cpp) = %v, want [llama-cpp]", got)
	}
	if got := k.Search("", 100); slices.Contains(got, "llama-cpp-dark") {
		t.Errorf("variant files must not show up as separate icons: %v", got)
	}
	if !k.Has("llama-cpp") {
		t.Error("Has(llama-cpp) = false")
	}
}

func TestSelfhstIndexSurvivesOffline(t *testing.T) {
	_, dir, _ := twoCatalogs(t)

	// a second process on the same cache, without any network
	m := &iconMock{files: map[string]string{}}
	k, _ := newCatalogWithMockCDN(t, m.handler)
	k.cacheDir = dir
	k.Refresh(context.Background())

	if !slices.Contains(k.Search("llama", 10), "llama-cpp") {
		t.Error("the selfh.st index cached on disk was not used offline")
	}
	if d, _ := k.Variants("llama-cpp"); d == "" {
		t.Error("offline the selfh.st variants should come from the cached index")
	}
}

func TestUnknownNameFallsBackToSelfhstWithoutIndex(t *testing.T) {
	// no index of any kind (first start, network flaky): try Dashboard Icons,
	// then selfh.st
	m := &iconMock{files: map[string]string{selfhstSVG + "llama-cpp.svg": "<svg>selfhst llama</svg>"}}
	k, _ := newCatalogWithMockCDN(t, m.handler)

	data, kind, err := k.Read(context.Background(), "llama-cpp")
	if err != nil || string(data) != "<svg>selfhst llama</svg>" || kind != "image/svg+xml" {
		t.Fatalf("Read(llama-cpp) = %q %q %v, want the selfh.st fallback", data, kind, err)
	}
	if !m.asked(homarrSVG + "llama-cpp.svg") {
		t.Error("without an index Dashboard Icons must be tried first")
	}
}

func TestZipModeWithSelfhst(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "icons.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("jellyfin.svg")
	_, _ = w.Write([]byte("<svg>zip jellyfin</svg>"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m := &iconMock{files: map[string]string{selfhstSVG + "llama-cpp.svg": "<svg>selfhst llama</svg>",
		selfhstIdxURL: selfhstIndexJSON(t)}}
	k, _ := newCatalogWithMockCDN(t, m.handler)
	k2 := NewCatalog(zipPath, t.TempDir())
	k2.client = k.client

	k2.Refresh(context.Background())
	if k2.Mode() != "zip" || k2.Count() != 1 {
		t.Fatalf("zip mode: mode=%q count=%d, want zip/1", k2.Mode(), k2.Count())
	}
	if m.asked(selfhstIdxURL) {
		t.Error("zip mode must not fetch any index")
	}
	if data, _, err := k2.Read(context.Background(), "jellyfin"); err != nil || string(data) != "<svg>zip jellyfin</svg>" {
		t.Errorf("Read(jellyfin) from zip = %q, %v", data, err)
	}
	if d, l := k2.Variants("llama-cpp"); d != "" || l != "" {
		t.Errorf("zip mode Variants = (%q, %q), want none", d, l)
	}
	if data, _, err := k2.Read(context.Background(), "llama-cpp"); err != nil || string(data) != "<svg>selfhst llama</svg>" {
		t.Errorf("Read(llama-cpp) in zip mode = %q, %v; want the selfh.st fallback", data, err)
	}
}

func TestSelfhstBadNames(t *testing.T) {
	k, _, _ := twoCatalogs(t)
	for _, n := range []string{"selfhst:", "selfhst:../index", "selfhst:a/b", `selfhst:a\b`} {
		if _, _, err := k.Read(context.Background(), n); err == nil {
			t.Errorf("Read(%q) should fail", n)
		}
	}
}

func TestIconEndpointServesSelfhstSVG(t *testing.T) {
	k, _, _ := twoCatalogs(t)
	s := &Server{icons: k}
	for _, n := range []string{"llama-cpp", "llama-cpp.svg", "selfhst:llama-cpp-dark"} {
		req := httptest.NewRequest(http.MethodGet, "/icon/"+n, nil)
		req.SetPathValue("name", n)
		rec := httptest.NewRecorder()
		s.handleIcon(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
			t.Errorf("GET /icon/%s = %d %q, want 200 image/svg+xml", n, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
