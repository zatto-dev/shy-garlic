package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

type selfhstIndex struct {
	loaded   bool                   // names come from index.json, not just a scan of the cache
	names    map[string]string      // reference → "svg" | "png"; what the picker lists
	files    map[string]string      // every file, variants included → "svg" | "png"
	variants map[string]toneVariant // reference → selfhst:<ref>-dark / selfhst:<ref>-light
	aliases  map[string][]string    // reference → display name, when it says more than the reference
}

func parseSelfhstIndex(data []byte) (selfhstIndex, error) {
	var entries []struct {
		Name      string `json:"Name"`
		Reference string `json:"Reference"`
		SVG       string `json:"SVG"`
		Light     string `json:"Light"`
		Dark      string `json:"Dark"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return selfhstIndex{}, err
	}
	idx := selfhstIndex{
		loaded:   true,
		names:    make(map[string]string, len(entries)),
		files:    make(map[string]string, 3*len(entries)),
		variants: make(map[string]toneVariant, len(entries)),
		aliases:  make(map[string][]string, len(entries)),
	}
	for _, e := range entries {
		ref := e.Reference
		if ref == "" || badIconName(ref) {
			continue
		}
		ext := "svg"
		if e.SVG != "Yes" {
			ext = "png"
		}
		idx.names[ref] = ext
		idx.files[ref] = ext
		if e.Dark == "Yes" {
			idx.files[ref+"-dark"] = ext
		}
		if e.Light == "Yes" {
			idx.files[ref+"-light"] = ext
		}
		if e.Dark == "Yes" && e.Light == "Yes" {
			idx.variants[ref] = toneVariant{Dark: selfhstPrefix + ref + "-dark", Light: selfhstPrefix + ref + "-light"}
		}
		if e.Name != "" && !strings.EqualFold(e.Name, ref) {
			idx.aliases[ref] = []string{e.Name}
		}
	}
	if len(idx.names) == 0 {
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
			if data, err := os.ReadFile(file); err == nil && k.setSelfhstFrom(data) == nil {
				slog.Info("selfh.st icon catalog from cache", "icons", k.selfhstCount())
				return
			}
		}
	}

	data, err := k.fetchJSON(ctx, selfhstIndexURL, 8<<20)
	if err == nil {
		err = k.setSelfhstFrom(data)
	}
	if err == nil {
		if dir != "" && os.MkdirAll(dir, 0o755) == nil {
			tmp := filepath.Join(dir, ".index.json.tmp")
			if os.WriteFile(tmp, data, 0o644) == nil {
				_ = os.Rename(tmp, file)
			}
		}
		slog.Info("selfh.st icon catalog fetched", "icons", k.selfhstCount())
		return
	}

	slog.Warn("selfh.st icon index unavailable, keeping what is on disk", "err", err)
	if dir == "" {
		return
	}
	if data, err := os.ReadFile(file); err == nil && k.setSelfhstFrom(data) == nil {
		return
	}
	k.indexSelfhstCache(dir)
}

func (k *Catalog) setSelfhstFrom(data []byte) error {
	idx, err := parseSelfhstIndex(data)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.selfhst = idx
	k.rebuildNamesLocked()
	return nil
}

// indexSelfhstCache is the selfh.st twin of indexCache: no index at all, so list
// the files we once downloaded. Without an index the variants are unknown.
func (k *Catalog) indexSelfhstCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	idx := selfhstIndex{names: map[string]string{}, files: map[string]string{}}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		for _, ext := range []string{"svg", "png"} {
			if b, ok := strings.CutSuffix(n, "."+ext); ok {
				if _, is := idx.names[b]; !is {
					idx.names[b] = ext
					idx.files[b] = ext
				}
			}
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.selfhst = idx
	k.rebuildNamesLocked()
}

func (k *Catalog) selfhstCount() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.selfhst.names)
}

// readSelfhst serves a selfh.st file: its own cache subdirectory, then the CDN.
func (k *Catalog) readSelfhst(ctx context.Context, ref string) ([]byte, string, error) {
	k.mu.RLock()
	ext, known := k.selfhst.files[ref]
	k.mu.RUnlock()
	if !known {
		ext = "svg"
	}
	dir := k.selfhstCacheDir()
	if data, kind, ok := readCached(dir, ref, ext); ok {
		return data, kind, nil
	}
	return k.fetchFrom(ctx, selfhstCDN, dir, ref, ext)
}
