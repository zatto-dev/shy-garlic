package server

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

// User wallpapers live on the server, not in the browser.
//
// The prototype kept the image in localStorage and admitted its own failure
// with the message "Image too large to remember". A file on disk lifts that limit, but
// in return it opens the only place in this panel where the user uploads bytes.
// That is why the whole handling stands on three rules:
//
//  1. a name from the client NEVER creates a path — the body is rewritten into
//     our alphabet, and the extension comes only from sniffing the content;
//  2. every file operation goes through an os.Root handle, so traversal and a
//     planted symlink are cut off at the syscall level, not by comparing
//     strings;
//  3. the /bg/ response is sandboxed with its own CSP — even if a polyglot
//     got through validation, it has no right to execute anything.

const (
	backgroundFileLimit  = 8 << 20 // 8 MiB per file
	BackgroundLimitMB    = 8       // the same in megabytes — for the UI and messages
	backgroundFilesLimit = 20
	backgroundByteLimit  = 100 << 20
	maxBackgroundBody    = 48
	uploadTimeout        = 30 * time.Second
	junkBackgroundAge    = 30 * 24 * time.Hour
)

// backgroundMIME — a constant format→MIME map. The response gets it ONLY from here, never
// a header from the client or a guess from the name.
var backgroundMIME = map[string]string{
	"jpg":  "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
	"gif":  "image/gif",
	"avif": "image/avif",
}

var (
	errBackgroundsDisabled = errors.New("backgrounds directory unavailable")
	errNoBackground        = errors.New("no such background")
	errBackgroundLimit     = errors.New("background limit reached")
)

// BackgroundEntry — one uploaded wallpaper in the JSON response.
type BackgroundEntry struct {
	Name  string `json:"name"`
	Kind  string `json:"type"`
	Bytes int64  `json:"bytes"`
	At    string `json:"at"`
}

type BackgroundStore struct {
	dir       string
	root      *os.Root
	available bool
	// sem trims concurrent uploads — two at once are enough, and without it
	// a dozen requests of 8 MB can clog the writable layer
	sem chan struct{}
}

// NewBackgroundStore prepares the directory and immediately checks whether it can be written to.
// A failure does not take the panel down — it disables the feature and says what to fix.
func NewBackgroundStore(dir string) *BackgroundStore {
	k := &BackgroundStore{dir: dir, sem: make(chan struct{}, 2)}
	if dir == "" {
		return k
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Error("cannot create the backgrounds directory", "dir", dir, "err", err)
		return k
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		slog.Error("cannot open the backgrounds directory", "dir", dir, "err", err)
		return k
	}
	// trial write: better to know now than after accepting and decoding 8 MB
	trial := ".proba-zapisu.tmp"
	f, err := root.OpenFile(trial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Close()
		slog.Error("backgrounds directory not writable, wallpaper upload disabled",
			"dir", dir, "err", err, "fix", "chown -R 65532:65532 "+dir)
		return k
	}
	_ = f.Close()
	_ = root.Remove(trial)

	k.root, k.available = root, true
	slog.Info("backgrounds directory ready", "dir", dir)
	return k
}

func (k *BackgroundStore) Available() bool { return k != nil && k.available }

func (k *BackgroundStore) Close() {
	if k != nil && k.root != nil {
		_ = k.root.Close()
	}
}

// ── names ────────────────────────────────────────────────────────────────────

// sanitizeName turns a name given by the client into a body that surely
// will not create a path.
//
// ! filepath.Base does NOT cut `..\x` on Linux, and r.PathValue returns the segment already
// unescaped, so `%2e%2e%2f` arrives here as `../`. That is why we do not filter
// known payloads, but rewrite the name into an alphabet in which none of them
// can be expressed. The extension is added separately by sniffing the content.
func sanitizeName(raw, format string) (string, bool) {
	body := strings.ToLower(raw)
	if i := strings.LastIndexAny(body, `/\`); i >= 0 {
		body = body[i+1:]
	}
	if i := strings.LastIndexByte(body, '.'); i > 0 {
		body = body[:i] // the extension from the client is meaningless
	}

	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-':
			// collapse repeated separators
			if (c == '.' || c == '-') && b.Len() > 0 {
				if p := b.String()[b.Len()-1]; p == '.' || p == '-' {
					continue
				}
			}
			b.WriteByte(c)
		}
	}
	body = strings.Trim(b.String(), ".-")
	if len(body) > maxBackgroundBody {
		body = strings.Trim(body[:maxBackgroundBody], ".-")
	}
	if body == "" {
		body = "tlo"
	}

	name := body + "." + format
	if _, known := backgroundMIME[format]; !known || !config.BackgroundName.MatchString(name) {
		return "", false
	}
	return name, true
}

// formatFromName reads the extension from OUR name — the one we wrote ourselves
// after sniffing. The client's name does not pass through here.
func formatFromName(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	format := name[i+1:]
	_, ok := backgroundMIME[format]
	return format, ok
}

// ── format detection ─────────────────────────────────────────────────────────

// detectImageKind determines the format ONLY from the file content.
//
// ! SVG has no entry here and never will: opened as a document it executes
// <script>, and our CSP must hold 'unsafe-inline' for scripts, because index.html
// injects the service state.
func detectImageKind(header []byte) (string, bool) {
	if isAVIF(header) {
		return "avif", true
	}
	switch http.DetectContentType(header) {
	case "image/jpeg":
		return "jpg", true
	case "image/png":
		return "png", true
	case "image/webp":
		return "webp", true
	case "image/gif":
		return "gif", true
	}
	return "", false
}

// isAVIF recognizes the ISO-BMFF `ftyp` box with an avif/avis brand.
// http.DetectContentType does not know AVIF and would return application/octet-stream.
func isAVIF(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	if brand := string(b[8:12]); brand == "avif" || brand == "avis" {
		return true
	}
	size := int(binary.BigEndian.Uint32(b[:4]))
	if size > len(b) || size < 16 {
		size = len(b)
	}
	for i := 16; i+4 <= size; i += 4 { // compatible brands come after major_brand and the version
		if m := string(b[i : i+4]); m == "avif" || m == "avis" {
			return true
		}
	}
	return false
}

// ── file operations ──────────────────────────────────────────────────────────

// List returns the uploaded wallpapers, newest first.
func (k *BackgroundStore) List() ([]BackgroundEntry, int64, error) {
	if !k.Available() {
		return nil, 0, errBackgroundsDisabled
	}
	entries, err := os.ReadDir(k.dir)
	if err != nil {
		return nil, 0, err
	}
	out := make([]BackgroundEntry, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() || !config.BackgroundName.MatchString(e.Name()) {
			continue // temp files start with a dot and drop out here
		}
		format, ok := formatFromName(e.Name())
		if !ok {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		total += fi.Size()
		out = append(out, BackgroundEntry{
			Name: e.Name(), Kind: backgroundMIME[format],
			Bytes: fi.Size(), At: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, total, nil
}

// freeName adds a suffix while the name is taken. It never overwrites
// someone else's file — two people uploading "tlo.png" get two files.
func (k *BackgroundStore) freeName(name string) (string, error) {
	if _, err := k.root.Stat(name); errors.Is(err, os.ErrNotExist) {
		return name, nil
	}
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		return "", errNoBackground
	}
	body, format := name[:dot], name[dot+1:]
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d.%s", body, i, format)
		if !config.BackgroundName.MatchString(candidate) {
			return "", errBackgroundLimit
		}
		if _, err := k.root.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errBackgroundLimit
}

// Save puts the stream on disk atomically: first a temp file, only after a
// full write and fsync a rename to the proper name.
//
// ! The pattern from config/save.go, not from icons.go — there Sync before Rename is missing,
// which for a CDN icon is harmless (it can be downloaded again), but for a
// user's irreplaceable file it is not.
func (k *BackgroundStore) Save(clientName string, header []byte, rest io.Reader) (BackgroundEntry, error) {
	if !k.Available() {
		return BackgroundEntry{}, errBackgroundsDisabled
	}
	format, ok := detectImageKind(header)
	if !ok {
		return BackgroundEntry{}, errors.New("unsupported image format")
	}
	name, ok := sanitizeName(clientName, format)
	if !ok {
		return BackgroundEntry{}, errors.New("name cannot be salvaged")
	}

	list, total, err := k.List()
	if err != nil {
		return BackgroundEntry{}, err
	}
	if len(list) >= backgroundFilesLimit || total >= backgroundByteLimit {
		return BackgroundEntry{}, errBackgroundLimit
	}

	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return BackgroundEntry{}, err
	}
	tmp := ".wgrywanie-" + hex.EncodeToString(random) + ".tmp"
	f, err := k.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return BackgroundEntry{}, err
	}
	cleanup := func() {
		_ = f.Close()
		_ = k.root.Remove(tmp)
	}

	n, err := f.Write(header)
	if err != nil {
		cleanup()
		return BackgroundEntry{}, err
	}
	copied, err := io.Copy(f, rest)
	if err != nil {
		cleanup()
		return BackgroundEntry{}, err
	}
	size := int64(n) + copied
	if total+size > backgroundByteLimit {
		cleanup()
		return BackgroundEntry{}, errBackgroundLimit
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return BackgroundEntry{}, err
	}
	// umask in distroless would trim the bits from OpenFile, hence an explicit Chmod
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return BackgroundEntry{}, err
	}
	if err := f.Close(); err != nil {
		_ = k.root.Remove(tmp)
		return BackgroundEntry{}, err
	}

	target, err := k.freeName(name)
	if err != nil {
		_ = k.root.Remove(tmp)
		return BackgroundEntry{}, err
	}
	if err := k.root.Rename(tmp, target); err != nil {
		_ = k.root.Remove(tmp)
		return BackgroundEntry{}, err
	}
	return BackgroundEntry{
		Name: target, Kind: backgroundMIME[format], Bytes: size,
		At: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// Remove deletes a wallpaper by a name from OUR alphabet.
//
// ! Order matters: first the regex, then Stat (missing = 404 and done,
// without touching anything), and only at the end Remove. handleServiceDelete
// today takes ?name= with no validation — that is exactly the pattern
// new code must not repeat.
func (k *BackgroundStore) Remove(name string) error {
	if !k.Available() {
		return errBackgroundsDisabled
	}
	if !config.BackgroundName.MatchString(name) {
		return errNoBackground
	}
	if _, err := k.root.Stat(name); err != nil {
		return errNoBackground
	}
	return k.root.Remove(name)
}

// Exists says whether a file with such a name lies in the backgrounds directory. Used when
// saving the appearance: the config must not point to a wallpaper that does not exist.
func (k *BackgroundStore) Exists(name string) bool {
	if !k.Available() || !config.BackgroundName.MatchString(name) {
		return false
	}
	fi, err := k.root.Stat(name)
	return err == nil && !fi.IsDir()
}

// Open gives the file to send. The caller closes the handle.
func (k *BackgroundStore) Open(name string) (*os.File, os.FileInfo, string, error) {
	if !k.Available() || !config.BackgroundName.MatchString(name) {
		return nil, nil, "", errNoBackground
	}
	format, ok := formatFromName(name)
	if !ok {
		return nil, nil, "", errNoBackground
	}
	f, err := k.root.Open(name)
	if err != nil {
		return nil, nil, "", errNoBackground
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		_ = f.Close()
		return nil, nil, "", errNoBackground
	}
	return f, fi, backgroundMIME[format], nil
}

// SweepStale deletes UNUSED wallpapers older than 30 days, and only
// when the directory exceeded the limit. The panel must not, on its own initiative,
// throw away files that someone deliberately left alone.
func (k *BackgroundStore) SweepStale(inUse string) int {
	list, total, err := k.List()
	if err != nil {
		return 0
	}
	if len(list) <= backgroundFilesLimit && total <= backgroundByteLimit {
		return 0
	}
	threshold := time.Now().Add(-junkBackgroundAge).UTC().Format(time.RFC3339)
	removed := 0
	for i := len(list) - 1; i >= 0; i-- { // from the oldest
		w := list[i]
		if w.Name == inUse || w.At >= threshold {
			continue
		}
		if err := k.Remove(w.Name); err != nil {
			continue
		}
		removed++
		total -= w.Bytes
		if len(list)-removed <= backgroundFilesLimit && total <= backgroundByteLimit {
			break
		}
	}
	if removed > 0 {
		slog.Info("old wallpapers removed", "count", removed)
	}
	return removed
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// handleBackgroundUpload accepts a single image file.
//
// Uploading a file by itself changes NOTHING in the config — only pointing to it
// as the instance background goes through PUT /api/appearance, i.e. through diff, version
// and commit. Only bytes land here.
func (s *Server) handleBackgroundUpload(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	if !s.backgrounds.Available() {
		jsonError(w, http.StatusServiceUnavailable, i18n.T(lang, "tapeta_wylaczona"))
		return
	}
	// ! the FIRST line of the handler, before any parsing
	r.Body = http.MaxBytesReader(w, r.Body, backgroundFileLimit)

	select {
	case s.backgrounds.sem <- struct{}{}:
		defer func() { <-s.backgrounds.sem }()
	default:
		jsonError(w, http.StatusConflict, i18n.T(lang, "tapeta_limit"))
		return
	}
	// the server has no ReadTimeout (deliberately, because of SSE), so one infinitely
	// slow body would hold a goroutine forever
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadTimeout))

	// r.MultipartReader, NEVER ParseMultipartForm: the latter spills the excess
	// to os.TempDir() and clogs the container's writable layer
	mr, err := r.MultipartReader()
	if err != nil {
		s.backgroundErrorResponse(w, lang, err)
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		s.backgroundErrorResponse(w, lang, err)
		return
	}
	defer part.Close()
	if part.FormName() != "file" {
		jsonError(w, http.StatusBadRequest, "expected a single part named \"file\"")
		return
	}

	header := make([]byte, 512)
	n, err := io.ReadFull(part, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		s.backgroundErrorResponse(w, lang, err)
		return
	}
	// ! FileName() and the part's Content-Type are 100% under the uploader's control —
	// the first one is used only to derive the body, the second for nothing
	entry, err := s.backgrounds.Save(part.FileName(), header[:n], part)
	if err != nil {
		s.backgroundErrorResponse(w, lang, err)
		return
	}
	if _, err := mr.NextPart(); !errors.Is(err, io.EOF) {
		_ = s.backgrounds.Remove(entry.Name)
		jsonError(w, http.StatusBadRequest, "expected exactly one part")
		return
	}
	slog.Info("wallpaper uploaded", "name", entry.Name, "bytes", entry.Bytes)
	jsonReply(w, http.StatusCreated, entry)
}

// backgroundErrorResponse translates a write error to an HTTP code and a message for a human.
func (s *Server) backgroundErrorResponse(w http.ResponseWriter, lang i18n.Lang, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge), strings.Contains(err.Error(), "request body too large"):
		jsonError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf(i18n.T(lang, "tapeta_za_duza"), BackgroundLimitMB))
	case errors.Is(err, errBackgroundLimit):
		jsonError(w, http.StatusConflict, i18n.T(lang, "tapeta_limit"))
	case errors.Is(err, errBackgroundsDisabled):
		jsonError(w, http.StatusServiceUnavailable, i18n.T(lang, "tapeta_wylaczona"))
	case strings.Contains(err.Error(), "format"), strings.Contains(err.Error(), "nazwa"):
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "tapeta_zly_typ"))
	default:
		slog.Warn("wallpaper upload failed", "err", err)
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_zapisu_tla"))
	}
}

func (s *Server) handleBackgroundList(w http.ResponseWriter, r *http.Request) {
	list, _, err := s.backgrounds.List()
	if err != nil {
		if errors.Is(err, errBackgroundsDisabled) {
			jsonReply(w, http.StatusOK, map[string]any{"items": []BackgroundEntry{}, "used": "",
				"count": 0, "limitFiles": backgroundFilesLimit, "limitBytes": backgroundByteLimit})
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"items": list, "used": s.instanceAppearance().Image, "count": len(list),
		"limitFiles": backgroundFilesLimit, "limitBytes": backgroundByteLimit,
	})
}

func (s *Server) handleBackgroundRemove(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	name := r.PathValue("nazwa")
	if !config.BackgroundName.MatchString(name) {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
		return
	}
	// a background in use is deleted only after the config change — otherwise the panel stays
	// with a url() returning 404, and an appearance change should go through diff like any other
	if s.instanceAppearance().Image == name {
		jsonError(w, http.StatusConflict, i18n.T(lang, "tapeta_uzywana_blad"))
		return
	}
	if err := s.backgrounds.Remove(name); err != nil {
		if errors.Is(err, errBackgroundsDisabled) {
			jsonError(w, http.StatusServiceUnavailable, i18n.T(lang, "tapeta_wylaczona"))
			return
		}
		jsonError(w, http.StatusNotFound, i18n.T(lang, "blad_wygladu"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBg serves an uploaded wallpaper — only to logged-in users.
func (s *Server) handleBg(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("nazwa")
	f, fi, kind, err := s.backgrounds.Open(name)
	if err != nil {
		// 404 also for a bad name: we do not reveal what exists and what does not
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	h := w.Header()
	h.Set("Content-Type", kind)
	h.Set("X-Content-Type-Options", "nosniff")
	// the strongest single defense here: a document with no right to do
	// anything, even if validation ever let a polyglot through
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", `inline; filename="`+name+`"`)
	// ! not public/604800 like icons: a background is sometimes replaced under the same name,
	// and `public` on a resource behind a login would store it in a shared proxy
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// DefaultBackgroundDir — next to the DB, but in a SEPARATE subdirectory.
//
// ! Never in the same directory as the DB: a file uploaded under the name `shygarlic.db`
// would overwrite it, UserCount() would return 0, and then /setup stands open
// and creates an administrator account without authentication. An image upload would be
// a panel takeover.
func DefaultBackgroundDir(dataPath string) string {
	return filepath.Join(filepath.Dir(dataPath), "bg")
}
