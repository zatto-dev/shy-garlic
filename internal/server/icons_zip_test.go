package server

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

func creditParagraphs(html string) []string {
	var out []string
	for _, part := range strings.Split(html, `<p class="icon-credit">`)[1:] {
		out = append(out, part[:strings.Index(part, "</p>")])
	}
	return out
}

func writeZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "icons.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return zipPath
}

func TestZipRefusesOversizedIcon(t *testing.T) {
	zipPath := writeZip(t, map[string][]byte{"huge.svg": bytes.Repeat([]byte("x"), maxIconBytes+1)})
	k := NewCatalogAt("", zipPath, t.TempDir())
	k.client = &http.Client{Transport: &failingTransport{}}
	if data, _, err := k.Read(context.Background(), "huge"); err == nil {
		t.Fatalf("Read returned %d bytes of an oversized zip entry, want an error", len(data))
	}
}

// Zip mode is for networks without internet: no index is requested, the picker
// searches the zip, an icon in the zip never touches the network, and a name the zip
// lacks is still looked up at the source when asked for (never ahead of time).
func TestZipModeMakesNoIndexRequests(t *testing.T) {
	m := newIconServer(t, fixtureIcons())
	zipPath := writeZip(t, map[string][]byte{"jellyfin.svg": []byte("<svg>zip jellyfin</svg>"), "readme.txt": []byte("x")})
	dir := t.TempDir()
	k := NewCatalogAt(m.srv.URL, zipPath, dir)
	if k.Mode() != "zip" {
		t.Fatalf("mode = %q", k.Mode())
	}
	k.Refresh(context.Background())
	k.Retain(context.Background(), []string{"jellyfin", "plex"})
	if got := m.totalHits(); got != 0 {
		t.Errorf("zip mode made %d requests at start, want 0", got)
	}
	if k.Count() != 1 || !slices.Equal(k.Search("jelly", 5), []string{"jellyfin"}) {
		t.Errorf("zip Count = %d, Search = %v", k.Count(), k.Search("jelly", 5))
	}
	if data, _, err := k.Read(context.Background(), "jellyfin"); err != nil || string(data) != "<svg>zip jellyfin</svg>" {
		t.Errorf("Read(jellyfin) = %q %v", data, err)
	}
	if m.totalHits() != 0 {
		t.Error("an icon in the zip touched the network")
	}
	if data, _, err := k.Read(context.Background(), "plex"); err != nil || string(data) != "<svg>plex</svg>" {
		t.Errorf("Read(plex), a name the zip lacks = %q %v", data, err)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("zip mode wrote %v to the data directory", names)
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
			if strings.Contains(c, "/attribution") {
				t.Errorf("%s: zip mode links to an attribution page that is not its own: %q", l, c)
			}
		}
	}
}
