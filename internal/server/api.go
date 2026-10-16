package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/dockerdisc"
	"github.com/zatto-dev/shy-garlic/internal/gitrepo"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/notify"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// Editor API. Every change goes through the engine from internal/config, so the
// YAML file after a save differs by exactly what the user changed — comments,
// anchors and formatting stay in place.

type errorResponse struct {
	Error string `json:"error"`
}

func jsonReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	jsonReply(w, code, errorResponse{Error: msg})
}

// Limits on JSON request bodies. Without them a single multi-GB POST took the
// process down (json.Decoder reads to the end, and a logged-in user is not
// always a trusted one).
const (
	limitJSON   = 1 << 20 // 1 MiB — the body of every ordinary request is a few hundred bytes
	configLimit = 2 << 20 // 2 MiB — PUT /api/config carries the whole YAML inside JSON
)

// decodeJSON reads the request body into v, at most limitJSON bytes.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeJSONLimit(w, r, v, limitJSON)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return json.NewDecoder(r.Body).Decode(v)
}

// withEdit opens the file, calls fn, then shows or saves the change.
//
// With ?dry=1 it saves nothing — it returns a diff, so the user sees exactly
// what will change BEFORE agreeing. This is not decoration: research showed that
// with editors of other people's files the problem is not missing features but
// missing trust.
//
// After a successful save: an entry in the history (with the option to undo) and
// a git commit, if the file lives in a repository. A conflict with a manual edit
// of the file ends in 409, not a silent overwrite.
func (s *Server) withEdit(w http.ResponseWriter, r *http.Request, description string, fn func(*config.Document) error) bool {
	return s.withEditKeys(w, r, description, nil, fn)
}

// withEditKeys is like withEdit, but after a successful save it moves the history
// and state of the services whose key (group/name) changed. migrate receives the
// config from before the change and returns old → new pairs; nil = nothing to move.
func (s *Server) withEditKeys(w http.ResponseWriter, r *http.Request, description string,
	migrate func(old *config.Config) map[string]string, fn func(*config.Document) error) bool {
	return s.withEditOpen(w, r, description, migrate, config.Open, fn)
}

// withEditOpen — like withEditKeys, but with a choice of how the file is opened.
// The raw YAML editor and version restore use config.OpenRaw so they can repair
// a file that config.Open will not accept.
func (s *Server) withEditOpen(w http.ResponseWriter, r *http.Request, description string,
	migrate func(old *config.Config) map[string]string,
	open func(string) (*config.Document, error), fn func(*config.Document) error) bool {
	s.edit.Lock()
	defer s.edit.Unlock()

	before, err := configBytes(s.path)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "cannot read the config: "+err.Error())
		return false
	}
	d, err := open(s.path)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "cannot open the config: "+err.Error())
		return false
	}
	if err := fn(d); err != nil {
		code := http.StatusBadRequest
		message := err.Error()
		switch {
		case errors.Is(err, config.ErrServiceNotFound), errors.Is(err, config.ErrGroupNotFound):
			code = http.StatusNotFound
		case errors.Is(err, config.ErrDuplicate):
			code = http.StatusConflict
			message = i18n.T(i18n.FromRequest(r), "blad_duplikat_nazwy")
		case errors.Is(err, config.ErrInheritedField):
			message = i18n.T(i18n.FromRequest(r), "blad_pole_dziedziczone")
		case errors.Is(err, config.ErrUnsupportedLayout):
			message = i18n.T(i18n.FromRequest(r), "blad_uklad_pliku")
		}
		jsonError(w, code, message)
		return false
	}
	if _, err := config.Load(d.Bytes()); err != nil {
		jsonError(w, http.StatusInternalServerError, "the change would produce an invalid file: "+err.Error())
		return false
	}

	diff := config.Diff(before, d.Bytes(), 3)
	added, removed := config.SummarizeDiff(diff)

	if r.URL.Query().Get("dry") == "1" {
		jsonReply(w, http.StatusOK, map[string]any{
			"dry": true, "diff": diff, "added": added, "removed": removed, "summary": description,
		})
		return false
	}

	// nothing changed (e.g. a move to the place where the element already stands):
	// no save, no history entry, no commit
	if bytes.Equal(before, d.Bytes()) {
		return true
	}

	if err := d.Save(); err != nil {
		if errors.Is(err, config.ErrChangedOnDisk) {
			jsonError(w, http.StatusConflict, i18n.T(i18n.FromRequest(r), "konflikt_pliku"))
			return false
		}
		jsonError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return false
	}

	author := "?"
	if u := UserFromRequest(r); u != nil {
		author = u.Login
	}
	if err := s.st.SaveVersion(r.Context(), author, description, string(d.Bytes()), diff); err != nil {
		slog.Error("config history", "err", err)
	}
	s.commitToGit(r.Context(), description, author)

	// ! after Save (the YAML is already on disk), before reload (which forgets unknown keys)
	if migrate != nil {
		var old *config.Config
		if c, err := config.Load(before); err == nil {
			old = c
		}
		s.migrateKeys(r.Context(), migrate(old))
	}

	if err := s.reload(s.ctx); err != nil {
		slog.Error("reload after save", "err", err)
	}
	return true
}

// commitToGit leaves a trace in the repository if the config lives in one.
// No repo or no git is not an error — nothing simply happens.
func (s *Server) commitToGit(ctx context.Context, description, author string) {
	if !gitrepo.InRepo(ctx, s.path) {
		return
	}
	message := "shyg: " + description
	if err := gitrepo.Commit(ctx, s.path, message, author); err != nil {
		slog.Warn("config commit failed", "err", err)
		return
	}
	slog.Info("config committed", "description", description, "author", author)
}

// ── services ─────────────────────────────────────────────────────────────────

type serviceRequest struct {
	Group    string `json:"group"`
	NewGroup string `json:"newGroup,omitempty"`
	Name     string `json:"name"`
	NewName  string `json:"newName,omitempty"`
	URL      string `json:"url"`
	Icon     string `json:"icon"`
	// The check fields are pointers for the same reason as the description: on edit
	// an absent field means "do not touch", and an empty one — "back to the default".
	Interval *string `json:"interval,omitempty"`
	Timeout  *string `json:"timeout,omitempty"`
	Expect   *[]int  `json:"expect,omitempty"`
	Insecure *bool   `json:"insecure,omitempty"`
	Size     string  `json:"size,omitempty"`
	// Description and Tags are pointers: an absent field means "do not touch", a
	// present but empty one — "remove from the file".
	Description *string   `json:"description,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
}

// descriptionAndTags validates the optional description and tags of a request.
// Returns the values after normalization (nil = the field did not arrive) or the
// key of the error text.
func (req serviceRequest) descriptionAndTags() (description *string, tags *[]string, err string) {
	if req.Description != nil {
		v, err := config.NormalizeDescription(*req.Description)
		switch {
		case errors.Is(err, config.ErrDescriptionTooLong):
			return nil, nil, "blad_opis_dlugi"
		case err != nil:
			return nil, nil, "blad_opis_znaki"
		}
		description = &v
	}
	if req.Tags != nil {
		v, err := config.NormalizeTags(*req.Tags)
		if err != nil {
			return nil, nil, "blad_tagi"
		}
		tags = &v
	}
	return description, tags, ""
}

// tagsToJSON — an empty list as [], not null; JS iterates over it without checking.
func tagsToJSON(t config.Tags) []string {
	if len(t) == 0 {
		return []string{}
	}
	return []string(t)
}

// serviceFromConfig returns the service from the in-memory state.
func (s *Server) serviceFromConfig(group, name string) (config.Service, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil {
		return config.Service{}, false
	}
	for _, g := range s.cfg.Groups {
		if g.Name != group {
			continue
		}
		for _, u := range g.Services {
			if u.Name == name {
				return u, true
			}
		}
	}
	return config.Service{}, false
}

// sizeFromRequest lets only the allowlist through; empty stays empty, so we do
// not write a key into the file that nobody asked for.
func sizeFromRequest(v string) string {
	if config.Allowed(v, config.Sizes) {
		return v
	}
	return ""
}

// validEntryName guards the parameters we use to search the YAML.
// ! handleServiceDelete and handleGroupDelete took ?name= with no validation at all.
func validEntryName(v string) bool {
	if v == "" || len(v) > 200 || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Server) handleServiceCreate(w http.ResponseWriter, r *http.Request) {
	var req serviceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if req.Name == "" || req.Group == "" {
		jsonError(w, http.StatusBadRequest, "name and group are required")
		return
	}
	if !config.ValidServiceAddress(req.URL) {
		jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), "blad_adres_uslugi"))
		return
	}
	check, checkKey := req.parseCheck()
	if checkKey != "" {
		jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), checkKey))
		return
	}
	newDescription, newTags, err := req.descriptionAndTags()
	if err != "" {
		jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), err))
		return
	}
	svc := config.Service{Name: req.Name, URL: req.URL, Icon: req.Icon, Size: sizeFromRequest(req.Size)}
	if newDescription != nil {
		svc.Description = *newDescription
	}
	if newTags != nil {
		svc.Tags = config.Tags(*newTags)
	}
	svc.Check = check

	description := fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_dodano_usluge"), req.Name, req.Group)
	if s.withEdit(w, r, description, func(d *config.Document) error { return d.AddService(req.Group, svc) }) {
		jsonReply(w, http.StatusCreated, map[string]any{"ok": true})
	}
}

// parseCheck reads and validates the check fields of a request. POST, bulk
// and PATCH share it, so an invalid value is rejected with 400 everywhere
// instead of being silently dropped on create (SHYG-52). Returns the i18n key
// of the problem, or "".
func (req serviceRequest) parseCheck() (config.Check, string) {
	var c config.Check
	for _, f := range []struct {
		in  *string
		out *config.Duration
	}{{req.Interval, &c.Interval}, {req.Timeout, &c.Timeout}} {
		if f.in == nil {
			continue
		}
		t := strings.TrimSpace(*f.in)
		if t == "" {
			continue // empty = default
		}
		v, err := time.ParseDuration(t)
		// below one second the ticker would flood the checked service
		if err != nil || v < time.Second {
			return c, "blad_czas_sprawdzania"
		}
		*f.out = config.Duration(v)
	}
	if req.Expect != nil {
		for _, k := range *req.Expect {
			if k < 100 || k > 599 {
				return c, "blad_kody_http"
			}
		}
		c.Expect = *req.Expect
	}
	if req.Insecure != nil {
		c.Insecure = *req.Insecure
	}
	return c, ""
}

// checkChange recomputes the fields of the "Check" section from an edit request.
// A value equal to the current one (after defaults) is not a change — the form
// sends back what it showed, and defaults must not land in the file. Returns the
// key of the error text.
func (req serviceRequest) checkChange(current config.Check) (config.CheckChange, string) {
	var z config.CheckChange
	c, key := req.parseCheck()
	if key != "" {
		return z, key
	}
	duration := func(given bool, v, current config.Duration, def time.Duration) *config.Duration {
		if !given {
			return nil
		}
		effective := v
		if v == 0 {
			effective = config.Duration(def)
		}
		if effective == current {
			return nil
		}
		return &v
	}
	z.Interval = duration(req.Interval != nil, c.Interval, current.Interval, config.DefaultInterval)
	z.Timeout = duration(req.Timeout != nil, c.Timeout, current.Timeout, config.DefaultTimeout)
	if req.Expect != nil {
		effective := c.Expect
		if len(effective) == 0 {
			effective = config.DefaultExpect
		}
		if !slices.Equal(effective, current.Expect) {
			z.Expect = req.Expect
		}
	}
	if req.Insecure != nil && *req.Insecure != current.Insecure {
		z.Insecure = req.Insecure
	}
	return z, ""
}

func (s *Server) handleServiceUpdate(w http.ResponseWriter, r *http.Request) {
	var req serviceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if req.Name == "" || req.Group == "" {
		jsonError(w, http.StatusBadRequest, "name and group are required")
		return
	}
	lang := i18n.FromRequest(r)
	if !config.ValidServiceAddress(req.URL) {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_adres_uslugi"))
		return
	}
	newDescription, newTags, errorKey := req.descriptionAndTags()
	if errorKey != "" {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, errorKey))
		return
	}
	// without an in-memory config we compare against the defaults — SetCheck
	// touches only what differs from the file anyway
	size := sizeFromRequest(req.Size)
	currentCheck := config.Check{Interval: config.Duration(config.DefaultInterval),
		Timeout: config.Duration(config.DefaultTimeout), Expect: config.DefaultExpect}
	// a value equal to the current one is not a change — no point saving it, and for
	// a field inherited from a merge key saving would be impossible
	if current, ok := s.serviceFromConfig(req.Group, req.Name); ok {
		currentCheck = current.Check
		// the dialog always sends back the size — the default `md` must not land in the file
		if size == current.Size {
			size = ""
		}
		if newDescription != nil && *newDescription == current.Description {
			newDescription = nil
		}
		if newTags != nil && slices.Equal(*newTags, []string(current.Tags)) {
			newTags = nil
		}
	}
	check, errorKey := req.checkChange(currentCheck)
	if errorKey != "" {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, errorKey))
		return
	}
	description := fmt.Sprintf(i18n.T(lang, "opis_zmieniono_usluge"), req.Name)
	if req.NewGroup != "" && req.NewGroup != req.Group {
		description = fmt.Sprintf(i18n.T(lang, "opis_przeniesiono"), req.Name, req.Group, req.NewGroup)
	}
	// ! pairs computed NOW — fn below overwrites req.Group with the new group
	newGroup, newName := req.Group, req.Name
	if req.NewGroup != "" {
		newGroup = req.NewGroup
	}
	if req.NewName != "" {
		newName = req.NewName
	}
	pairs := map[string]string{key(req.Group, req.Name): key(newGroup, newName)}
	migrate := func(*config.Config) map[string]string { return pairs }
	ok := s.withEditKeys(w, r, description, migrate, func(d *config.Document) error {
		// the move goes first — later changes look up the service in the new group
		group := req.Group
		if req.NewGroup != "" && req.NewGroup != req.Group {
			if err := d.MoveService(req.Group, req.Name, req.NewGroup); err != nil {
				return err
			}
			group = req.NewGroup
		}
		req.Group = group
		if req.URL != "" {
			if err := d.SetServiceField(req.Group, req.Name, "url", req.URL); err != nil {
				return err
			}
		}
		if req.Icon != "" {
			if err := d.SetServiceField(req.Group, req.Name, "icon", req.Icon); err != nil &&
				!errors.Is(err, config.ErrPathNotFound) {
				return err
			}
		}
		if size != "" {
			if err := d.SetServiceField(req.Group, req.Name, "size", size); err != nil &&
				!errors.Is(err, config.ErrPathNotFound) {
				return err
			}
		}
		if newDescription != nil {
			if err := d.SetServiceDescription(req.Group, req.Name, *newDescription); err != nil {
				return err
			}
		}
		if newTags != nil {
			if err := d.SetServiceTags(req.Group, req.Name, *newTags); err != nil {
				return err
			}
		}
		if err := d.SetCheck(req.Group, req.Name, check); err != nil {
			return err
		}
		// rename last — otherwise the following lookups by the old name would fail
		if req.NewName != "" && req.NewName != req.Name {
			if err := d.SetServiceField(req.Group, req.Name, "name", req.NewName); err != nil {
				return err
			}
		}
		return nil
	})
	if ok {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleServiceDelete(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	name := r.URL.Query().Get("name")
	if !validEntryName(group) || !validEntryName(name) {
		jsonError(w, http.StatusBadRequest, "provide group and name")
		return
	}
	description := fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_usunieto_usluge"), name, group)
	if s.withEdit(w, r, description, func(d *config.Document) error { return d.RemoveService(group, name) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleServiceMove puts a service at a specific position — in the same group
// or in another one. index counts in the target list after the move.
func (s *Server) handleServiceMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Group   string `json:"group"`
		Name    string `json:"name"`
		ToGroup string `json:"toGroup"`
		Index   *int   `json:"index"`
	}
	lang := i18n.FromRequest(r)
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if req.ToGroup == "" {
		req.ToGroup = req.Group
	}
	if !validEntryName(req.Group) || !validEntryName(req.Name) ||
		!validEntryName(req.ToGroup) || req.Index == nil {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_przenoszenie"))
		return
	}
	description := fmt.Sprintf(i18n.T(lang, "opis_kolejnosc_uslugi"), req.Name, req.ToGroup, *req.Index+1)
	if req.ToGroup != req.Group {
		description = fmt.Sprintf(i18n.T(lang, "opis_przeniesiono"), req.Name, req.Group, req.ToGroup)
	}
	pairs := map[string]string{key(req.Group, req.Name): key(req.ToGroup, req.Name)}
	if s.withEditKeys(w, r, description, func(*config.Config) map[string]string { return pairs },
		func(d *config.Document) error {
			return d.MoveServiceTo(req.Group, req.Name, req.ToGroup, *req.Index)
		}) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleGroupMove puts a whole group at position index.
func (s *Server) handleGroupMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Index *int   `json:"index"`
	}
	lang := i18n.FromRequest(r)
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if !validEntryName(req.Name) || req.Index == nil {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_przenoszenie"))
		return
	}
	description := fmt.Sprintf(i18n.T(lang, "opis_kolejnosc_grupy"), req.Name, *req.Index+1)
	if s.withEdit(w, r, description, func(d *config.Document) error { return d.MoveGroup(req.Name, *req.Index) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── panel title ──────────────────────────────────────────────────────────────

// handleTitlePut writes the top-level `title` with a single splice. Validation
// lives on the server side regardless of the field in the browser.
func (s *Server) handleTitlePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	lang := i18n.FromRequest(r)
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	title, err := config.NormalizeTitle(req.Title)
	if err != nil {
		key := "blad_tytul_znaki"
		switch {
		case errors.Is(err, config.ErrTitleEmpty):
			key = "blad_tytul_pusty"
		case errors.Is(err, config.ErrTitleTooLong):
			key = "blad_tytul_dlugi"
		}
		jsonError(w, http.StatusBadRequest, i18n.T(lang, key))
		return
	}
	description := fmt.Sprintf(i18n.T(lang, "opis_tytul"), title)
	if s.withEdit(w, r, description, func(d *config.Document) error { return d.SetTitle(title) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true, "title": title})
	}
}

// ── groups ───────────────────────────────────────────────────────────────────

func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.Name == "" {
		jsonError(w, http.StatusBadRequest, "provide name")
		return
	}
	if s.withEdit(w, r, fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_dodano_grupe"), req.Name),
		func(d *config.Document) error { return d.AddGroup(req.Name) }) {
		jsonReply(w, http.StatusCreated, map[string]any{"ok": true})
	}
}

func (s *Server) handleGroupUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.Name == "" || req.NewName == "" {
		jsonError(w, http.StatusBadRequest, "provide name and newName")
		return
	}
	// every service of the group changes key; pairs from the config before the change,
	// because the group name may contain "/" and the prefix would be ambiguous
	migrate := func(old *config.Config) map[string]string {
		pairs := map[string]string{}
		if old == nil {
			return pairs
		}
		for _, g := range old.Groups {
			if g.Name != req.Name {
				continue
			}
			for _, svc := range g.Services {
				pairs[key(g.Name, svc.Name)] = key(req.NewName, svc.Name)
			}
		}
		return pairs
	}
	if s.withEditKeys(w, r, fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_zmieniono_grupe"), req.Name, req.NewName),
		migrate, func(d *config.Document) error { return d.RenameGroup(req.Name, req.NewName) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !validEntryName(name) {
		jsonError(w, http.StatusBadRequest, "provide name")
		return
	}
	if s.withEdit(w, r, fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_usunieto_grupe"), name),
		func(d *config.Document) error { return d.RemoveGroup(name) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── raw YAML ─────────────────────────────────────────────────────────────────

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	raw, err := configBytes(s.path)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonReply(w, http.StatusOK, map[string]any{"yaml": string(raw)})
}

// handleConfigPut saves hand-edited YAML — for those who prefer the file to
// clicking. Rejects content that does not parse.
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := decodeJSONLimit(w, r, &req, configLimit); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if _, err := config.Load([]byte(req.YAML)); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}
	if s.withEditOpen(w, r, i18n.T(i18n.FromRequest(r), "opis_reczna_edycja"), nil, config.OpenRaw,
		func(d *config.Document) error { return d.ReplaceAll([]byte(req.YAML)) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── icons ────────────────────────────────────────────────────────────────────

func (s *Server) handleIcons(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 300 {
		limit = v
	}
	names := s.icons.Search(q, limit)
	// variants carries only the entries that have separate artwork for the dark and
	// light theme — the browser renders both images from it and switches between
	// them with pure CSS (zero extra requests when the tone changes).
	variants := make(map[string][2]string, 8)
	for _, n := range names {
		if dark, light := s.icons.Variants(n); dark != "" {
			variants[n] = [2]string{dark, light}
		}
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"icons":    names,
		"warianty": variants,
		"total":    s.icons.Count(),
		"mode":     s.icons.Mode(),
	})
}

// ── history ──────────────────────────────────────────────────────────────────

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		jsonError(w, http.StatusBadRequest, "provide key")
		return
	}
	n := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 500 {
		n = v
	}
	h, err := s.st.History(r.Context(), key, n)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	availability, samples, _ := s.st.Availability(r.Context(), key, 24*time.Hour)

	type point struct {
		State string `json:"state"`
		MS    int64  `json:"ms"`
		At    string `json:"at"`
	}
	points := make([]point, 0, len(h))
	for _, w := range h {
		points = append(points, point{State: w.State, MS: w.MS, At: w.At.Format(time.RFC3339)})
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"key":     key,
		"points":  points,
		"uptime":  availability,
		"samples": samples,
	})
}

// ── accounts ─────────────────────────────────────────────────────────────────

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.Users(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	out := make([]u, 0, len(list))
	for _, x := range list {
		out = append(out, u{ID: x.ID, Login: x.Login})
	}
	jsonReply(w, http.StatusOK, map[string]any{"users": out, "me": UserFromRequest(r).Login})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if _, err := s.st.AddUser(r.Context(), strings.TrimSpace(req.Login), req.Password); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonReply(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	me := UserFromRequest(r)
	if me != nil && me.ID == id {
		jsonError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	list, _ := s.st.Users(r.Context())
	if len(list) <= 1 {
		jsonError(w, http.StatusBadRequest, "this is the last account — the panel would be left without access")
		return
	}
	if err := s.st.RemoveUser(r.Context(), id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonReply(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	me := UserFromRequest(r)
	if me == nil {
		jsonError(w, http.StatusUnauthorized, "no session")
		return
	}
	if _, err := s.st.Authenticate(r.Context(), me.Login, req.Old); err != nil {
		jsonError(w, http.StatusForbidden, "the current password does not match")
		return
	}
	if err := s.st.ChangePassword(r.Context(), me.ID, req.New); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// ! A password change must kick out the rest of the sessions. Someone with a stolen
	// cookie would otherwise stay in the panel for the full 30 days. The current
	// session stays.
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.st.RemoveOtherSessions(r.Context(), me.ID, c.Value); err != nil {
			slog.Error("ending other sessions after password change", "err", err)
		}
	}
	jsonReply(w, http.StatusOK, map[string]any{"ok": true})
}

// ── config change history ────────────────────────────────────────────────────

func (s *Server) handleVersionsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.Versions(r.Context(), 30)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type entry struct {
		ID          int64  `json:"id"`
		At          string `json:"at"`
		Author      string `json:"author"`
		Description string `json:"summary"`
		Added       int    `json:"added"`
		Removed     int    `json:"removed"`
	}
	out := make([]entry, 0, len(list))
	for _, w := range list {
		d, u := config.SummarizeDiff(w.Diff)
		out = append(out, entry{ID: w.ID, At: w.At.Format(time.RFC3339),
			Author: w.Author, Description: w.Description, Added: d, Removed: u})
	}
	jsonReply(w, http.StatusOK, map[string]any{"versions": out, "git": gitrepo.InRepo(r.Context(), s.path)})
}

func (s *Server) handleVersionOne(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	v, err := s.st.Version(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"id": v.ID, "at": v.At.Format(time.RFC3339), "author": v.Author,
		"summary": v.Description, "diff": v.Diff, "yaml": v.Content,
	})
}

// handleVersionRestore restores the file to the state from the given version. The
// restore itself also goes into the history, so undoing the undo is possible.
func (s *Server) handleVersionRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	v, err := s.st.Version(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if _, err := config.Load([]byte(v.Content)); err != nil {
		jsonError(w, http.StatusBadRequest, "this version does not parse: "+err.Error())
		return
	}
	description := fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_przywrocono"), v.ID, v.Description)
	if s.withEditOpen(w, r, description, nil, config.OpenRaw,
		func(d *config.Document) error { return d.ReplaceAll([]byte(v.Content)) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── service details ──────────────────────────────────────────────────────────

// handleService gathers everything we know about one service: current state,
// availability in two windows, response times, chart and state-change moments.
func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	serviceKey := r.URL.Query().Get("key")
	if serviceKey == "" {
		jsonError(w, http.StatusBadRequest, "provide key")
		return
	}

	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	var svc *config.Service
	var group string
	if cfg != nil {
		for _, g := range cfg.Groups {
			for i := range g.Services {
				if key(g.Name, g.Services[i].Name) == serviceKey {
					svc = &g.Services[i]
					group = g.Name
				}
			}
		}
	}
	if svc == nil {
		jsonError(w, http.StatusNotFound, "no such service")
		return
	}

	n := 120
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 1000 {
		n = v
	}
	history, err := s.st.History(r.Context(), serviceKey, n)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	day, _ := s.st.Stats(r.Context(), serviceKey, 24*time.Hour)
	week, _ := s.st.Stats(r.Context(), serviceKey, 7*24*time.Hour)
	changes, _ := s.st.Changes(r.Context(), serviceKey, 12)

	type point struct {
		State string `json:"state"`
		MS    int64  `json:"ms"`
		At    string `json:"at"`
		Code  int    `json:"code,omitempty"`
		Error string `json:"err,omitempty"`
	}
	points := make([]point, 0, len(history))
	for _, h := range history {
		points = append(points, point{State: h.State, MS: h.MS, At: h.At.Format(time.RFC3339),
			Code: h.Code, Error: h.Err})
	}

	type change struct {
		At    string `json:"at"`
		From  string `json:"from"`
		To    string `json:"to"`
		Error string `json:"err,omitempty"`
		// Code lets us show the reason "HTTP 502" instead of just the error text
		Code int `json:"code,omitempty"`
	}
	changeList := make([]change, 0, len(changes))
	for _, z := range changes {
		changeList = append(changeList, change{At: z.At.Format(time.RFC3339), From: z.From, To: z.To,
			Error: z.Err, Code: z.Code})
	}

	statsJSON := func(x store.Statistics) map[string]any {
		return map[string]any{"samples": x.Samples, "uptime": x.Availability,
			"p50": x.P50, "p95": x.P95, "max": x.Worst}
	}

	icon := s.resolveIcon(svc.Icon, svc.Name)
	jsonReply(w, http.StatusOK, map[string]any{
		"key": serviceKey, "name": svc.Name, "group": group, "url": svc.URL, "icon": svc.Icon,
		"iconSrc": icon.Src, "iconWariantCiemny": icon.WariantCiemny, "iconWariantJasny": icon.WariantJasny,
		"size": svc.Size, "description": svc.Description, "tags": tagsToJSON(svc.Tags),
		"interval": svc.Check.Interval.String(), "timeout": svc.Check.Timeout.String(),
		"expect": svc.Check.Expect, "insecure": svc.Check.Insecure,
		"path": svc.Check.Path, "contains": svc.Check.Contains, "notContains": svc.Check.NotContains,
		"now":     s.monitor.Snapshot()[serviceKey],
		"day":     statsJSON(day),
		"week":    statsJSON(week),
		"points":  points,
		"changes": changeList,
	})
}

// handleCheckNow checks a service immediately, without waiting for the schedule.
func (s *Server) handleCheckNow(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		jsonError(w, http.StatusBadRequest, "provide key")
		return
	}
	res, err := s.monitor.CheckNow(r.Context(), key)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	// result straight into the history, so the chart shows it
	_ = s.st.SaveResult(r.Context(), store.Result{
		Key: key, State: string(res.State), Code: res.Code,
		MS: res.MS, Err: res.Err, At: res.At,
	})
	jsonReply(w, http.StatusOK, res)
}

// handleNotifyTest sends a trial message so the webhook can be checked without
// waiting for something to really go down.
// webhookMask keeps the scheme and host, and replaces the rest with dots.
// The user is meant to recognize whether it is that address — not read it off the
// screen or out of the API response.
func webhookMask(address string) string {
	if address == "" {
		return ""
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "•••"
	}
	return u.Scheme + "://" + u.Host + "/•••"
}

func (s *Server) webhookSource() string {
	switch {
	case s.webhookFromEnv:
		return "srodowisko"
	case s.notifier.Webhook() != "":
		return "baza"
	default:
		return "brak"
	}
}

func (s *Server) handleNotifyGet(w http.ResponseWriter, r *http.Request) {
	formats := make([]string, len(notify.Formats))
	for i, f := range notify.Formats {
		formats[i] = string(f)
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"wlaczony":     s.notifier.Enabled(),
		"webhook":      webhookMask(s.notifier.Webhook()),
		"zrodlo":       s.webhookSource(),
		"format":       string(s.notifier.Format()),
		"formatZrodlo": s.formatSource(r.Context()),
		"formaty":      formats,
		"prog":         s.notifier.Threshold(),
	})
}

func (s *Server) formatSource(ctx context.Context) string {
	if s.formatFromEnv {
		return "srodowisko"
	}
	if v, err := s.st.Setting(ctx, "notify.format"); err == nil && v != "" {
		return "baza"
	}
	return "domyslny"
}

// error text key for an address that does not match the format
func formatAddressError(err error) string {
	switch {
	case errors.Is(err, notify.ErrTelegramAddress):
		return "webhook_adres_telegram"
	case errors.Is(err, notify.ErrGotifyAddress):
		return "webhook_adres_gotify"
	}
	return "webhook_zly_adres"
}

// handleNotifyPut writes to TWO layers at once: address and format to SQLite (the
// address is a secret, and the config file is committed), threshold to the YAML
// (because it is not). This is the only place where the layer split leaks into the
// API — the alternative was two routes and handling a half-failure on the UI side.
//
// First validate EVERYTHING, then save: a rejected request does not leave half of
// the changes behind.
func (s *Server) handleNotifyPut(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	var req struct {
		Webhook   *string `json:"webhook"`
		Format    *string `json:"format"`
		Threshold *int    `json:"prog"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}

	if req.Webhook != nil && s.webhookFromEnv {
		jsonError(w, http.StatusConflict, i18n.T(lang, "webhook_ze_srodowiska"))
		return
	}
	if req.Format != nil {
		if s.formatFromEnv {
			jsonError(w, http.StatusConflict, i18n.T(lang, "webhook_format_ze_srodowiska"))
			return
		}
		if !notify.ValidFormat(*req.Format) {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "webhook_zly_format"))
			return
		}
	}
	address := ""
	if req.Webhook != nil {
		address = strings.TrimSpace(*req.Webhook)
		if address != "" {
			// ! https only, in every format. The secret travels in the address (path or
			// query), so over http it would go in clear text. An operator who really wants
			// http on a LAN (ntfy, Gotify) sets the address through SHYG_WEBHOOK — there the
			// choice belongs to the deploy, not to a click.
			u, err := url.Parse(address)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				jsonError(w, http.StatusBadRequest, i18n.T(lang, "webhook_zly_adres"))
				return
			}
		}
	}
	if req.Threshold != nil && (*req.Threshold < 1 || *req.Threshold > 10) {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "webhook_zly_prog"))
		return
	}
	// the address must match the format — we check the pair in the state it will be in
	// after the save, even when only one of the two changes
	if req.Webhook != nil || req.Format != nil {
		format := s.notifier.Format()
		if req.Format != nil {
			format = notify.Format(*req.Format)
		}
		effective := s.notifier.Webhook()
		if req.Webhook != nil {
			effective = address
		}
		if err := notify.ValidateAddress(format, effective); err != nil {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, formatAddressError(err)))
			return
		}
	}

	if req.Webhook != nil {
		if err := s.st.SetSetting(r.Context(), "notify.webhook", address); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.notifier.SetWebhook(address)
	}
	if req.Format != nil {
		newValue := *req.Format
		if newValue == string(notify.FormatDiscord) {
			newValue = "" // default is not in the DB
		}
		if err := s.st.SetSetting(r.Context(), "notify.format", newValue); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.notifier.SetFormat(notify.Format(*req.Format))
	}

	if req.Threshold != nil {
		if !s.withEdit(w, r, "próg powiadomień", func(d *config.Document) error {
			return d.SetNotifyThreshold(*req.Threshold)
		}) {
			return
		}
		s.notifier.SetThreshold(*req.Threshold)
	}

	s.handleNotifyGet(w, r)
}

// handleInstance lists the start-up options — read-only.
func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, http.StatusOK, map[string]any{"opcje": s.instance})
}

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if !s.notifier.Enabled() {
		jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), "webhook_zrodlo_brak"))
		return
	}
	if err := s.notifier.Test(r.Context()); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	jsonReply(w, http.StatusOK, map[string]any{"ok": true})
}

// handleInfo tells the UI what is enabled in this instance.
//
// ! "docker" was NOT returned here, so app.js never showed the "Find services"
// button — the whole Docker suggestion feature was unavailable from the UI. This
// is a fix for a live bug, not a new feature.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"version": Version,
		"notify":  s.notifier.Enabled(),
		"git":     gitrepo.InRepo(r.Context(), s.path),
		"icons":   map[string]any{"mode": s.icons.Mode(), "total": s.icons.Count()},
		"config":  s.path,
		"docker":  s.dockerAvailable(),
		// Disabled in the file is not the same as a missing socket: the user chose the
		// first one themselves and the entry should disappear, the second is the state of
		// the environment and the entry should fade, so it is visible that it exists at all.
		"dockerWylaczony": !s.dockerEnabled(),
		"dockerNew":       s.newInDockerCount(r.Context(), host),
		"backgrounds":     s.backgrounds.Available(),
		"appearance":      s.instanceAppearance().toAPI(),
	})
}

// newInDockerCount counts containers outside the config. The result lives for a
// minute, because /api/info is called on every panel open, and the Docker socket
// does not need to look at that every time.
func (s *Server) newInDockerCount(ctx context.Context, host string) int {
	if !s.dockerAvailable() {
		return 0
	}
	s.muDocker.Lock()
	defer s.muDocker.Unlock()
	if time.Since(s.dockerAt) < time.Minute {
		return s.dockerCount
	}
	s.dockerAt = time.Now()

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	candidates, err := s.docker.Candidates(ctx, host)
	if err != nil {
		return s.dockerCount
	}

	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	have := map[string]bool{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			for _, svc := range g.Services {
				have[strings.ToLower(svc.Name)] = true
				if svc.URL != "" {
					have[strings.ToLower(svc.URL)] = true
				}
			}
		}
	}
	new := 0
	for _, k := range candidates {
		if !have[strings.ToLower(k.Name)] && !(k.URL != "" && have[strings.ToLower(k.URL)]) {
			new++
		}
	}
	s.dockerCount = new
	return new
}

// ── appearance ───────────────────────────────────────────────────────────────

func (s *Server) handleAppearanceGet(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, http.StatusOK, s.instanceAppearance().toAPI())
}

// appearanceRequest — pointers only, because we must tell "do not touch this
// field" from "set it to empty". Missing field = that YAML line stays untouched.
type appearanceRequest struct {
	Preset *string `json:"preset"`
	Image  *string `json:"image"`
	Blur   *int    `json:"blur"`
	Dim    *int    `json:"dim"`
	Tone   *string `json:"tone"`
	View   *string `json:"view"`
}

// handleAppearancePut saves the instance appearance to the YAML.
//
// It goes the same way as every other edit — withEdit, i.e. diff preview, a
// version in SQLite and a git commit. Uploading the wallpaper file alone changes
// nothing in the config; only this moment does.
//
// ! Validation here is STRICTER than in Load: anything that does not fit ends in
// 400, not a silent straightening. The user is meant to learn they made a
// mistake, instead of getting a saved value they did not ask for.
func (s *Server) handleAppearancePut(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	var req appearanceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}

	current := s.instanceAppearance()
	fields := map[string]string{}
	var changes []string

	targetPreset, targetImage := current.Preset, current.Image
	if req.Preset != nil {
		if !config.Allowed(*req.Preset, config.BackgroundPresets) {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		targetPreset = *req.Preset
		fields["preset"] = *req.Preset
		changes = append(changes, "preset="+*req.Preset)
	}
	if req.Tone != nil {
		if !config.Allowed(*req.Tone, config.BackgroundTones) {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		fields["tone"] = *req.Tone
		changes = append(changes, "tone="+*req.Tone)
	}
	if req.View != nil {
		if !config.Allowed(*req.View, config.Views) {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		fields["view"] = *req.View
		changes = append(changes, "view="+*req.View)
	}
	if req.Blur != nil {
		if *req.Blur < 0 || *req.Blur > config.MaxBlur {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		fields["blur"] = strconv.Itoa(*req.Blur)
		changes = append(changes, "blur="+fields["blur"])
	}
	if req.Dim != nil {
		if *req.Dim < 0 || *req.Dim > config.MaxDim {
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		fields["dim"] = strconv.Itoa(*req.Dim)
		changes = append(changes, "dim="+fields["dim"])
	}
	if req.Image != nil {
		image := *req.Image
		switch {
		case image == "":
			// clearing the reference — valid for every preset
		case !config.BackgroundName.MatchString(image), targetPreset != config.PresetPhoto:
			jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		case !s.backgrounds.Exists(image):
			jsonError(w, http.StatusNotFound, i18n.T(lang, "blad_wygladu"))
			return
		}
		targetImage = image
		fields["image"] = image
		changes = append(changes, "image="+image)
	}
	if targetPreset == config.PresetPhoto && targetImage == "" {
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
		return
	}
	if len(fields) == 0 {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	description := fmt.Sprintf(i18n.T(lang, "opis_zmieniono_wyglad"), strings.Join(changes, ", "))
	if s.withEdit(w, r, description, func(d *config.Document) error { return d.SetAppearance(fields) }) {
		jsonReply(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── Docker service suggestions ───────────────────────────────────────────────

// handleDiscover proposes entries based on the running containers.
// It saves nothing — the user decides what to accept.
// dockerEnabled — a missing `discovery.docker` key means "as before", i.e.
// suggest if the socket is there. Only an explicit `false` turns it off.
func (s *Server) dockerEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil || s.cfg.Discovery.Docker == nil {
		return true
	}
	return *s.cfg.Discovery.Docker
}

// dockerAvailable combines both conditions: the socket must be there, and the user
// must not have disabled it in the file.
func (s *Server) dockerAvailable() bool {
	return s.docker != nil && s.docker.Available() && s.dockerEnabled()
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	// ! Two different reasons for refusal and two different codes. 403 means "you
	// disabled it yourself", 404 "there is no socket" — mixing them forces the user to
	// guess whether to look for the error in the file or in the volume.
	if !s.dockerEnabled() {
		jsonError(w, http.StatusForbidden, i18n.T(i18n.FromRequest(r), "docker_wylaczony"))
		return
	}
	if s.docker == nil || !s.docker.Available() {
		jsonError(w, http.StatusNotFound, "Docker socket unavailable")
		return
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if h := r.URL.Query().Get("host"); h != "" {
		host = h
	}

	candidates, err := s.docker.Candidates(r.Context(), host)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}

	// drop what is already in the config — by name or by address
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	have := map[string]bool{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			for _, svc := range g.Services {
				have[strings.ToLower(svc.Name)] = true
				if svc.URL != "" {
					have[strings.ToLower(svc.URL)] = true
				}
			}
		}
	}
	type proposal struct {
		dockerdisc.Candidate
		Known bool `json:"known"`
	}
	out := make([]proposal, 0, len(candidates))
	for _, k := range candidates {
		// Docker knows only the image, and that can be misleading: a "jellyfin" container
		// from the nginx image would get the nginx icon. If the catalog has an icon named
		// after the service, it is a better match than the one from the image.
		if n := strings.ToLower(k.Name); n != k.Icon && s.icons.Has(n) {
			k.Icon = n
		}
		out = append(out, proposal{
			Candidate: k,
			Known:     have[strings.ToLower(k.Name)] || (k.URL != "" && have[strings.ToLower(k.URL)]),
		})
	}

	groups := []string{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			groups = append(groups, g.Name)
		}
	}
	jsonReply(w, http.StatusOK, map[string]any{"candidates": out, "groups": groups, "host": host})
}

// handleServicesBulk adds many services at once — the whole batch goes as a single
// file change, so the user sees one diff and gets one commit.
func (s *Server) handleServicesBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Group    string           `json:"group"`
		Services []serviceRequest `json:"services"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if len(req.Services) == 0 {
		jsonError(w, http.StatusBadRequest, "empty list")
		return
	}
	for _, u := range req.Services {
		if !config.ValidServiceAddress(u.URL) {
			jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), "blad_adres_uslugi"))
			return
		}
		if _, _, err := u.descriptionAndTags(); err != "" {
			jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), err))
			return
		}
		if _, key := u.parseCheck(); key != "" {
			jsonError(w, http.StatusBadRequest, i18n.T(i18n.FromRequest(r), key))
			return
		}
	}

	description := fmt.Sprintf(i18n.T(i18n.FromRequest(r), "opis_dodano_wiele"), len(req.Services), req.Group)
	ok := s.withEdit(w, r, description, func(d *config.Document) error {
		for _, u := range req.Services {
			group := u.Group
			if group == "" {
				group = req.Group
			}
			svc := config.Service{Name: u.Name, URL: u.URL, Icon: u.Icon,
				Size: sizeFromRequest(u.Size)}
			description, tags, _ := u.descriptionAndTags() // errors checked above
			if description != nil {
				svc.Description = *description
			}
			if tags != nil {
				svc.Tags = config.Tags(*tags)
			}
			svc.Check, _ = u.parseCheck() // checked above
			if err := d.AddService(group, svc); err != nil {
				return fmt.Errorf("%s: %w", u.Name, err)
			}
		}
		return nil
	})
	if ok {
		jsonReply(w, http.StatusCreated, map[string]any{"ok": true, "added": len(req.Services)})
	}
}
