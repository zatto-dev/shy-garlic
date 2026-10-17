package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// replaceRange replaces bytes [start,end) and re-parses the document.
// After every mutation the AST offsets are stale, so reparse is mandatory.
func (d *Document) replaceRange(start, end int, repl []byte) error {
	next := slices.Concat(d.src[:start:start], repl, d.src[end:])
	prev := d.src
	d.src = next
	if err := d.reparse(); err != nil {
		d.src = prev
		_ = d.reparse() // back to the state before the change
		return err
	}
	return nil
}

// quoteLike formats the value so it matches the style already used in the file.
// old is the original bytes of the value (may contain quotes).
func quoteLike(old []byte, value string) string {
	if len(old) > 0 && (old[0] == '"' || old[0] == '\'') {
		return quoteWith(rune(old[0]), value)
	}
	if needsQuoting(value) {
		return quoteWith('"', value)
	}
	return value
}

func quoteWith(q rune, v string) string {
	// ! A single-quoted scalar cannot carry control characters: a raw CR or LF is
	// folded by the parser (a group named "a\rb" corrupted the file), and there is
	// no escape syntax. Fall back to double quotes, which have one.
	if q == '\'' && !hasControl(v) {
		// in YAML an apostrophe in a single-quoted value is doubled
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case isControl(r):
			// every other control character, DEL, NEL and the Unicode line/paragraph
			// separators — YAML treats several of them as line breaks
			if r <= 0xff {
				fmt.Fprintf(&b, `\x%02X`, r)
			} else {
				fmt.Fprintf(&b, `\u%04X`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029
}

func hasControl(v string) bool {
	return strings.ContainsFunc(v, isControl)
}

// needsQuoting says whether writing the value bare would change its meaning
// or break the file.
func needsQuoting(v string) bool {
	if v == "" {
		return true
	}
	if strings.TrimSpace(v) != v {
		return true
	}
	if hasControl(v) {
		return true
	}
	if strings.Contains(v, ": ") || strings.HasSuffix(v, ":") || strings.Contains(v, " #") {
		return true
	}
	switch v[0] {
	case '&', '*', '!', '%', '@', '`', '>', '|', '{', '[', ',', '?', '#', '-', ':', '\'', '"':
		return true
	}
	// a value that on its own would become a number, bool or null
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return true
	}
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	return !plainRoundTrips(v)
}

// lineIndent returns the indent (in bytes) of the line containing the offset.
func (d *Document) lineIndent(off int) int {
	start := d.lineStart(off)
	n := 0
	for i := start; i < len(d.src) && (d.src[i] == ' ' || d.src[i] == '\t'); i++ {
		n++
	}
	return n
}

func (d *Document) lineStart(off int) int {
	for i := off; i > 0; i-- {
		if d.src[i-1] == '\n' {
			return i
		}
	}
	return 0
}

func (d *Document) lineEnd(off int) int {
	for i := off; i < len(d.src); i++ {
		if d.src[i] == '\n' {
			return i
		}
	}
	return len(d.src)
}
