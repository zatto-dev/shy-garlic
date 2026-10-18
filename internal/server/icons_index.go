package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // the icon index is a SQLite file, same driver as the store
)

// The icon catalog lives on disk, not in the heap.
//
// ! icons.zatto.dev publishes one index.json (about 1 MB, 5 000+ icons). Parsed into
// maps it would sit in memory for the whole life of the process, to answer
// a handful of picker searches a day. Instead the index is imported once, token by
// token, into a SQLite file next to the icon cache and queried from there: the
// resident cost is a small page cache of one connection that is closed again when
// the panel is idle.
//
// The file is immutable once written. A refresh builds a NEW file beside it
// (tmp → fsync → rename), so a crash or a bad download never leaves a half-written
// index, and readers keep their open handle on the old one until they are swapped.
//
// Search is `instr()` on three lowercase columns with bound parameters. That is
// exactly the old "starts with / contains" behavior (nothing else was ever
// offered), needs no FTS index (a few hundred KB less on disk and none in
// memory), and has no query syntax to inject into: the phrase is data.

const (
	DefaultIconsURL  = "https://icons.zatto.dev"
	indexFileName    = "icons-index.db"
	indexMetaName    = "icons-index.meta.json"
	indexSchema      = 1
	indexRefreshEach = 24 * time.Hour
	indexRetryEach   = 15 * time.Minute
	maxIndexBytes    = 16 << 20 // the real one is ~1 MB; the limit only stops a runaway
	maxIndexIcons    = 100000
	maxQueryRunes    = 64
	maxAliasesPerRow = 32
)

// NormalizeIconsURL checks the icon source given by the operator (--icons-url) and
// returns it without a trailing slash. It is a base for <base>/index.json and
// <base>/svg/<name>.svg, so a query, a fragment or credentials make no sense in it.
// Private addresses are fine here: pointing at a mirror on the LAN is the use case.
func NormalizeIconsURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultIconsURL, nil
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("%q is not an http(s) URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "?") || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%q must not carry credentials, a query or a fragment", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// iconName is the only shape of icon name the panel accepts anywhere: it becomes a
// file name in the cache and a path in the upstream URL.
var iconName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// legacySelfhstPrefix: configs written for 0.15 could say `icon: selfhst:<name>`.
// Names are one flat namespace now (the catalog already resolved duplicates), so the
// prefix is just dropped.
const legacySelfhstPrefix = "selfhst:"

// canonicalName lowercases a user-given icon name, strips the extension and the
// legacy prefix, and enforces the allow-list.
func canonicalName(name string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, legacySelfhstPrefix)
	n = strings.TrimSuffix(strings.TrimSuffix(n, ".svg"), ".png")
	if !iconName.MatchString(n) || badIconName(n) {
		return "", false
	}
	return n, true
}

// catalogName is canonicalName for a name that comes from a config: configs of the
// earlier versions say `Home Assistant` or `Home_Assistant`, which the allow-list
// rejects as written. Such a name is first brought to the catalog's own form
// (aliasForm), and only the result has to pass the allow-list. A name that could
// leave the cache directory is refused before any rewriting.
func catalogName(name string) (string, bool) {
	if n, ok := canonicalName(name); ok {
		return n, true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, legacySelfhstPrefix)
	n = strings.TrimSuffix(strings.TrimSuffix(n, ".svg"), ".png")
	if badIconName(n) {
		return "", false
	}
	n = aliasForm(n)
	if !iconName.MatchString(n) {
		return "", false
	}
	return n, true
}

// aliasForm is the normalization icons.zatto.dev applies to aliases and slugs:
// lowercase, every run of other characters becomes one dash.
func aliasForm(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	return b.String()
}

// iconFile is where one icon file lives upstream: <base>/<Ext>/<File>.<Ext>.
type iconFile struct {
	File string // file name without extension (the slug, or slug-dark / slug-light)
	Ext  string // "svg" | "png"
}

// iconIndex is an open, read-only index file.
type iconIndex struct {
	db    *sql.DB
	count int

	mu      sync.Mutex
	refs    int  // queries in progress (see Catalog.acquire)
	retired bool // replaced by a newer index: closed as soon as the last reader is done
	closed  bool
}

func sqliteURI(path string, query string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return "file:" + r.Replace(path) + "?" + query
}

// openIconIndex opens and validates the index file. Any problem (missing, wrong
// schema, damaged) is an error: the caller discards the file and downloads again.
func openIconIndex(path string) (*iconIndex, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	q := neturl.Values{}
	q.Add("mode", "ro")
	q.Add("immutable", "1")
	q.Add("_pragma", "cache_size(-256)") // 256 KiB of page cache per connection
	q.Add("_pragma", "mmap_size(0)")
	q.Add("_pragma", "temp_store(FILE)")
	db, err := sql.Open("sqlite", sqliteURI(path, q.Encode()))
	if err != nil {
		return nil, err
	}
	// two connections are plenty for lookups of microseconds; an idle one is closed
	// quickly, which hands its page cache back
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(30 * time.Second)

	x := &iconIndex{db: db}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != indexSchema {
		_ = db.Close()
		return nil, fmt.Errorf("icon index: bad schema version %d (%v)", version, err)
	}
	var check string
	if err := db.QueryRow(`PRAGMA quick_check(1)`).Scan(&check); err != nil || check != "ok" {
		_ = db.Close()
		return nil, fmt.Errorf("icon index: damaged (%q, %v)", check, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM icons`).Scan(&x.count); err != nil || x.count == 0 {
		_ = db.Close()
		return nil, fmt.Errorf("icon index: empty (%v)", err)
	}
	return x, nil
}

func (x *iconIndex) close() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.closeLocked()
}

func (x *iconIndex) closeLocked() {
	if !x.closed {
		x.closed = true
		_ = x.db.Close()
	}
}

// release ends the use started by Catalog.acquire; safe on nil. A retired index is
// closed here when the last reader leaves.
func (x *iconIndex) release() {
	if x == nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.refs--
	if x.retired && x.refs <= 0 {
		x.closeLocked()
	}
}

// retire marks the index as replaced; it closes now if nobody is reading it.
func (x *iconIndex) retire() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.retired = true
	if x.refs <= 0 {
		x.closeLocked()
	}
}

// lookup finds the file for one exact slug or one of its variants.
func (x *iconIndex) lookup(name string) (iconFile, bool) {
	var fmtc string
	if err := x.db.QueryRow(`SELECT fmt FROM icons WHERE slug = ?`, name).Scan(&fmtc); err == nil {
		return iconFile{File: name, Ext: fmtc}, true
	}
	for _, v := range []struct{ suffix, col string }{{"-dark", "vd"}, {"-light", "vl"}} {
		base, ok := strings.CutSuffix(name, v.suffix)
		if !ok || base == "" {
			continue
		}
		var ext string
		// col comes from the literal table above, never from input
		if err := x.db.QueryRow(`SELECT `+v.col+` FROM icons WHERE slug = ?`, base).Scan(&ext); err == nil && ext != "" {
			return iconFile{File: name, Ext: ext}, true
		}
	}
	return iconFile{}, false
}

// resolve maps a name from a config (or an old config) to the file that serves it:
// the slug itself, a -dark/-light variant of one, an alias from the index's
// aliasMap, or the same after the catalog's own normalization (Home_Assistant →
// home-assistant). The slugs of the two catalogs this one replaced are a contract
// with existing configs, which is what the alias map and the normalization keep.
func (x *iconIndex) resolve(name string) (iconFile, bool) {
	if f, ok := x.lookup(name); ok {
		return f, true
	}
	for _, n := range []string{name, aliasForm(name)} {
		if n == "" {
			continue
		}
		if n != name {
			if f, ok := x.lookup(n); ok {
				return f, true
			}
		}
		var slug, ext string
		err := x.db.QueryRow(`SELECT i.slug, i.fmt FROM alias a JOIN icons i ON i.slug = a.slug WHERE a.a = ?`, n).Scan(&slug, &ext)
		if err == nil {
			return iconFile{File: slug, Ext: ext}, true
		}
	}
	return iconFile{}, false
}

// variants returns the dark-ink and light-ink files of an icon; both or nothing, as
// before (a half pair cannot serve both themes).
func (x *iconIndex) variants(name string) (files [2]iconFile, ok bool) {
	// a variant file is not an icon with variants of its own
	if strings.HasSuffix(name, "-dark") || strings.HasSuffix(name, "-light") {
		if _, isSlug := x.lookupSlug(name); !isSlug {
			return files, false
		}
	}
	f, found := x.resolve(name)
	if !found {
		return files, false
	}
	var vd, vl string
	if err := x.db.QueryRow(`SELECT vd, vl FROM icons WHERE slug = ?`, f.File).Scan(&vd, &vl); err != nil || vd == "" || vl == "" {
		return files, false
	}
	return [2]iconFile{{File: f.File + "-dark", Ext: vd}, {File: f.File + "-light", Ext: vl}}, true
}

func (x *iconIndex) lookupSlug(name string) (string, bool) {
	var ext string
	err := x.db.QueryRow(`SELECT fmt FROM icons WHERE slug = ?`, name).Scan(&ext)
	return ext, err == nil
}

// search returns slugs whose name starts with, or contains, the phrase (or whose
// display name or an alias contains it), prefix matches first, then alphabetical.
func (x *iconIndex) search(q string, limit int) ([]string, error) {
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = x.db.Query(`SELECT slug FROM icons ORDER BY slug LIMIT ?`, limit)
	} else {
		rows, err = x.db.Query(`SELECT slug FROM icons
			WHERE instr(slug, ?1) > 0 OR instr(lname, ?1) > 0 OR instr(lalias, ?1) > 0
			ORDER BY instr(slug, ?1) = 1 DESC, slug LIMIT ?2`, q, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, min(limit, 64))
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// cleanQuery bounds and flattens a search phrase: lowercase, whitespace runs become
// one space (the alias column joins entries with newlines, so a newline in the phrase
// must not match across two aliases), at most maxQueryRunes runes.
func cleanQuery(q string) string {
	q = strings.Join(strings.Fields(strings.ToLower(q)), " ")
	if r := []rune(q); len(r) > maxQueryRunes {
		q = string(r[:maxQueryRunes])
	}
	return q
}

// ── import ───────────────────────────────────────────────────────────────────

// indexEntry is the part of an icons.zatto.dev entry the panel keeps.
type indexEntry struct {
	Name     string            `json:"name"`
	Aliases  []string          `json:"aliases"`
	Format   string            `json:"format"`
	Variants map[string]string `json:"variants"`
}

func tokenDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("icon index: expected %q, got %v", want, tok)
	}
	return nil
}

// openObject consumes the "{" of an object value; a null stands for an empty one.
func openObject(dec *json.Decoder) (open bool, err error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if tok == nil {
		return false, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return false, fmt.Errorf("icon index: expected an object, got %v", tok)
	}
	return true, nil
}

func tokenString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("icon index: expected a string, got %v", tok)
	}
	return s, nil
}

func validExt(e string) bool { return e == "svg" || e == "png" }

// importIndex streams the JSON document into a new SQLite file at tmpPath. The
// document is never held whole: the decoder yields one key at a time and one entry
// at a time.
func importIndex(ctx context.Context, r io.Reader, tmpPath string) (n int, err error) {
	q := neturl.Values{}
	// throw-away file: it only matters once complete, and the rename below is the commit
	q.Add("_pragma", "journal_mode(OFF)")
	q.Add("_pragma", "synchronous(OFF)")
	q.Add("_pragma", "cache_size(-64)")
	q.Add("_pragma", "locking_mode(EXCLUSIVE)")
	db, err := sql.Open("sqlite", sqliteURI(tmpPath, q.Encode()))
	if err != nil {
		return 0, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE icons (slug TEXT PRIMARY KEY, fmt TEXT NOT NULL, vd TEXT NOT NULL, vl TEXT NOT NULL,
			lname TEXT NOT NULL, lalias TEXT NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE alias (a TEXT PRIMARY KEY, slug TEXT NOT NULL) WITHOUT ROWID`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return 0, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	insIcon, err := tx.Prepare(`INSERT OR REPLACE INTO icons VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	insAlias, err := tx.Prepare(`INSERT OR IGNORE INTO alias VALUES (?, ?)`)
	if err != nil {
		return 0, err
	}

	dec := json.NewDecoder(r)
	if err = tokenDelim(dec, '{'); err != nil {
		return 0, err
	}
	sawSchema := false
	for dec.More() {
		var key string
		if key, err = tokenString(dec); err != nil {
			return 0, err
		}
		switch key {
		case "schema":
			var v int
			if err = dec.Decode(&v); err != nil {
				return 0, err
			}
			if v != indexSchema {
				return 0, fmt.Errorf("icon index: unsupported schema %d", v)
			}
			sawSchema = true
		case "icons":
			var open bool
			if open, err = openObject(dec); err != nil {
				return 0, err
			}
			for open && dec.More() {
				if n%256 == 0 && ctx.Err() != nil {
					return 0, ctx.Err()
				}
				var slug string
				if slug, err = tokenString(dec); err != nil {
					return 0, err
				}
				var e indexEntry
				if err = dec.Decode(&e); err != nil {
					return 0, err
				}
				// ! the document comes from the network: a slug becomes a file name
				// and a URL path, so only the allow-listed shape gets in
				if !iconName.MatchString(slug) || badIconName(slug) || !validExt(e.Format) {
					continue
				}
				if n++; n > maxIndexIcons {
					return 0, errors.New("icon index: too many icons")
				}
				vd, vl := e.Variants["dark"], e.Variants["light"]
				if !validExt(vd) {
					vd = ""
				}
				if !validExt(vl) {
					vl = ""
				}
				if len(e.Aliases) > maxAliasesPerRow {
					e.Aliases = e.Aliases[:maxAliasesPerRow]
				}
				la := make([]string, 0, len(e.Aliases))
				for _, a := range e.Aliases {
					if a = strings.Join(strings.Fields(strings.ToLower(a)), " "); a != "" && len(a) <= 128 {
						la = append(la, a)
					}
				}
				if _, err = insIcon.Exec(slug, e.Format, vd, vl, strings.ToLower(e.Name), strings.Join(la, "\n")); err != nil {
					return 0, err
				}
			}
			if open {
				if err = tokenDelim(dec, '}'); err != nil {
					return 0, err
				}
			}
		case "aliasMap":
			var open bool
			if open, err = openObject(dec); err != nil {
				return 0, err
			}
			for open && dec.More() {
				var a, slug string
				if a, err = tokenString(dec); err != nil {
					return 0, err
				}
				if slug, err = tokenString(dec); err != nil {
					return 0, err
				}
				if iconName.MatchString(a) && iconName.MatchString(slug) {
					if _, err = insAlias.Exec(a, slug); err != nil {
						return 0, err
					}
				}
			}
			if open {
				if err = tokenDelim(dec, '}'); err != nil {
					return 0, err
				}
			}
		default: // note, generated, sources, counts: small, and not needed
			var skip json.RawMessage
			if err = dec.Decode(&skip); err != nil {
				return 0, err
			}
		}
	}
	if err = tokenDelim(dec, '}'); err != nil {
		return 0, err
	}
	if !sawSchema {
		return 0, errors.New("icon index: no schema field")
	}
	if n == 0 {
		return 0, errors.New("icon index: no icons")
	}
	// an alias must point at a real icon and never shadow a real slug
	for _, stmt := range []string{
		`DELETE FROM alias WHERE slug NOT IN (SELECT slug FROM icons)`,
		`DELETE FROM alias WHERE a IN (SELECT slug FROM icons)`,
		fmt.Sprintf(`PRAGMA user_version = %d`, indexSchema),
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// syncFile fsyncs a file by path; syncDir does the same for a directory so the
// rename itself survives a power cut.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// ── on-disk state and refresh ────────────────────────────────────────────────

type indexMeta struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	CheckedAt    time.Time `json:"checkedAt"`
	Source       string    `json:"source"`
}

func (k *Catalog) indexPath() string { return filepath.Join(k.cacheDir, indexFileName) }
func (k *Catalog) metaPath() string  { return filepath.Join(k.cacheDir, indexMetaName) }

func (k *Catalog) readMeta() indexMeta {
	var m indexMeta
	if b, err := os.ReadFile(k.metaPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	// a metadata file from another source says nothing about this one
	if m.Source != k.baseURL {
		return indexMeta{}
	}
	return m
}

// lastChecked is when the index source was last asked. The value in memory wins: a
// read-only cache directory cannot keep it on disk, and without it a 304 would be
// followed by another request a minute later, for ever.
func (k *Catalog) lastChecked() time.Time {
	k.mu.RLock()
	t := k.checked
	k.mu.RUnlock()
	if !t.IsZero() {
		return t
	}
	return k.readMeta().CheckedAt
}

func (k *Catalog) writeMeta(m indexMeta) {
	k.mu.Lock()
	k.checked = m.CheckedAt
	k.mu.Unlock()
	m.Source = k.baseURL
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := k.save(indexMetaName, b); err != nil {
		slog.Debug("icon index metadata not saved", "err", err)
	}
}

// index returns the loaded index, nil when there is none yet.
func (k *Catalog) index() *iconIndex {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.idx
}

// acquire returns the loaded index for one use (nil when there is none); the caller
// ends it with release. An index replaced meanwhile stays open until its last reader
// is done, so a query never meets a closed database.
func (k *Catalog) acquire() *iconIndex {
	k.mu.RLock()
	defer k.mu.RUnlock()
	x := k.idx
	if x != nil {
		x.mu.Lock()
		x.refs++
		x.mu.Unlock()
	}
	return x
}

// swapIndex installs a new index and retires the old one: it is closed once the
// readers that already hold it are done.
func (k *Catalog) swapIndex(x *iconIndex) {
	k.mu.Lock()
	old := k.idx
	k.idx = x
	k.mu.Unlock()
	k.net.forgetMisses()
	if old != nil {
		old.retire()
	}
}

// loadIndexFromDisk opens the index file if there is a good one. A damaged file is
// removed so the next refresh rebuilds it.
func (k *Catalog) loadIndexFromDisk() bool {
	if k.cacheDir == "" || k.Mode() == "zip" {
		return false
	}
	x, err := openIconIndex(k.indexPath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("icon index on disk is unusable, it will be downloaded again", "err", err)
			_ = os.Remove(k.indexPath())
			_ = os.Remove(k.metaPath())
		}
		return false
	}
	k.swapIndex(x)
	return true
}

// Refresh brings the index up to date once: from disk first (instant, works
// offline), then from the network when the copy on disk is older than a day. A
// failure keeps whatever index is already loaded. No internet is not an error —
// the picker keeps searching the index on disk.
func (k *Catalog) Refresh(ctx context.Context) {
	k.mu.Lock()
	k.life = ctx
	k.mu.Unlock()
	if k.Mode() == "zip" || k.cacheDir == "" {
		return
	}
	if k.index() == nil {
		k.loadIndexFromDisk()
	}
	if _, err := k.updateIndex(ctx); err != nil {
		slog.Warn("icon index not refreshed, keeping what is on disk", "err", err)
	}
}

// RunRefresh keeps the index fresh for the life of ctx: a conditional request when
// the copy on disk is a day old, a retry every 15 minutes while there is none or a
// refresh failed. Call it after Refresh, which does the first pass.
func (k *Catalog) RunRefresh(ctx context.Context) {
	if k.Mode() == "zip" || k.cacheDir == "" {
		return
	}
	failed := k.index() == nil
	for {
		wait := indexRetryEach
		if !failed {
			wait = max(time.Minute, indexRefreshEach-time.Since(k.lastChecked()))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_, err := k.updateIndex(ctx)
		failed = err != nil
		if failed {
			slog.Debug("icon index refresh failed", "err", err)
		}
	}
}

// updateIndex does one conditional GET of <base>/index.json. It reports whether a
// new index was installed.
func (k *Catalog) updateIndex(ctx context.Context) (bool, error) {
	meta := k.readMeta()
	have := k.index() != nil
	if have && time.Since(k.lastChecked()) < indexRefreshEach {
		return false, nil
	}
	host := hostOf(k.baseURL)
	if k.net.hostDown(host) {
		return false, fmt.Errorf("icon source %s on cooldown after a failure", host)
	}
	ctx, cancel := context.WithTimeout(ctx, indexBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.baseURL+"/index.json", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	req.Header.Set("Accept", "application/json")
	if have { // a conditional request only makes sense when the copy we hold matches the validators
		if meta.ETag != "" {
			req.Header.Set("If-None-Match", meta.ETag)
		}
		if meta.LastModified != "" {
			req.Header.Set("If-Modified-Since", meta.LastModified)
		}
	}
	resp, err := k.client.Do(req)
	if err != nil {
		k.net.markDown(host)
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && have:
		meta.CheckedAt = time.Now()
		k.writeMeta(meta)
		return false, nil
	case resp.StatusCode >= 500:
		k.net.markDown(host)
		return false, fmt.Errorf("icon index: %s", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("icon index: %s", resp.Status)
	}
	if err := os.MkdirAll(k.cacheDir, 0o755); err != nil {
		return false, err
	}
	k.ensureMarker()
	tmp, err := os.CreateTemp(k.cacheDir, ".icons-index-*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath) // a no-op after the rename

	n, err := importIndex(ctx, io.LimitReader(resp.Body, maxIndexBytes), tmpPath)
	if err != nil {
		return false, err
	}
	if err := syncFile(tmpPath); err != nil {
		return false, err
	}
	// the new file must open as an index before it replaces the old one
	if x, err := openIconIndex(tmpPath); err != nil {
		return false, err
	} else {
		x.close()
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, k.indexPath()); err != nil {
		return false, err
	}
	syncDir(k.cacheDir)
	x, err := openIconIndex(k.indexPath())
	if err != nil {
		return false, err
	}
	k.writeMeta(indexMeta{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), CheckedAt: time.Now()})
	k.swapIndex(x)
	// the import allocated decoder buffers and sqlite pages: hand them back now, once,
	// instead of letting them linger until the scavenger gets to them
	debug.FreeOSMemory()
	slog.Info("icon index imported", "icons", n, "source", k.baseURL)
	k.retainAgain()
	return true, nil
}
