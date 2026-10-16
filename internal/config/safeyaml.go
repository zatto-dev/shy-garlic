package config

import (
	"errors"
	"fmt"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// goccy/go-yaml panics on some malformed input — `expect: !x` with an empty
// value is a nil dereference in decodeSlice (found by FuzzLoadParse, SHYG-20).
// A broken hand-edited config must end as a load error, never as a crash of
// the reload goroutine, so every call into the library goes through here.

func safeUnmarshal(src []byte, v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("yaml: unsupported input (parser panic: %v)", r)
		}
	}()
	return yaml.Unmarshal(src, v)
}

func safeParseBytes(src []byte, mode parser.Mode) (f *ast.File, err error) {
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
