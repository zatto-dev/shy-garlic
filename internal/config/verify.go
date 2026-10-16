package config

import (
	"errors"
	"slices"

	"github.com/goccy/go-yaml/ast"
)

// ErrUnsupportedLayout: the edit was undone because the file did not read back
// as written. Splices trust token positions reported by goccy/go-yaml; on
// layouts where those do not match the bytes (found by fuzzing, SHYG-20) the
// value would land in the wrong place, so the editor refuses instead.
var ErrUnsupportedLayout = errors.New("this part of the file has a layout the editor cannot change safely — edit it in the file")

// verifyLoads re-reads the document after an edit and, unless it loads and
// satisfies ok, restores prev.
func (d *Document) verifyLoads(prev []byte, ok func(*Config) bool) error {
	if cfg, err := Load(d.src); err == nil && ok(cfg) {
		return nil
	}
	d.src = prev
	_ = d.reparse()
	return ErrUnsupportedLayout
}

// verifyService is verifyLoads for an edit of one service, looked up by its
// name after the edit.
func (d *Document) verifyService(prev []byte, group, service string, ok func(Service) bool) error {
	return d.verifyLoads(prev, func(cfg *Config) bool {
		return findLoaded(cfg, group, service, ok)
	})
}

// verifyRename checks a rename moved exactly one entry from the old name to
// the new one. Finding the new name is not enough: a splice landing in another
// entry would create a second service and leave the old one untouched.
func (d *Document) verifyRename(prev []byte, group, oldName, newName string) error {
	before, err := Load(prev)
	if err != nil {
		return d.verifyLoads(prev, func(*Config) bool { return false })
	}
	total, olds := countInGroup(before, group, oldName)
	return d.verifyLoads(prev, func(cfg *Config) bool {
		t, o := countInGroup(cfg, group, oldName)
		if oldName == newName {
			return t == total && o == olds
		}
		_, n := countInGroup(cfg, group, newName)
		return t == total && o == olds-1 && n >= 1
	})
}

// countInGroup returns how many services the group has and how many of them
// are called name.
func countInGroup(cfg *Config, group, name string) (total, named int) {
	for _, g := range cfg.Groups {
		if g.Name != group {
			continue
		}
		for _, s := range g.Services {
			total++
			if s.Name == name {
				named++
			}
		}
	}
	return total, named
}

func findLoaded(cfg *Config, group, service string, ok func(Service) bool) bool {
	for _, g := range cfg.Groups {
		if g.Name != group {
			continue
		}
		for _, s := range g.Services {
			if s.Name == service && ok(s) {
				return true
			}
		}
	}
	return false
}

func hasGroup(cfg *Config, name string) bool {
	for _, g := range cfg.Groups {
		if g.Name == name {
			return true
		}
	}
	return false
}

// plainRoundTrips asks the parser itself whether v written without quotes
// reads back as exactly this string. Heuristics miss cases such as `0x10`,
// `0o7` or `.inf` (found by fuzzing, SHYG-20); the parser does not.
func plainRoundTrips(v string) bool {
	f, err := safeParseBytes([]byte("k: "+v+"\n"), 0)
	if err != nil || len(f.Docs) != 1 {
		return false
	}
	m := asMapping(f.Docs[0].Body)
	if m == nil || len(m.Values) != 1 {
		return false
	}
	s, ok := m.Values[0].Value.(*ast.StringNode)
	return ok && s.Value == v
}

// checkMatches reports whether the loaded check carries every field of want
// (defaults applied the same way Load applies them).
func checkMatches(got, want Check) bool {
	applyDefaults(&want)
	return got.Interval == want.Interval && got.Timeout == want.Timeout &&
		slices.Equal(got.Expect, want.Expect) && got.Insecure == want.Insecure
}
