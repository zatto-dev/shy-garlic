package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// goccy/go-yaml panics on some malformed input — `expect: !x` with an empty
// value is a nil dereference in decodeSlice (found by FuzzLoadParse, SHYG-20).
// A broken hand-edited config must end as a load error, never as a crash of
// the reload goroutine, so every call into the library goes through here.

func safeUnmarshal(src []byte, v any) (err error) {
	if err := checkNesting(src); err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("yaml: unsupported input (parser panic: %v)", r)
		}
	}()
	return yaml.Unmarshal(src, v)
}

func safeParseBytes(src []byte, mode parser.Mode) (f *ast.File, err error) {
	if err := checkNesting(src); err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			f, err = nil, fmt.Errorf("yaml: unsupported input (parser panic: %v)", r)
		}
	}()
	return parser.ParseBytes(src, mode)
}

// errLoneCR: the parser treats a lone CR as a line break, while the editor's
// line index counts only LF — a splice would land on the wrong line and
// silently corrupt the file (found by FuzzEdit, SHYG-20). Such files still
// load for display; the editor refuses to touch them.
var errLoneCR = errors.New("parse yaml: the file has lone CR line endings — save it with LF or CRLF line endings to edit it here")

func hasLoneCR(src []byte) bool {
	for i, b := range src {
		if b == '\r' && (i+1 == len(src) || src[i+1] != '\n') {
			return true
		}
	}
	return false
}

// maxNesting — how deep a config may nest. A real file is 5 levels deep
// (groups → services → check → …); the parser's memory and time grow with the
// depth, and `[[[[…` ten thousand levels deep used to eat gigabytes before the
// parser gave up.
const maxNesting = 64

// checkNesting rejects too-deep input BEFORE it reaches the parser. It is a
// cheap line scanner, not a parser, and errs on the conservative side: it
// counts flow brackets (outside quotes and comments), block indentation levels
// and `- - -` sequence markers. A file that trips it by accident would have to be
// stranger than anything a homelab dashboard needs.
//
// The error carries a "[line:column]" prefix, which parseRawError turns into a
// position for the banner.
func checkNesting(src []byte) error {
	flow := 0
	var indents []int // open block indentation levels
	skipBelow := -1   // inside a block scalar: skip lines indented deeper than this
	// quote is the open quote character, carried ACROSS lines: a quoted scalar may
	// span several lines, and brackets inside it are text, not nesting. Resetting it
	// at every line end both let a stray `[` stay counted forever (rejecting valid
	// files) and let one hide real nesting from the scan (a bypass).
	var quote byte
	line := 1
	for start := 0; start <= len(src); line++ {
		end := start
		for end < len(src) && src[end] != '\n' {
			end++
		}
		text := src[start:end]
		start = end + 1

		indent := 0
		rest := text
		depth := 0
		if quote != 0 {
			// continuation of a multi-line quoted scalar: no indentation rules apply
			depth = len(indents)
		} else {
			for indent < len(text) && text[indent] == ' ' {
				indent++
			}
			rest = text[indent:]
			if len(rest) == 0 || rest[0] == '#' {
				continue
			}
			// the content of a `|` / `>` block is text, brackets and all
			if skipBelow >= 0 {
				if indent > skipBelow {
					continue
				}
				skipBelow = -1
			}
			// block levels: pop deeper-or-equal indents, then push this one
			if flow == 0 {
				for len(indents) > 0 && indents[len(indents)-1] >= indent {
					indents = indents[:len(indents)-1]
				}
				indents = append(indents, indent)
			}
			depth = len(indents)
			// `- - - x` opens a sequence per marker on the same line
			for i := 0; i+1 < len(rest) && (rest[i] == '-' || rest[i] == '?') && rest[i+1] == ' '; {
				depth++
				i += 2
				for i < len(rest) && rest[i] == ' ' {
					i++
				}
			}
		}
		for i := 0; i < len(rest); i++ {
			c := rest[i]
			switch {
			case quote == '"':
				if c == '\\' {
					i++
				} else if c == '"' {
					quote = 0
				}
			case quote == '\'':
				if c == '\'' {
					if i+1 < len(rest) && rest[i+1] == '\'' {
						i++ // '' is an escaped apostrophe
					} else {
						quote = 0
					}
				}
			case c == '"' || c == '\'':
				// a quote only opens a string at the start of a token
				if i == 0 || strings.IndexByte(" [{,:-", rest[i-1]) >= 0 {
					quote = c
				}
			case c == '#' && (i == 0 || rest[i-1] == ' ' || rest[i-1] == '\t'):
				i = len(rest)
			case c == '[' || c == '{':
				flow++
				if depth+flow > maxNesting {
					return fmt.Errorf("[%d:%d] yaml: nesting is deeper than %d levels", line, indent+i+1, maxNesting)
				}
			case c == ']' || c == '}':
				if flow > 0 {
					flow--
				}
			}
		}
		if depth+flow > maxNesting {
			return fmt.Errorf("[%d:%d] yaml: nesting is deeper than %d levels", line, indent+1, maxNesting)
		}
		if flow == 0 && quote == 0 && blockScalarHeader.Match(rest) {
			skipBelow = indent
		}
	}
	return nil
}

// blockScalarHeader matches a line that ends in a block scalar indicator
// (`key: |`, `- >-`, `desc: |2+  # note`).
var blockScalarHeader = regexp.MustCompile(`(?:^|[\s:-])[|>][+-]?[0-9]?[+-]?[ \t]*(?:#.*)?\r?$`)
