package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	keyShape   = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)
	jsStatic   = regexp.MustCompile(`\btr\("([a-z0-9_]+)"\s*[,)]`)
	jsPrefix   = regexp.MustCompile(`\btr\("([a-z0-9_]+_)"\s*\+`)
	tmplKey    = regexp.MustCompile(`\.T\.([A-Za-z0-9_]+)`)
	goCallKey  = regexp.MustCompile(`\bT\([^,()]*(?:\([^()]*\))?[^,()]*,\s*"([a-z0-9_]+)"\)`)
	goLiteral  = regexp.MustCompile(`"([a-z][a-z0-9_]*)"`)
	percentVrb = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
)

func read(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func serverGoFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join("..", "server", "*.go"))
	if err != nil || len(all) == 0 {
		t.Fatalf("no server sources found: %v", err)
	}
	var out []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// Every key is English snake_case and has both texts, with the same printf verbs.
func TestKeysAreEnglishAndComplete(t *testing.T) {
	for _, k := range Keys() {
		if !keyShape.MatchString(k) {
			t.Errorf("key %q is not lower snake_case", k)
		}
		en, pl := texts[k][EN], texts[k][PL]
		if en == "" {
			t.Errorf("key %q has no English text", k)
		}
		if pl == "" {
			t.Errorf("key %q has no Polish text", k)
		}
		if a, b := len(percentVrb.FindAllString(en, -1)), len(percentVrb.FindAllString(pl, -1)); a != b {
			t.Errorf("key %q: %d printf verbs in EN, %d in PL", k, a, b)
		}
	}
}

// Keys used by the templates, app.js and the server exist, and every key is used
// somewhere. Keys built at run time ("source_" + value in app.js) are found by their
// prefix: the prefix has to match at least one key, and those keys count as used.
func TestKeysMatchTheirUsage(t *testing.T) {
	known := map[string]bool{}
	for _, k := range Keys() {
		known[k] = true
	}
	used := map[string]bool{}
	missing := func(where, k string) {
		if !known[k] {
			t.Errorf("%s uses key %q, which is not in the texts map", where, k)
		}
		used[k] = true
	}

	js := read(t, "..", "server", "assets", "app.js")
	for _, m := range jsStatic.FindAllStringSubmatch(js, -1) {
		missing("app.js", m[1])
	}
	for _, m := range jsPrefix.FindAllStringSubmatch(js, -1) {
		n := 0
		for k := range known {
			if strings.HasPrefix(k, m[1]) {
				used[k] = true
				n++
			}
		}
		if n == 0 {
			t.Errorf("app.js builds keys with prefix %q, but no key has it", m[1])
		}
	}

	tmpls, _ := filepath.Glob(filepath.Join("..", "server", "templates", "*.html"))
	if len(tmpls) == 0 {
		t.Fatal("no templates found")
	}
	for _, f := range tmpls {
		for _, m := range tmplKey.FindAllStringSubmatch(read(t, f), -1) {
			missing(filepath.Base(f), m[1])
		}
	}

	for _, f := range serverGoFiles(t) {
		src := read(t, f)
		for _, m := range goCallKey.FindAllStringSubmatch(src, -1) {
			missing(filepath.Base(f), m[1])
		}
		// keys also travel through variables (error keys returned by validators), so a
		// literal equal to a key counts as a use
		for _, m := range goLiteral.FindAllStringSubmatch(src, -1) {
			if known[m[1]] {
				used[m[1]] = true
			}
		}
	}

	for _, k := range Keys() {
		if !used[k] {
			t.Errorf("key %q is not used by any template, app.js or server code", k)
		}
	}
}
