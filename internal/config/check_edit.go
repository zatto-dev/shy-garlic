package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// The "Checking" section in the service editor (GH #6, SHYG-51).

// CheckChange — fields of the `check` block sent from the service editor.
//
// nil means "leave alone". A zero value (0, empty list, false) means "go back
// to the default", i.e. remove the key from the file — default values are not written.
type CheckChange struct {
	Interval *Duration
	Timeout  *Duration
	Expect   *[]int
	Insecure *bool
}

// SetCheck writes the changed fields of the service's `check` block.
//
// It compares with what is in the file (without Load's defaults) and touches only
// the keys that actually change: replaces the value in place,
// appends a missing key or deletes it. Comments and the rest of the block stay.
// When there is no block — it is created; when it ends up empty after the change — it disappears.
//
// A `check` block inherited via `<<: *anchor` ends with ErrInheritedField:
// a local `check` next to the merge would replace the whole inherited block (the merge is
// shallow), not just the changed field.
func (d *Document) SetCheck(group, service string, z CheckChange) error {
	prev := d.src
	want, err := d.setCheck(group, service, z)
	if err != nil || want == nil {
		return err
	}
	return d.verifyService(prev, group, service, func(s Service) bool { return checkMatches(s.Check, *want) })
}

// setCheck applies the change and returns the check block it meant to write
// (nil when nothing changed).
func (d *Document) setCheck(group, service string, z CheckChange) (*Check, error) {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return nil, err
	}
	oldValue, err := d.rawCheck(gi, si)
	if err != nil {
		return nil, err
	}

	newValue := oldValue
	var changes []string // keys in write order
	if z.Interval != nil && *z.Interval != oldValue.Interval {
		newValue.Interval = *z.Interval
		changes = append(changes, "interval")
	}
	if z.Timeout != nil && *z.Timeout != oldValue.Timeout {
		newValue.Timeout = *z.Timeout
		changes = append(changes, "timeout")
	}
	if z.Expect != nil && !slices.Equal(*z.Expect, oldValue.Expect) {
		newValue.Expect = slices.Clone(*z.Expect)
		changes = append(changes, "expect")
	}
	if z.Insecure != nil && *z.Insecure != oldValue.Insecure {
		newValue.Insecure = *z.Insecure
		changes = append(changes, "insecure")
	}
	if len(changes) == 0 {
		return nil, nil
	}

	svc := []any{"groups", gi, "services", si}
	path := append(slices.Clone(svc), "check")
	n, err := d.node(path...)
	switch {
	case errors.Is(err, ErrPathNotFound):
		if !emptyCheck(oldValue) {
			return nil, ErrInheritedField
		}
		return &newValue, d.insertServiceBlock(renderCheck(newValue, 0), svc...)
	case err != nil:
		return nil, err
	}

	if !d.blockMapping(n, "check", svc...) {
		// `check: {interval: 1m}` or an empty `check:` — rewrite the whole block
		if err := d.removeEntryField("check", svc...); err != nil {
			return nil, err
		}
		if emptyCheck(newValue) {
			return &newValue, nil
		}
		return &newValue, d.insertServiceBlock(renderCheck(newValue, 0), svc...)
	}

	// writes first, then deletions — the block must not end up empty along the way
	for _, key := range changes {
		if value := checkValue(newValue, key); value != "" {
			if err := d.setCheckKey(key, value, path...); err != nil {
				return nil, err
			}
		}
	}
	for _, key := range changes {
		if checkValue(newValue, key) == "" {
			if err := d.removeEntryField(key, path...); err != nil {
				return nil, err
			}
		}
	}
	if n, err := d.node(path...); err == nil {
		if m := asMapping(n); m == nil || len(m.Values) == 0 {
			return &newValue, d.removeEntryField("check", svc...)
		}
	}
	return &newValue, nil
}

// rawCheck returns the service's `check` block as it stands in the file: with
// merge keys expanded, but WITHOUT the defaults that Load adds.
func (d *Document) rawCheck(gi, si int) (Check, error) {
	var cfg Config
	if err := safeUnmarshal(d.src, &cfg); err != nil {
		return Check{}, fmt.Errorf("parse config: %w", err)
	}
	if gi >= len(cfg.Groups) || si >= len(cfg.Groups[gi].Services) {
		return Check{}, ErrServiceNotFound
	}
	return cfg.Groups[gi].Services[si].Check, nil
}

func emptyCheck(c Check) bool {
	return c.Interval == 0 && c.Timeout == 0 && len(c.Expect) == 0 && !c.Insecure &&
		c.Path == "" && c.Contains == "" && c.NotContains == ""
}

// checkValue — the key's value ready to be written; "" = the key should not be there.
func checkValue(c Check, key string) string {
	switch key {
	case "interval":
		if c.Interval != 0 {
			return c.Interval.String()
		}
	case "timeout":
		if c.Timeout != 0 {
			return c.Timeout.String()
		}
	case "expect":
		if len(c.Expect) > 0 {
			return renderCodes(c.Expect)
		}
	case "insecure":
		if c.Insecure {
			return "true"
		}
	}
	return ""
}

func renderCodes(codes []int) string {
	s := make([]string, len(codes))
	for i, k := range codes {
		s[i] = strconv.Itoa(k)
	}
	return "[" + strings.Join(s, ", ") + "]"
}

// renderCheck — the `check:` block with non-zero fields, key indented by column.
func renderCheck(c Check, column int) string {
	if emptyCheck(c) {
		return ""
	}
	key := strings.Repeat(" ", column)
	var b strings.Builder
	fmt.Fprintf(&b, "%scheck:\n", key)
	for _, keyName := range []string{"interval", "timeout", "expect", "insecure"} {
		if v := checkValue(c, keyName); v != "" {
			fmt.Fprintf(&b, "%s  %s: %s\n", key, keyName, v)
		}
	}
	if c.Path != "" {
		fmt.Fprintf(&b, "%s  path: %s\n", key, quoteLike(nil, c.Path))
	}
	if c.Contains != "" {
		fmt.Fprintf(&b, "%s  contains: %s\n", key, quoteLike(nil, c.Contains))
	}
	if c.NotContains != "" {
		fmt.Fprintf(&b, "%s  not_contains: %s\n", key, quoteLike(nil, c.NotContains))
	}
	return b.String()
}

// blockMapping says whether the key's value is a block map with entries (not a flow
// `{...}` nor an empty value). We look at the source: right after `key:` on the key's line
// nothing but a comment may stand.
func (d *Document) blockMapping(n ast.Node, key string, path ...any) bool {
	if m := asMapping(n); m == nil || len(m.Values) == 0 {
		return false
	}
	parent, err := d.node(path...)
	if err != nil {
		return false
	}
	off, err := d.keyOffset(parent, key)
	if err != nil {
		return false
	}
	rest := string(d.src[off+len(key) : d.lineEnd(off)])
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":"))
	return rest == "" || strings.HasPrefix(rest, "#")
}

// setCheckKey replaces the value of an existing key in the check block
// or appends it at the end of the block. A code list written as a block (`- 200`)
// turns into a single line `[200, 301]`.
func (d *Document) setCheckKey(key, value string, path ...any) error {
	n, err := d.node(append(slices.Clone(path), key)...)
	if errors.Is(err, ErrPathNotFound) {
		return d.insertKey(key, value, path...)
	}
	if err != nil {
		return err
	}
	if seq, ok := unwrap(n).(*ast.SequenceNode); ok && !seq.IsFlowStyle {
		if err := d.removeEntryField(key, path...); err != nil {
			return err
		}
		return d.insertKey(key, value, path...)
	}
	start, end, err := d.valueRange(append(slices.Clone(path), key)...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(value))
}

// insertServiceBlock appends a multi-line block (indented from column 0) to the end of
// the service entry, with the indent of its keys.
func (d *Document) insertServiceBlock(block string, path ...any) error {
	column, insert, err := d.insertionPoint(path...)
	if err != nil {
		return err
	}
	ind := strings.Repeat(" ", column)
	var b strings.Builder
	for _, line := range strings.SplitAfter(block, "\n") {
		if line != "" {
			b.WriteString(ind + line)
		}
	}
	text := b.String()
	if insert > 0 && insert <= len(d.src) && d.src[insert-1] != '\n' {
		text = "\n" + text
	}
	return d.replaceRange(insert, insert, []byte(text))
}
