package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
)

// Service description and tags (SHYG-47) — both fields optional.
//
// Limits are on the write side (API), not the read side: a hand-written file must
// always load, and CSS clips what does not fit anyway.
const (
	MaxDescription = 200 // characters, not bytes
	MaxTags        = 12
	MaxTag         = 32
)

var (
	ErrDescriptionTooLong = errors.New("description is too long")
	ErrDescriptionChars   = errors.New("description contains control characters")
	ErrTagsInvalid        = errors.New("invalid tags")

	// ErrInheritedField — the value comes from `<<: *anchor`, and a local key
	// next to the merge is a "duplicate key" for the parser. Changing it requires a manual edit.
	ErrInheritedField = errors.New("the value is inherited through a merge key (<<) — edit it in the file")
)

// Tags is the list of service labels. A separate type, because `tags: media` (a single string
// instead of a list) is such a natural notation that it must not be punished by a
// load error for the whole file.
type Tags []string

// UnmarshalYAML accepts a list or a single string.
func (t *Tags) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var list []string
	if err := unmarshal(&list); err == nil {
		*t = Tags(list)
		return nil
	}
	var single string
	if err := unmarshal(&single); err != nil {
		return err
	}
	*t = Tags{single}
	return nil
}

func controlChar(r rune) bool {
	return unicode.IsControl(r) || r == ' ' || r == ' '
}

// NormalizeDescription trims and validates the description. An empty result is valid and means
// "remove the description". A description is one line, without control characters.
func NormalizeDescription(v string) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > MaxDescription {
		return "", ErrDescriptionTooLong
	}
	for _, r := range v {
		if controlChar(r) {
			return "", ErrDescriptionChars
		}
	}
	// collapse runs of spaces the same way Load does, so what is written is
	// exactly what reads back
	return strings.Join(strings.Fields(v), " "), nil
}

// cleanTags removes empty and repeated tags (case-insensitive),
// preserving order. It validates nothing — see NormalizeTags.
func cleanTags(t Tags) Tags {
	var out Tags
	seen := map[string]bool{}
	for _, x := range t {
		x = strings.TrimSpace(x)
		k := strings.ToLower(x)
		if x == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}

// NormalizeTags cleans and validates tags sent for saving: up to MaxTags
// of them, each up to MaxTag characters and without control characters.
func NormalizeTags(t []string) ([]string, error) {
	out := cleanTags(Tags(t))
	if len(out) > MaxTags {
		return nil, fmt.Errorf("%w: at most %d", ErrTagsInvalid, MaxTags)
	}
	for _, x := range out {
		if utf8.RuneCountInString(x) > MaxTag {
			return nil, fmt.Errorf("%w: %q is longer than %d", ErrTagsInvalid, x, MaxTag)
		}
		for _, r := range x {
			if controlChar(r) {
				return nil, fmt.Errorf("%w: control characters", ErrTagsInvalid)
			}
		}
	}
	return []string(out), nil
}

// ── writing ──────────────────────────────────────────────────────────────────

// SetServiceDescription sets, replaces or (empty description) removes `description`.
// Quoting and the same-line comment stay, as with SetServiceField.
func (d *Document) SetServiceDescription(group, service, description string) error {
	description, err := NormalizeDescription(description)
	if err != nil {
		return err
	}
	hasDescription := func(s Service) bool { return s.Description != "" }
	if err := d.refuseIfInherited(group, service, "description", hasDescription); err != nil {
		return err
	}
	prev := d.src
	if description != "" {
		err = d.SetServiceField(group, service, "description", description)
	} else {
		err = d.removeServiceField(group, service, "description")
	}
	if err != nil {
		return err
	}
	return d.verifyService(prev, group, service, func(s Service) bool { return s.Description == description })
}

// SetServiceTags sets, replaces or (empty list) removes `tags`.
//
// Writing style: an existing key stays in its own style (flow `[a, b]` or
// block list); a new one gets the style in which the file already writes tags, and when there are
// none yet — flow.
func (d *Document) SetServiceTags(group, service string, tags []string) error {
	prev := d.src
	if err := d.setServiceTags(group, service, tags); err != nil {
		return err
	}
	want, _ := NormalizeTags(tags) // validated by setServiceTags
	return d.verifyService(prev, group, service, func(s Service) bool {
		return slices.Equal([]string(s.Tags), want)
	})
}

func (d *Document) setServiceTags(group, service string, tags []string) error {
	tags, err := NormalizeTags(tags)
	if err != nil {
		return err
	}
	hasTags := func(s Service) bool { return len(s.Tags) > 0 }
	if err := d.refuseIfInherited(group, service, "tags", hasTags); err != nil {
		return err
	}
	if len(tags) == 0 {
		return d.removeServiceField(group, service, "tags")
	}
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	path := []any{"groups", gi, "services", si}
	n, err := d.node(append(append([]any{}, path...), "tags")...)
	if errors.Is(err, ErrPathNotFound) {
		if d.fileWritesBlockTags() {
			return d.insertTagsAsBlock(tags, path...)
		}
		return d.insertKey("tags", renderTagsFlow(tags), path...)
	}
	if err != nil {
		return err
	}

	if seq, ok := unwrap(n).(*ast.SequenceNode); ok && !seq.IsFlowStyle && len(seq.Values) > 0 {
		return d.replaceBlockTags(seq, tags)
	}
	// flow, scalar (`tags: media`) or alias — the value fits in the key's line
	start, end, err := d.valueRange(append(append([]any{}, path...), "tags")...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(renderTagsFlow(tags)))
}

// refuseIfInherited returns ErrInheritedField when the service has no own
// key, yet the field has a value — i.e. it gets it from a merge key. Writing
// a local key next to the merge would produce a file the parser cannot load.
func (d *Document) refuseIfInherited(group, service, key string, hasValue func(Service) bool) error {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	if _, err := d.node("groups", gi, "services", si, key); err == nil {
		return nil // local key — the change concerns it
	}
	cfg, err := Load(d.src)
	if err != nil {
		return nil
	}
	for _, g := range cfg.Groups {
		if g.Name != group {
			continue
		}
		for _, s := range g.Services {
			if s.Name == service && hasValue(s) {
				return ErrInheritedField
			}
		}
	}
	return nil
}

func (d *Document) removeServiceField(group, service, key string) error {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	return d.removeEntryField(key, "groups", gi, "services", si)
}

// removeEntryField is removeKey, which also handles a key standing right after the
// dash (`- description: x`) — removeKey alone would then take the dash, and
// with it the whole list element.
func (d *Document) removeEntryField(key string, path ...any) error {
	n, err := d.node(path...)
	if err != nil {
		return nil
	}
	off, err := d.keyOffset(n, key)
	if err != nil {
		return nil // no local key — nothing to delete
	}
	column, _ := d.entryRange(off)
	if d.lineIndent(off) == column {
		return d.removeKey(key, path...)
	}

	// First key after the dash: we remove it together with its value and pull the
	// next key up under the dash.
	end := d.blockEnd(off, column)
	next := end
	for next < len(d.src) && strings.TrimSpace(string(d.src[next:d.lineEnd(next)])) == "" {
		next = d.lineEnd(next) + 1
	}
	if next >= len(d.src) || d.lineIndent(next) != column {
		return fmt.Errorf("cannot remove %q: it is the only key of its entry", key)
	}
	return d.replaceRange(off, next+column, nil)
}

// ── tags: helpers ────────────────────────────────────────────────────────────

// quoteFlowTag — inside a flow list a comma and brackets are syntax too.
func quoteFlowTag(v string) string {
	if strings.ContainsAny(v, ",[]{}") || needsQuoting(v) {
		return quoteWith('"', v)
	}
	return v
}

func renderTagsFlow(tags []string) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = quoteFlowTag(t)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func renderTagsBlock(tags []string, dashChar int) string {
	var b strings.Builder
	for _, t := range tags {
		b.WriteString(strings.Repeat(" ", dashChar) + "- " + quoteLike(nil, t) + "\n")
	}
	return b.String()
}

// fileWritesBlockTags: whether the first tag list found in the file is
// block. No tags in the file = flow.
func (d *Document) fileWritesBlockTags() bool {
	groupList, ok := d.sequence("groups")
	if !ok {
		return false
	}
	for gi := range groupList.Values {
		services, ok := d.sequence("groups", gi, "services")
		if !ok {
			continue
		}
		for si := range services.Values {
			n, err := d.node("groups", gi, "services", si, "tags")
			if err != nil {
				continue
			}
			if seq, ok := unwrap(n).(*ast.SequenceNode); ok {
				return !seq.IsFlowStyle
			}
		}
	}
	return false
}

func (d *Document) insertTagsAsBlock(tags []string, path ...any) error {
	column, insert, err := d.insertionPoint(path...)
	if err != nil {
		return err
	}
	block := strings.Repeat(" ", column) + "tags:\n" + renderTagsBlock(tags, column+2)
	if insert > 0 && insert <= len(d.src) && d.src[insert-1] != '\n' {
		block = "\n" + block
	}
	return d.replaceRange(insert, insert, []byte(block))
}

func (d *Document) replaceBlockTags(seq *ast.SequenceNode, tags []string) error {
	first, err := d.offsetOf(seq.Values[0])
	if err != nil {
		return err
	}
	last, err := d.offsetOf(seq.Values[len(seq.Values)-1])
	if err != nil {
		return err
	}
	dashChar := d.dashIndentOf(first)
	start := d.lineStart(first)
	end := d.blockEnd(last, d.dashIndentOf(last))
	return d.replaceRange(start, end, []byte(renderTagsBlock(tags, dashChar)))
}
