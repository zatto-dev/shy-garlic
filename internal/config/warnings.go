package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Two things that can be wrong with a file have different weight:
//   - an error (Load returns error): it cannot be loaded, the panel keeps running on
//     the previous configuration and shows a message with the line number;
//   - a warning (below): the file loads, but something in it does not do what
//     the author thought — a typo in a key (`chek:`) or an address that cannot
//     be checked. Without this the parser silently ignored the field.

const (
	KindKey     = "klucz"
	KindAddress = "adres"
	// KindCheck: check.interval / check.timeout outside the accepted range,
	// replaced with the default (Key = field, Address = the value as written,
	// Suggestion = the value used instead).
	KindCheck = "check"
	// KindCredentials: a service url: carries user:password@.
	KindCredentials = "credentials"
)

// Warning describes one suspicious place in the config. A structure, not
// ready text: the message is composed by the server, in the browser's language.
type Warning struct {
	Kind       string
	Line       int    // 0 = unknown
	Path       string // e.g. groups[0].services[1]
	Key        string // unknown key
	Suggestion string // nearest known key, if similar
	Group      string // for KindAddress
	Service    string
	Address    string
}

type schema struct {
	keys map[string]*schema
	list *schema
}

var leaf = &schema{}

func object(k map[string]*schema) *schema { return &schema{keys: k} }

// configSchema mirrors the model from model.go. ! When adding a field to Config,
// add it here too — otherwise every user gets a warning about a valid
// key (TestWarningsCoverModel guards this).
var configSchema = object(map[string]*schema{
	"title": leaf,
	"appearance": object(map[string]*schema{
		"view": leaf,
		"background": object(map[string]*schema{
			"preset": leaf, "image": leaf, "blur": leaf, "dim": leaf, "tone": leaf,
		}),
	}),
	"notify":    object(map[string]*schema{"threshold": leaf}),
	"discovery": object(map[string]*schema{"docker": leaf}),
	"groups": {list: object(map[string]*schema{
		"name": leaf,
		"services": {list: object(map[string]*schema{
			"name": leaf, "url": leaf, "icon": leaf, "size": leaf,
			"description": leaf, "tags": leaf,
			"check": object(map[string]*schema{
				"interval": leaf, "timeout": leaf, "expect": leaf, "insecure": leaf,
				"path": leaf, "contains": leaf, "not_contains": leaf,
			}),
		})},
	})},
})

// keyNotReported — places for anchors. `defaults: &defaults` stands in the
// README and in the examples, and `x-*` is a Docker Compose convention.
func keyNotReported(topLevel bool, k string) bool {
	return topLevel && (k == "defaults" || strings.HasPrefix(k, "x-"))
}

const maxDepth = 32 // protection against aliases referring to themselves

type walker struct {
	anchors map[string]ast.Node
	seen    map[string]bool
	result  []Warning
}

type anchorCollector struct{ k map[string]ast.Node }

func (z anchorCollector) Visit(n ast.Node) ast.Visitor {
	if a, ok := n.(*ast.AnchorNode); ok {
		z.k[scalar(a.Name)] = a.Value
	}
	return z
}

// Warnings looks in the source for keys the model does not know. Invalid YAML
// yields an empty list — Load reports that.
func Warnings(src []byte) []Warning {
	f, err := safeParseBytes(src, 0)
	if err != nil || len(f.Docs) == 0 || f.Docs[0].Body == nil {
		return nil
	}
	c := &walker{anchors: map[string]ast.Node{}, seen: map[string]bool{}}
	ast.Walk(anchorCollector{c.anchors}, f.Docs[0].Body)
	c.visitNode(f.Docs[0].Body, configSchema, "", 0)
	return c.result
}

// expand strips anchors and tags and follows an alias to its target.
func (c *walker) expand(n ast.Node) ast.Node {
	for i := 0; i < maxDepth; i++ {
		switch v := n.(type) {
		case *ast.AnchorNode:
			n = v.Value
		case *ast.TagNode:
			n = v.Value
		case *ast.AliasNode:
			target, ok := c.anchors[scalar(v.Value)]
			if !ok {
				return nil
			}
			n = target
		default:
			return n
		}
	}
	return nil
}

func (c *walker) visitNode(n ast.Node, s *schema, path string, depth int) {
	if depth > maxDepth || s == nil {
		return
	}
	switch v := c.expand(n).(type) {
	case *ast.MappingNode:
		for _, kv := range v.Values {
			c.entry(kv, s, path, depth)
		}
	case *ast.MappingValueNode:
		c.entry(v, s, path, depth)
	case *ast.SequenceNode:
		if s.list == nil {
			return
		}
		for i, e := range v.Values {
			c.visitNode(e, s.list, path+"["+strconv.Itoa(i)+"]", depth+1)
		}
	}
}

func (c *walker) entry(kv *ast.MappingValueNode, s *schema, path string, depth int) {
	if _, merge := kv.Key.(*ast.MergeKeyNode); merge {
		// `<<: *a` or `<<: [*a, *b]` — the target's keys belong to THIS mapping
		if seq, ok := c.expand(kv.Value).(*ast.SequenceNode); ok {
			for _, e := range seq.Values {
				c.visitNode(e, s, path, depth+1)
			}
			return
		}
		c.visitNode(kv.Value, s, path, depth+1)
		return
	}
	key := scalar(kv.Key)
	if s.keys == nil { // leaf with a mapping inside — nothing to check
		return
	}
	sub, known := s.keys[key]
	if !known {
		if keyNotReported(path == "", key) {
			return
		}
		line := 0
		if t := kv.Key.GetToken(); t != nil {
			line = t.Position.Line
		}
		// the same anchor pulled in by N merges is one warning
		id := strconv.Itoa(line) + ":" + key
		if c.seen[id] {
			return
		}
		c.seen[id] = true
		c.result = append(c.result, Warning{
			Kind: KindKey, Line: line, Path: path, Key: key,
			Suggestion: nearest(key, s.keys),
		})
		return
	}
	newName := key
	if path != "" {
		newName = path + "." + key
	}
	c.visitNode(kv.Value, sub, newName, depth+1)
}

// nearest returns a known key at most 2 edits away (and shorter than
// half the length, so that `x` does not suggest `url`).
func nearest(key string, known map[string]*schema) string {
	best, bestDist := "", 3
	for k := range known {
		if d := distance(key, k); d < bestDist || (d == bestDist && d < 3 && k < best) {
			best, bestDist = k, d
		}
	}
	if bestDist <= 2 && bestDist*2 < len(key)+1 {
		return best
	}
	return ""
}

func distance(a, b string) int {
	runesA, runesB := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	prev := make([]int, len(runesB)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(runesA); i++ {
		currentRow := make([]int, len(runesB)+1)
		currentRow[0] = i
		for j := 1; j <= len(runesB); j++ {
			cost := 1
			if runesA[i-1] == runesB[j-1] {
				cost = 0
			}
			currentRow[j] = min(prev[j]+1, currentRow[j-1]+1, prev[j-1]+cost)
		}
		prev = currentRow
	}
	return prev[len(runesB)]
}

// AddressWarnings points at services with an address that can be neither
// checked nor opened (e.g. `javascript:`). The API will not accept such, but
// the file can be edited by hand.
func AddressWarnings(cfg *Config) []Warning {
	var out []Warning
	if cfg == nil {
		return nil
	}
	for _, g := range cfg.Groups {
		for _, s := range g.Services {
			if !ValidServiceAddress(s.URL) {
				out = append(out, Warning{Kind: KindAddress, Group: g.Name, Service: s.Name, Address: RedactURL(s.URL)})
			} else if HasCredentials(s.URL) {
				out = append(out, Warning{Kind: KindCredentials, Group: g.Name, Service: s.Name, Address: RedactURL(s.URL)})
			}
		}
	}
	return out
}

// FileError — an invalid file in a form that can be shown to a human.
type FileError struct {
	Line, Column int // 0 = unknown
	Message      string
}

var positionPattern = regexp.MustCompile(`^\[(\d+):(\d+)\]\s*`)

// LoadError is the error from Load together with the position in the file. Error() stays the
// same as before ("parse config: …"); the line number can be read through
// ParseFileError.
type LoadError struct {
	FileError
	err error
}

func (e *LoadError) Error() string { return e.err.Error() }
func (e *LoadError) Unwrap() error { return e.err }

var durationPattern = regexp.MustCompile(`invalid duration "([^"]*)"`)

// locateLine looks for the line for errors the parser reports without a position —
// our own UnmarshalYAML (Duration) does not get a token. It guesses from the error text:
// the first `interval:`/`timeout:` line with that value.
func locateLine(src []byte, message string) int {
	m := durationPattern.FindStringSubmatch(message)
	if m == nil {
		return 0
	}
	pattern, err := regexp.Compile(`^\s*(?:-\s+)?(?:interval|timeout)\s*:\s*["']?` + regexp.QuoteMeta(m[1]) + `["']?\s*(?:#.*)?$`)
	if err != nil {
		return 0
	}
	for i, l := range strings.Split(string(src), "\n") {
		if pattern.MatchString(strings.TrimRight(l, "\r")) {
			return i + 1
		}
	}
	return 0
}

// ParseFileError extracts the line number and a short message from the parser error. goccy
// appends a source fragment with colours in Error() — we do not want it here.
func ParseFileError(err error) FileError {
	var bw *LoadError
	if errors.As(err, &bw) {
		return bw.FileError
	}
	return parseRawError(err)
}

func parseRawError(err error) FileError {
	var b FileError
	var ye yaml.Error
	if errors.As(err, &ye) {
		b.Message = ye.GetMessage()
		if t := ye.GetToken(); t != nil && t.Position != nil {
			b.Line, b.Column = t.Position.Line, t.Position.Column
		}
	}
	if b.Message == "" {
		b.Message = err.Error()
	}
	// an entry "[3:5] message\n  source…" — the only thing we have without a token
	if b.Line == 0 {
		if m := positionPattern.FindStringSubmatch(b.Message); m != nil {
			b.Line, _ = strconv.Atoi(m[1])
			b.Column, _ = strconv.Atoi(m[2])
			b.Message = b.Message[len(m[0]):]
		}
	}
	if i := strings.IndexByte(b.Message, '\n'); i >= 0 {
		b.Message = b.Message[:i]
	}
	b.Message = strings.TrimSpace(positionPattern.ReplaceAllString(b.Message, ""))
	return b
}
