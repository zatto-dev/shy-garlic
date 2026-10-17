package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

var (
	ErrPathNotFound    = errors.New("path not found in config")
	ErrServiceNotFound = errors.New("service not found")
	ErrGroupNotFound   = errors.New("group not found")
	// ErrDuplicate — the name is already taken in the same list (service in a group, group).
	// Two services with the same name would merge check history under one key.
	ErrDuplicate = errors.New("name already exists")
	// ErrNameRequired — a service or group was given an empty name.
	ErrNameRequired = errors.New("name is required")
	// ErrPosition — a move target outside the list.
	ErrPosition = errors.New("position out of range")
)

// Document is the configuration seen as TEXT, not as a data structure.
//
// The AST serves solely for navigation — to point out where in the original bytes
// a given value lies. Saving never prints the AST, it replaces a range of
// bytes in src. Thanks to that everything the user did not change — comments,
// blank lines, anchors, merge keys, alignment — stays physically untouched.
//
// Verified empirically 2026-08-14: printing the AST (even in goccy, which copes
// best) collapses the alignment of spaces before an end-of-line comment, i.e.
// it touches lines nobody edited. Hence splice instead of print.
type Document struct {
	src   []byte
	file  *ast.File
	lines []int // offset of each line's start (0-based line index)

	// filled in by Open — needed for a safe write
	path string
	hash [32]byte
	mode os.FileMode

	// crlf: the file had ONLY CRLF line endings. src then holds plain LF
	// (all offset navigation and insertions count on a single line-end byte),
	// and Bytes() restores CRLF. A file with mixed endings stays untouched.
	crlf bool
}

// normalizeLineEndings turns CRLF into LF, but only when EVERY \n in the file is
// preceded by \r — thanks to that an untouched round-trip is byte for byte
// identical, and a file with mixed endings is not silently rewritten.
func normalizeLineEndings(src []byte) ([]byte, bool) {
	crlf := bytes.Count(src, []byte("\r\n"))
	if crlf == 0 || crlf != bytes.Count(src, []byte("\n")) {
		return src, false
	}
	return bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n")), true
}

func Parse(src []byte) (*Document, error) {
	if hasLoneCR(src) {
		return nil, errLoneCR
	}
	src, crlf := normalizeLineEndings(src)
	d := &Document{src: src, crlf: crlf}
	if err := d.reparse(); err != nil {
		return nil, err
	}
	return d, nil
}

// reparse refreshes the AST and the line index after src changes.
func (d *Document) reparse() error {
	f, err := safeParseBytes(d.src, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	if len(f.Docs) == 0 || f.Docs[0].Body == nil {
		return errors.New("parse yaml: empty document")
	}
	d.file = f
	d.lines = d.lines[:0]
	d.lines = append(d.lines, 0)
	for i, b := range d.src {
		if b == '\n' {
			d.lines = append(d.lines, i+1)
		}
	}
	return nil
}

// Bytes returns the current file content. These are the real bytes, not a re-print of the AST;
// for a CRLF file the line endings go back to CRLF.
func (d *Document) Bytes() []byte {
	if d.crlf {
		return bytes.ReplaceAll(d.src, []byte("\n"), []byte("\r\n"))
	}
	return d.src
}

// root returns the root of the first document.
func (d *Document) root() ast.Node { return d.file.Docs[0].Body }

// scalar returns the raw value of the token.
// ! Do not use n.String(): with ParseComments it appends the end-of-line comment
// (`"Sonarr" # quotes on purpose`) and breaks every comparison.
func scalar(n ast.Node) string {
	if n == nil {
		return ""
	}
	if t := n.GetToken(); t != nil {
		return t.Value
	}
	return ""
}

// unwrap strips wrappers that carry no data: `key: &anchor {...}`
// gives an AnchorNode, not a mapping. Navigation should not see that.
func unwrap(n ast.Node) ast.Node {
	for {
		switch v := n.(type) {
		case *ast.AnchorNode:
			n = v.Value
		case *ast.TagNode:
			n = v.Value
		default:
			return n
		}
	}
}

func asMapping(n ast.Node) *ast.MappingNode {
	switch v := unwrap(n).(type) {
	case *ast.MappingNode:
		return v
	case *ast.MappingValueNode:
		return &ast.MappingNode{Values: []*ast.MappingValueNode{v}}
	}
	return nil
}

// child descends one step: by key (string) or by index (int).
func child(n ast.Node, step any) ast.Node {
	switch s := step.(type) {
	case string:
		m := asMapping(n)
		if m == nil {
			return nil
		}
		for _, kv := range m.Values {
			if scalar(kv.Key) == s {
				return kv.Value
			}
		}
	case int:
		seq, ok := unwrap(n).(*ast.SequenceNode)
		if !ok || s < 0 || s >= len(seq.Values) {
			return nil
		}
		return seq.Values[s]
	}
	return nil
}

// node returns the node under the path, e.g. node("groups", 0, "services", 1, "url").
func (d *Document) node(path ...any) (ast.Node, error) {
	cur := d.root()
	for i, step := range path {
		next := child(cur, step)
		if next == nil {
			return nil, fmt.Errorf("%w: %v (step %d)", ErrPathNotFound, path[:i+1], i)
		}
		cur = next
	}
	return cur, nil
}

// offsetOf converts a token position (line/column, both 1-based) to a byte offset.
func (d *Document) offsetOf(n ast.Node) (int, error) {
	tok := n.GetToken()
	if tok == nil || tok.Position == nil {
		return 0, fmt.Errorf("%w: node without a position", ErrPathNotFound)
	}
	lineIdx := tok.Position.Line - 1
	if lineIdx < 0 || lineIdx >= len(d.lines) {
		return 0, fmt.Errorf("%w: line %d outside the file", ErrPathNotFound, tok.Position.Line)
	}
	off := d.lines[lineIdx] + tok.Position.Column - 1
	if off < 0 || off > len(d.src) {
		return 0, fmt.Errorf("%w: offset %d outside the file", ErrPathNotFound, off)
	}
	return off, nil
}

// valueRange returns the byte range [start,end) occupied by the value under the path.
// The range includes quotes if the value is quoted, and does not include
// the end-of-line comment or the spaces before it.
func (d *Document) valueRange(path ...any) (int, int, error) {
	n, err := d.node(path...)
	if err != nil {
		return 0, 0, err
	}
	start, err := d.offsetOf(n)
	if err != nil {
		return 0, 0, err
	}
	return start, d.scanValueEnd(start), nil
}

// scanValueEnd determines the end of a value starting at start.
func (d *Document) scanValueEnd(start int) int {
	src := d.src
	if start >= len(src) {
		return len(src)
	}

	switch src[start] {
	case '"', '\'':
		q := src[start]
		for i := start + 1; i < len(src); i++ {
			if src[i] == '\\' && q == '"' {
				i++
				continue
			}
			if src[i] == q {
				return i + 1
			}
			if src[i] == '\n' {
				break
			}
		}
	case '[', '{':
		open, close := src[start], byte(']')
		if open == '{' {
			close = '}'
		}
		depth := 0
		for i := start; i < len(src); i++ {
			switch src[i] {
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
	}

	// scalar without quotes: to the end of the line, minus the comment and spaces on the right
	end := len(src)
	for i := start; i < len(src); i++ {
		if src[i] == '\n' {
			end = i
			break
		}
	}
	for i := start; i < end-1; i++ {
		if (src[i] == ' ' || src[i] == '\t') && src[i+1] == '#' {
			end = i
			break
		}
	}
	for end > start && (src[end-1] == ' ' || src[end-1] == '\t' || src[end-1] == '\r') {
		end--
	}
	return end
}

// keyOffset returns the byte offset of the KEY (not the value) in map n.
// Needed wherever we insert or remove a whole line rather than
// replace a value — i.e. where valueRange is not enough.
func (d *Document) keyOffset(n ast.Node, key string) (int, error) {
	m := asMapping(n)
	if m == nil {
		return 0, fmt.Errorf("%w: node is not a map (key %q)", ErrPathNotFound, key)
	}
	for _, kv := range m.Values {
		if scalar(kv.Key) == key {
			return d.offsetOf(kv.Key)
		}
	}
	return 0, fmt.Errorf("%w: no key %q", ErrPathNotFound, key)
}
