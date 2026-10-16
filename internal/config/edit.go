package config

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
)

// findGroup returns the index of the group with the given name.
func (d *Document) findGroup(group string) (int, error) {
	groups, err := d.node("groups")
	if err != nil {
		return 0, err
	}
	seq, ok := unwrap(groups).(*ast.SequenceNode)
	if !ok {
		return 0, fmt.Errorf("%w: groups is not a list", ErrPathNotFound)
	}
	for i, g := range seq.Values {
		if scalar(child(g, "name")) == group {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrGroupNotFound, group)
}

// findService returns the group and service indexes for the given names.
func (d *Document) findService(group, service string) (int, int, error) {
	gi, err := d.findGroup(group)
	if err != nil {
		return 0, 0, err
	}
	svcs, err := d.node("groups", gi, "services")
	if err != nil {
		return 0, 0, fmt.Errorf("%w: group %q has no services", ErrServiceNotFound, group)
	}
	seq, ok := unwrap(svcs).(*ast.SequenceNode)
	if !ok {
		return 0, 0, fmt.Errorf("%w: services is not a list", ErrPathNotFound)
	}
	for i, s := range seq.Values {
		if scalar(child(s, "name")) == service {
			return gi, i, nil
		}
	}
	return 0, 0, fmt.Errorf("%w: %q in group %q", ErrServiceNotFound, service, group)
}

// blockEnd returns the offset right after the last line with content belonging to
// the list element starting on the line of offset off. Trailing blank lines of the
// block are not pulled in — a new element is inserted before them, not after.
func (d *Document) blockEnd(off, dashIndent int) int {
	end := d.lineEnd(off) + 1 // after the '\n' of the '-' line
	if end > len(d.src) {
		return len(d.src)
	}
	last := end
	for i := end; i < len(d.src); {
		lineEnd := d.lineEnd(i)
		line := string(d.src[i:lineEnd])
		if strings.TrimSpace(line) == "" {
			i = lineEnd + 1
			continue
		}
		if d.lineIndent(i) <= dashIndent {
			break
		}
		i = lineEnd + 1
		last = i
	}
	if last > len(d.src) {
		last = len(d.src)
	}
	return last
}

// entryRange returns the key's column at the offset and the indent threshold after which
// its block ends — i.e. the second argument for blockEnd.
//
// ! Two different cases, hence a separate function. A plain map key (`view:`) has
// siblings at the SAME indent, so the block ends at them. A key standing
// after a dash (`- name:`) has a column two greater than the line's indent, and its
// siblings (`url:`, `icon:`) still belong to the same entry.
func (d *Document) entryRange(off int) (column, threshold int) {
	column = off - d.lineStart(off)
	threshold = column
	if d.lineIndent(off) < column {
		threshold = column - 1
	}
	return column, threshold
}

// dashIndentOf returns the indent of the dash of the list element on whose line off lies.
func (d *Document) dashIndentOf(off int) int {
	lines := d.lineStart(off)
	for i := lines; i < len(d.src) && i < d.lineEnd(off); i++ {
		if d.src[i] == '-' {
			return i - lines
		}
	}
	return d.lineIndent(off)
}

// precededByBlankLine says whether the line before the one containing off is blank.
// Used to mimic the file's style: if the user separates services with a blank line,
// the added service should be separated too.
func (d *Document) precededByBlankLine(off int) bool {
	lines := d.lineStart(off)
	if lines == 0 {
		return false
	}
	prevEnd := lines - 1
	prevStart := d.lineStart(prevEnd)
	return strings.TrimSpace(string(d.src[prevStart:prevEnd])) == ""
}

// renderService builds the service's YAML block with the given dash indent.
func renderService(svc Service, dashIndent int) string {
	ind := strings.Repeat(" ", dashIndent)
	key := ind + "  "

	var b strings.Builder
	fmt.Fprintf(&b, "%s- name: %s\n", ind, quoteLike(nil, svc.Name))
	if svc.URL != "" {
		fmt.Fprintf(&b, "%surl: %s\n", key, quoteLike(nil, svc.URL))
	}
	if svc.Icon != "" {
		fmt.Fprintf(&b, "%sicon: %s\n", key, quoteLike(nil, svc.Icon))
	}
	if svc.Size != "" {
		fmt.Fprintf(&b, "%ssize: %s\n", key, quoteLike(nil, svc.Size))
	}
	if svc.Description != "" {
		fmt.Fprintf(&b, "%sdescription: %s\n", key, quoteLike(nil, svc.Description))
	}
	if len(svc.Tags) > 0 {
		fmt.Fprintf(&b, "%stags: %s\n", key, renderTagsFlow(svc.Tags))
	}
	b.WriteString(renderCheck(svc.Check, len(key)))
	return b.String()
}

// AddService appends a service to the end of the group. The indent is taken from the last service
// in that group, not from assumptions — the user's file dictates the style.
func (d *Document) AddService(group string, svc Service) error {
	prev := d.src
	if err := d.addService(group, svc); err != nil {
		return err
	}
	return d.verifyLoads(prev, func(cfg *Config) bool {
		return findLoaded(cfg, group, svc.Name, func(Service) bool { return true })
	})
}

func (d *Document) addService(group string, svc Service) error {
	if svc.Name == "" {
		return errors.New("service name is required")
	}
	gi, err := d.findGroup(group)
	if err != nil {
		return err
	}
	if _, _, err := d.findService(group, svc.Name); err == nil {
		return fmt.Errorf("service %q already exists in group %q", svc.Name, group)
	}

	svcs, err := d.node("groups", gi, "services")
	if err != nil {
		return d.addServiceToEmptyGroup(gi, svc)
	}
	seq, ok := unwrap(svcs).(*ast.SequenceNode)
	if !ok || len(seq.Values) == 0 {
		return d.addServiceToEmptyGroup(gi, svc)
	}

	last := seq.Values[len(seq.Values)-1]
	off, err := d.offsetOf(last)
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	insert := d.blockEnd(off, dashIndent)

	block := renderService(svc, dashIndent)
	if d.precededByBlankLine(off) {
		block = "\n" + block
	}
	return d.replaceRange(insert, insert, []byte(block))
}

// addServiceToEmptyGroup handles two cases: the group has no `services`
// key yet, or has it with an empty list (`services: []`). In the second
// case we replace that line, because adding a second key would give
// "mapping key already defined".
func (d *Document) addServiceToEmptyGroup(gi int, svc Service) error {
	nameNode, err := d.node("groups", gi, "name")
	if err != nil {
		return err
	}
	off, err := d.offsetOf(nameNode)
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	keyIndent := dashIndent + 2

	g, err := d.node("groups", gi)
	if err != nil {
		return err
	}
	if keyOff, err := d.keyOffset(g, "services"); err == nil {
		block := strings.Repeat(" ", d.lineIndent(keyOff)) + "services:\n" +
			renderService(svc, d.lineIndent(keyOff)+2)
		// no trailing \n — replace the line, do not add a new one
		return d.replaceRange(d.lineStart(keyOff), d.lineEnd(keyOff), []byte(strings.TrimSuffix(block, "\n")))
	}

	insert := d.blockEnd(off, dashIndent)
	block := strings.Repeat(" ", keyIndent) + "services:\n" + renderService(svc, keyIndent+2)
	return d.replaceRange(insert, insert, []byte(block))
}

// SetServiceField changes one service field, replacing only the bytes of that
// value. The same-line comment, quoting style and indent stay.
//
// When the service does not have that field yet (`icon`, `size` — both optional),
// the key is appended to the end of its block. Without it the first setting of the
// icon or size from the UI silently vanished.
func (d *Document) SetServiceField(group, service, field, value string) error {
	prev := d.src
	if err := d.setServiceField(group, service, field, value); err != nil {
		return err
	}
	if field == "name" {
		return d.verifyRename(prev, group, service, value)
	}
	name, ok := service, func(Service) bool { return true }
	switch field {
	case "url":
		ok = func(s Service) bool { return s.URL == value }
	case "icon":
		ok = func(s Service) bool { return s.Icon == value }
	}
	return d.verifyService(prev, group, name, ok)
}

func (d *Document) setServiceField(group, service, field, value string) error {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	if field == "name" && value != service {
		if _, _, err := d.findService(group, value); err == nil {
			return fmt.Errorf("%w: service %q in group %q", ErrDuplicate, value, group)
		}
	}
	start, end, err := d.valueRange("groups", gi, "services", si, field)
	if errors.Is(err, ErrPathNotFound) {
		return d.insertKey(field, quoteLike(nil, value), "groups", gi, "services", si)
	}
	if err != nil {
		return err
	}
	repl := quoteLike(d.src[start:end], value)
	return d.replaceRange(start, end, []byte(repl))
}

// RemoveService removes the service together with its header comment (the one
// standing directly above the entry). A comment separated by a blank line belongs to the
// file, not the service, and stays.
func (d *Document) RemoveService(group, service string) error {
	prev := d.src
	if err := d.removeService(group, service); err != nil {
		return err
	}
	return d.verifyLoads(prev, func(*Config) bool { return true })
}

func (d *Document) removeService(group, service string) error {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	svcs, err := d.node("groups", gi, "services")
	if err != nil {
		return err
	}
	seq, ok := unwrap(svcs).(*ast.SequenceNode)
	if !ok {
		return fmt.Errorf("%w: services is not a list", ErrPathNotFound)
	}

	off, err := d.offsetOf(seq.Values[si])
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	start := d.startWithComment(off, dashIndent) // only a comment in the dash column
	end := d.blockEnd(off, dashIndent)

	// if removing the last entry, leave an empty list instead of a dangling key
	if len(seq.Values) == 1 {
		keyOff, err := d.offsetOf(seq.Values[0])
		if err == nil {
			_ = keyOff
		}
		if err := d.replaceRange(start, end, nil); err != nil {
			return err
		}
		return d.setEmptyList(group)
	}

	// eat the blank line left after the entry, so as not to leave gaps
	for end < len(d.src) {
		eol := d.lineEnd(end)
		if strings.TrimSpace(string(d.src[end:eol])) != "" {
			break
		}
		end = eol + 1
	}
	return d.replaceRange(start, end, nil)
}

// setEmptyList turns an orphaned `services:` key into `services: []`.
func (d *Document) setEmptyList(group string) error {
	gi, err := d.findGroup(group)
	if err != nil {
		return err
	}
	g, err := d.node("groups", gi)
	if err != nil {
		return err
	}
	off, err := d.keyOffset(g, "services")
	if err != nil {
		return nil // nothing to orphan
	}
	end := d.lineEnd(off)
	if strings.Contains(string(d.src[off:end]), "[]") {
		return nil
	}
	return d.replaceRange(off, end, []byte("services: []"))
}

// AddGroup appends a new, empty group to the end of the group list.
func (d *Document) AddGroup(name string) error {
	if name == "" {
		return errors.New("group name is required")
	}
	if _, err := d.findGroup(name); err == nil {
		return fmt.Errorf("group %q already exists", name)
	}
	groups, err := d.node("groups")
	if err != nil {
		return err
	}
	seq, ok := unwrap(groups).(*ast.SequenceNode)
	if !ok || len(seq.Values) == 0 {
		return fmt.Errorf("%w: no group list", ErrPathNotFound)
	}
	off, err := d.offsetOf(seq.Values[len(seq.Values)-1])
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	insert := d.blockEnd(off, dashIndent)
	ind := strings.Repeat(" ", dashIndent)
	block := fmt.Sprintf("%s- name: %s\n%s  services: []\n", ind, quoteLike(nil, name), ind)
	if d.precededByBlankLine(off) {
		block = "\n" + block
	}
	return d.replaceRange(insert, insert, []byte(block))
}

// RemoveGroup removes the whole group together with its services.
func (d *Document) RemoveGroup(name string) error {
	gi, err := d.findGroup(name)
	if err != nil {
		return err
	}
	groups, err := d.node("groups")
	if err != nil {
		return err
	}
	seq := unwrap(groups).(*ast.SequenceNode)
	off, err := d.offsetOf(seq.Values[gi])
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	start := d.startWithComment(off, dashIndent)
	end := d.blockEnd(off, dashIndent)
	for end < len(d.src) {
		keyLen := d.lineEnd(end)
		if strings.TrimSpace(string(d.src[end:keyLen])) != "" {
			break
		}
		end = keyLen + 1
	}
	return d.replaceRange(start, end, nil)
}

// ReplaceAll replaces the entire file content — used by the "raw YAML" editor.
// The change is rejected if the new content does not parse.
func (d *Document) ReplaceAll(newName []byte) error {
	// content with only CRLF (e.g. from a textarea) is written as CRLF; content with LF
	// gets the style the file had.
	newName, crlf := normalizeLineEndings(newName)
	if !crlf && d.crlf {
		// mixed endings in a CRLF file — Bytes() adds \r to every line,
		// so existing \r\n would yield \r\r\n
		newName = bytes.ReplaceAll(newName, []byte("\r\n"), []byte("\n"))
	}
	oldValue := d.crlf
	if crlf {
		d.crlf = true
	}
	if err := d.replaceRange(0, len(d.src), newName); err != nil {
		d.crlf = oldValue
		return err
	}
	return nil
}

// RenameGroup changes the group's name without touching its contents.
func (d *Document) RenameGroup(oldName, newName string) error {
	prev := d.src
	if err := d.renameGroup(oldName, newName); err != nil {
		return err
	}
	return d.verifyLoads(prev, func(cfg *Config) bool { return hasGroup(cfg, newName) })
}

func (d *Document) renameGroup(oldName, newName string) error {
	if newName == "" {
		return errors.New("group name is required")
	}
	if oldName == newName {
		return nil
	}
	if _, err := d.findGroup(newName); err == nil {
		return fmt.Errorf("%w: group %q", ErrDuplicate, newName)
	}
	gi, err := d.findGroup(oldName)
	if err != nil {
		return err
	}
	start, end, err := d.valueRange("groups", gi, "name")
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(quoteLike(d.src[start:end], newName)))
}

// MoveService moves the service to the end of another group together with its text block —
// comments and fields the panel does not know travel with it. The indent is
// recomputed for the target group. A specific position is given by MoveServiceTo.
func (d *Document) MoveService(fromGroup, service, toGroup string) error {
	if fromGroup == toGroup {
		return nil
	}
	titleIdx, err := d.findGroup(toGroup)
	if err != nil {
		return err
	}
	n := 0
	if seq, ok := d.sequence("groups", titleIdx, "services"); ok {
		n = len(seq.Values)
	}
	return d.MoveServiceTo(fromGroup, service, toGroup, n)
}

func (d *Document) insertBlockIntoEmpty(gi int, lines []string) error {
	nameNode, err := d.node("groups", gi, "name")
	if err != nil {
		return err
	}
	off, err := d.offsetOf(nameNode)
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	keyIndent := dashIndent + 2
	ind := strings.Repeat(" ", keyIndent+2)

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", keyIndent) + "services:\n")
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(ind + l + "\n")
	}

	g, err := d.node("groups", gi)
	if err != nil {
		return err
	}
	if keyOff, err := d.keyOffset(g, "services"); err == nil {
		return d.replaceRange(d.lineStart(keyOff), d.lineEnd(keyOff),
			[]byte(strings.TrimSuffix(b.String(), "\n")))
	}
	insert := d.blockEnd(off, dashIndent)
	return d.replaceRange(insert, insert, []byte(b.String()))
}

// ── single keys outside the service list ─────────────────────────────────────

// insertKey appends `key: value` to the end of the map under path. The indent
// is taken from a key already standing there — the file dictates the style, not us.
// the value must already be formatted (quoteLike or a validated number).
func (d *Document) insertKey(key, value string, path ...any) error {
	column, insert, err := d.insertionPoint(path...)
	if err != nil {
		return err
	}
	block := strings.Repeat(" ", column) + key + ": " + value + "\n"
	if insert > 0 && insert <= len(d.src) && d.src[insert-1] != '\n' {
		block = "\n" + block // file without a trailing \n — do not glue lines
	}
	return d.replaceRange(insert, insert, []byte(block))
}

// insertionPoint returns the column of the new key and the offset where
// its line should land — right after the last entry of the map under path.
func (d *Document) insertionPoint(path ...any) (column, insert int, err error) {
	n, err := d.node(path...)
	if err != nil {
		return 0, 0, err
	}
	m := asMapping(n)
	if m == nil || len(m.Values) == 0 {
		return 0, 0, fmt.Errorf("%w: %v is not a map with entries", ErrPathNotFound, path)
	}
	// from the end, because we look for the last entry — and incidentally skip
	// a node without a position, should one turn up (the parser is sparing with
	// special keys like `<<`)
	off := 0
	err = ErrPathNotFound
	for i := len(m.Values) - 1; i >= 0; i-- {
		if off, err = d.offsetOf(m.Values[i].Key); err == nil {
			break
		}
	}
	if err != nil {
		return 0, 0, err
	}
	column, threshold := d.entryRange(off)
	return column, d.blockEnd(off, threshold), nil
}

// removeKey deletes the line (or block) of the given key in the map under path.
// A missing key is not an error — we delete what is not there anyway.
// ! Not suitable for the FIRST key of a list element (`- name:`): it would take the
// dash along with it.
func (d *Document) removeKey(key string, path ...any) error {
	n, err := d.node(path...)
	if err != nil {
		return nil
	}
	off, err := d.keyOffset(n, key)
	if err != nil {
		return nil
	}
	_, threshold := d.entryRange(off)
	return d.replaceRange(d.lineStart(off), d.blockEnd(off, threshold), nil)
}

// SetPath changes the value under any path — the same as SetServiceField,
// just without looking up the service. The quoting style from the file wins.
func (d *Document) SetPath(value string, path ...any) error {
	start, end, err := d.valueRange(path...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(quoteLike(d.src[start:end], value)))
}

// rawValue — the only thing allowed to be written to the file without quoting.
var rawValue = regexp.MustCompile(`^(-?[0-9]{1,6}|true|false)$`)

// SetPathRaw writes the value literally, bypassing quoteLike.
//
// ! Exists solely for numbers and bools: needsQuoting would quote "10", because
// ParseFloat succeeds, and an `int` field on the other side will not
// load such a value. The allowlist is narrow on purpose — a raw user string has no
// way into the file through here.
func (d *Document) SetPathRaw(value string, path ...any) error {
	if !rawValue.MatchString(value) {
		return fmt.Errorf("raw value %q outside the allowlist (number or true/false)", value)
	}
	start, end, err := d.valueRange(path...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(value))
}

// ── appearance ───────────────────────────────────────────────────────────────

// appearanceKeys — fixed write order. Two identical changes must yield the same
// diff, so we do not iterate over a map.
var appearanceKeys = []string{"preset", "image", "blur", "dim", "tone", "view"}

// SetAppearance writes the given fields of the `appearance` block. A key that is not
// in the map is not touched at all — exactly as the service editor does not touch fields
// the user did not touch. An empty value deletes the line (this is how
// `image` goes when the background stops being a photo).
//
// When there is no `appearance` block at all, the whole of it is created in one splice.
func (d *Document) SetAppearance(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	if _, err := d.node("appearance"); err != nil {
		if !errors.Is(err, ErrPathNotFound) {
			return err
		}
		return d.AddAppearance(appearanceFromFields(fields))
	}
	for _, k := range appearanceKeys {
		v, ok := fields[k]
		if !ok {
			continue
		}
		if err := d.setAppearanceField(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (d *Document) setAppearanceField(key, value string) error {
	parent := []any{"appearance", "background"}
	if key == "view" {
		parent = []any{"appearance"}
	}
	path := append(append([]any{}, parent...), key)

	if value == "" {
		return d.removeKey(key, parent...)
	}

	var err error
	if key == "blur" || key == "dim" {
		err = d.SetPathRaw(value, path...)
	} else {
		err = d.SetPath(value, path...)
	}
	if !errors.Is(err, ErrPathNotFound) {
		return err
	}

	// the key is not there yet — append it to the map, if the map exists at all.
	// ! `appearance: <<: *something` yields a map without our keys and without room for
	// them; a missing path is then not a write error (we treat a missing
	// `icon` in the api the same way).
	if _, e := d.node(parent...); e != nil {
		return nil
	}
	formatted := quoteLike(nil, value)
	if key == "blur" || key == "dim" {
		if !rawValue.MatchString(value) {
			return fmt.Errorf("%s %q is not a number", key, value)
		}
		formatted = value
	}
	return d.insertKey(key, formatted, parent...)
}

func appearanceFromFields(fields map[string]string) Appearance {
	w := Appearance{
		Background: Background{Preset: DefaultPreset, Blur: DefaultBlur, Dim: DefaultDim, Tone: DefaultTone},
		View:       DefaultView,
	}
	if v := fields["preset"]; v != "" {
		w.Background.Preset = v
	}
	if v := fields["image"]; v != "" {
		w.Background.Image = v
	}
	if v, err := strconv.Atoi(fields["blur"]); err == nil {
		w.Background.Blur = v
	}
	if v, err := strconv.Atoi(fields["dim"]); err == nil {
		w.Background.Dim = v
	}
	if v := fields["tone"]; v != "" {
		w.Background.Tone = v
	}
	if v := fields["view"]; v != "" {
		w.View = v
	}
	return w
}

// renderAppearance builds the whole block. Empty fields are skipped — an empty value would give a
// NullNode, which navigation can no longer reach (we skip empty url
// and icon in renderService the same way).
func renderAppearance(w Appearance) string {
	var b strings.Builder
	b.WriteString("appearance:\n")
	b.WriteString("  background:\n")
	if w.Background.Preset != "" {
		fmt.Fprintf(&b, "    preset: %s\n", quoteLike(nil, w.Background.Preset))
	}
	if w.Background.Image != "" {
		fmt.Fprintf(&b, "    image: %s\n", quoteLike(nil, w.Background.Image))
	}
	fmt.Fprintf(&b, "    blur: %d\n", w.Background.Blur)
	fmt.Fprintf(&b, "    dim: %d\n", w.Background.Dim)
	if w.Background.Tone != "" {
		fmt.Fprintf(&b, "    tone: %s\n", quoteLike(nil, w.Background.Tone))
	}
	if w.View != "" {
		fmt.Fprintf(&b, "  view: %s\n", quoteLike(nil, w.View))
	}
	return b.String()
}

// AddAppearance inserts the whole `appearance` block when the file has none.
func (d *Document) AddAppearance(w Appearance) error {
	if _, err := d.node("appearance"); err == nil {
		return nil // already there — SetAppearance is for changes
	}
	return d.insertTopLevelBlock(renderAppearance(w))
}

// SetNotifyThreshold writes notify.threshold, adding the whole `notify` block
// when the file has nothing to correct yet.
//
// ! Without it the first threshold setting on a fresh install ended in a 400
// "path not found": SetPathRaw can replace a key, but cannot invent
// a parent that does not exist.
func (d *Document) SetNotifyThreshold(n int) error {
	err := d.SetPathRaw(strconv.Itoa(n), "notify", "threshold")
	if !errors.Is(err, ErrPathNotFound) {
		return err
	}
	if _, e := d.node("notify"); e == nil {
		// the block exists, only the key is missing
		return d.insertKey("threshold", strconv.Itoa(n), "notify")
	}
	return d.insertTopLevelBlock(fmt.Sprintf("notify:\n  threshold: %d\n", n))
}

// insertTopLevelBlock inserts a ready block BEFORE `groups:`.
//
// ! Not at the end of the file: both configs in the repo end with a footer comment in
// column 0, and an appended block would land under it and steal its meaning.
func (d *Document) insertTopLevelBlock(block string) error {
	if off, err := d.keyOffset(d.root(), "groups"); err == nil {
		insert := d.lineStart(off)
		// a comment glued above `groups:` describes the groups, not the appearance —
		// pull it upward so it stays with them (the RemoveService pattern)
		for insert > 0 {
			prevEnd := insert - 1
			prevStart := d.lineStart(prevEnd)
			if strings.HasPrefix(strings.TrimSpace(string(d.src[prevStart:prevEnd])), "#") {
				insert = prevStart
				continue
			}
			break
		}
		return d.replaceRange(insert, insert, []byte(block+"\n"))
	}

	// minimal file, without `groups:` — append at the end
	end := len(d.src)
	before := "\n"
	if end > 0 && d.src[end-1] != '\n' {
		before = "\n\n" // lineEnd returns the offset of '\n'; without this the block would glue to the line
	}
	return d.replaceRange(end, end, []byte(before+block))
}

// RemoveAppearance deletes the whole `appearance` block — to undo a change from the UI.
func (d *Document) RemoveAppearance() error {
	off, err := d.keyOffset(d.root(), "appearance")
	if err != nil {
		return err
	}
	start := d.lineStart(off)
	_, threshold := d.entryRange(off)
	end := d.blockEnd(off, threshold)
	for end < len(d.src) { // eat the blank line after the block, so as not to leave a gap
		eol := d.lineEnd(end)
		if strings.TrimSpace(string(d.src[end:eol])) != "" {
			break
		}
		end = eol + 1
	}
	return d.replaceRange(start, end, nil)
}

// MaxTitle — title length limit of the panel (in characters, not bytes).
const MaxTitle = 80

var (
	ErrTitleEmpty   = errors.New("title is empty")
	ErrTitleTooLong = errors.New("title is too long")
	ErrTitleChars   = errors.New("title contains control characters")
)

// NormalizeTitle trims and validates the title. Returns what may be written to
// the file — one line, without control characters.
func NormalizeTitle(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ErrTitleEmpty
	}
	if utf8.RuneCountInString(v) > MaxTitle {
		return "", ErrTitleTooLong
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", ErrTitleChars
		}
	}
	return v, nil
}

// SetTitle writes the top-level `title`. When the key is missing, it is created before
// `groups:`, like the appearance block. The quoting style from the file wins (SetPath).
func (d *Document) SetTitle(title string) error {
	title, err := NormalizeTitle(title)
	if err != nil {
		return err
	}
	err = d.SetPath(title, "title")
	if !errors.Is(err, ErrPathNotFound) {
		return err
	}
	return d.insertTopLevelBlock("title: " + quoteLike(nil, title) + "\n")
}
