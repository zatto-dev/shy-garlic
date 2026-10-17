package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// selfh.st/icons is the second icon catalog. Dashboard Icons stays first: a name
// it has is always served from it, exactly as before. selfh.st only fills the
// names Dashboard Icons lacks (e.g. llama-cpp), and is reachable explicitly as
// `selfhst:<name>` — the only way to get its artwork for a name both have.
//
// The repo layout (checked 2026-10-17, not from memory): svg/<ref>.svg for most
// icons, png/<ref>.png for all of them, and svg/<ref>-dark.svg / -light.svg for
// those with tone variants. index.json at the root lists every icon:
//
//	{"Name": "llama.cpp", "Reference": "llama-cpp", "SVG": "Yes", "PNG": "Yes",
//	 "Light": "Yes", "Dark": "Yes", ...}
//
// "-dark" is the dark-ink file and "-light" the light-ink one, the same meaning
// as Dashboard Icons' colors.dark / colors.light. Icons without an SVG ("SVG":
// "No") keep their variants as PNG.
//
// ! Cache collisions: both catalogs have files under the same names (jellyfin.svg
// exists in each, with different artwork). Dashboard Icons keeps the cache root
// it always had, selfh.st gets its own subdirectory <cache>/selfhst/, so a file
// from one source is never served as the other.
const (
	selfhstCDN      = "https://cdn.jsdelivr.net/gh/selfhst/icons@main"
	selfhstIndexURL = "https://raw.githubusercontent.com/selfhst/icons/main/index.json"
	selfhstPrefix   = "selfhst:"
	selfhstDir      = "selfhst"
)

var selfhstRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// selfhstFlags packs what the panel needs to know about one selfh.st icon.
type selfhstFlags uint8

const (
	selfhstPNG   selfhstFlags = 1 << iota // no SVG, the files are PNG
	selfhstDark                           // <ref>-dark exists
	selfhstLight                          // <ref>-light exists
)

func (f selfhstFlags) ext() string {
	if f&selfhstPNG != 0 {
		return "png"
	}
	return "svg"
}

// selfhstIndex is kept small on purpose — the panel is meant to be light: one
// map of reference → flags (variant file names are derived, not stored) and
// display names only where they say more than the reference.
type selfhstIndex struct {
	loaded  bool                    // from index.json, not just a scan of the cache
	icons   map[string]selfhstFlags // reference → flags; what the picker lists
	display map[string]string       // reference → display name, used as a search alias
}

func (x selfhstIndex) has(ref string) bool {
	_, ok := x.icons[ref]
	return ok
}

// fileExt resolves a file name — an icon or one of its -dark/-light variants.
func (x selfhstIndex) fileExt(name string) (string, bool) {
	if f, ok := x.icons[name]; ok {
		return f.ext(), true
	}
	for _, v := range []struct {
		suffix string
		flag   selfhstFlags
	}{{"-dark", selfhstDark}, {"-light", selfhstLight}} {
		if base, ok := strings.CutSuffix(name, v.suffix); ok {
			if f, ok := x.icons[base]; ok && f&v.flag != 0 {
				return f.ext(), true
			}
		}
	}
	return "", false
}

func (x selfhstIndex) variants(ref string) toneVariant {
	f := x.icons[ref]
	if f&selfhstDark == 0 || f&selfhstLight == 0 {
		return toneVariant{}
	}
	return toneVariant{Dark: selfhstPrefix + ref + "-dark", Light: selfhstPrefix + ref + "-light"}
}

// parseSelfhstIndex decodes index.json entry by entry, keeping only what is
// used; the raw document is never held in memory as a whole.
func parseSelfhstIndex(r io.Reader) (selfhstIndex, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return selfhstIndex{}, fmt.Errorf("selfh.st index is not a list")
	}
	idx := selfhstIndex{loaded: true, icons: make(map[string]selfhstFlags, 3200), display: map[string]string{}}
	for dec.More() {
		var e struct {
			Name      string `json:"Name"`
			Reference string `json:"Reference"`
			SVG       string `json:"SVG"`
			Light     string `json:"Light"`
			Dark      string `json:"Dark"`
		}
		if err := dec.Decode(&e); err != nil {
			return selfhstIndex{}, err
		}
		ref := e.Reference
		// ! the index comes from the network: a reference becomes a file name and a
		// URL path, so only the shape selfh.st actually uses gets through
		if !selfhstRef.MatchString(ref) || badIconName(ref) {
			continue
		}
		var f selfhstFlags
		if e.SVG != "Yes" {
			f |= selfhstPNG
		}
		if e.Dark == "Yes" {
			f |= selfhstDark
		}
		if e.Light == "Yes" {
			f |= selfhstLight
		}
		idx.icons[ref] = f
		if e.Name != "" && !strings.EqualFold(e.Name, ref) {
			idx.display[ref] = e.Name
		}
	}
	if len(idx.icons) == 0 {
		return selfhstIndex{}, fmt.Errorf("empty selfh.st index")
	}
	return idx, nil
}

func (k *Catalog) selfhstCacheDir() string {
	if k.cacheDir == "" {
		return ""
	}
	return filepath.Join(k.cacheDir, selfhstDir)
}

// refreshSelfhst loads the selfh.st index the same way as the Dashboard Icons
// one: fresh copy on disk → network → stale copy on disk → whatever icon files
// are already cached. index.json carries the variants too, so unlike Dashboard
// Icons there is no second request.
func (k *Catalog) refreshSelfhst(ctx context.Context) {
	dir := k.selfhstCacheDir()
	file := filepath.Join(dir, "index.json")
	if dir != "" {
		if fi, err := os.Stat(file); err == nil && time.Since(fi.ModTime()) < indexValidity {
			if k.setSelfhstFromFile(file) == nil {
				slog.Info("selfh.st icon catalog from cache", "icons", k.selfhstCount())
				return
			}
		}
	}

	err := k.downloadSelfhstIndex(ctx, dir, file)
	if err == nil {
		slog.Info("selfh.st icon catalog fetched", "icons", k.selfhstCount())
		return
	}

	slog.Warn("selfh.st icon index unavailable, keeping what is on disk", "err", err)
	if dir == "" {
		return
	}
	if k.setSelfhstFromFile(file) == nil {
		return
	}
	k.indexSelfhstCache(dir)
}

// downloadSelfhstIndex streams index.json to a temporary file in the cache,
// parses it from there and only then puts it in place — memory holds the
// parsed index, never the whole document.
func (k *Catalog) downloadSelfhstIndex(ctx context.Context, dir, file string) error {
	ctx, cancel := context.WithTimeout(ctx, indexBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, selfhstIndexURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", selfhstIndexURL, resp.Status)
	}
	body := io.LimitReader(resp.Body, 8<<20)
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return k.setSelfhstFrom(body)
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return k.setSelfhstFrom(body)
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	_, err = io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := k.setSelfhstFromFile(tmp.Name()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func (k *Catalog) setSelfhstFromFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return k.setSelfhstFrom(f)
}

func (k *Catalog) setSelfhstFrom(r io.Reader) error {
	idx, err := parseSelfhstIndex(r)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.selfhst = idx
	k.rebuildNamesLocked()
	k.net.forgetMisses()
	return nil
}

// indexSelfhstCache is the selfh.st twin of indexCache: no index at all, so list
// the files we once downloaded. Without an index the variants are unknown.
func (k *Catalog) indexSelfhstCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	idx := selfhstIndex{icons: map[string]selfhstFlags{}}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		if b, ok := strings.CutSuffix(n, ".svg"); ok {
			idx.icons[b] = 0
		} else if b, ok := strings.CutSuffix(n, ".png"); ok {
			if _, is := idx.icons[b]; !is {
				idx.icons[b] = selfhstPNG
			}
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.selfhst = idx
	k.rebuildNamesLocked()
	k.net.forgetMisses()
}

func (k *Catalog) selfhstCount() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.selfhst.icons)
}
