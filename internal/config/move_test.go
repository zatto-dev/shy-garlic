package config

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

// test file: comments above entries, fields unknown to the panel, blank lines
// between services, an empty group and a footer in column 0.
const moveSrc = `title: T

groups:
  # Media — pierwsza
  - name: Media
    services:
      # komentarz A
      - name: A
        url: http://a
        extra: "nieznane pole"

      - name: B   # koniec linii B
        url: http://b

      - name: C
        url: http://c

  - name: Sieć
    services:
      - name: D
        url: http://d
      - name: E
        url: http://e

  # Pusta
  - name: Pusta
    services: []

# stopka
`

func layoutOf(t *testing.T, src []byte) string {
	t.Helper()
	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, src)
	}
	var parts []string
	for _, g := range cfg.Groups {
		var n []string
		for _, s := range g.Services {
			n = append(n, s.Name)
		}
		parts = append(parts, g.Name+":"+strings.Join(n, ","))
	}
	return strings.Join(parts, " | ")
}

func TestMoveServiceTo(t *testing.T) {
	cases := []struct {
		name             string
		z, svc, to       string
		idx              int
		want             string
		commentOnService string // fragment that must stand right above the entry after the move
	}{
		{"in group: down to the middle", "Media", "A", "Media", 1, "Media:B,A,C | Sieć:D,E | Pusta:", ""},
		{"in group: to the end", "Media", "A", "Media", 2, "Media:B,C,A | Sieć:D,E | Pusta:", ""},
		{"in group: last to the start", "Media", "C", "Media", 0, "Media:C,A,B | Sieć:D,E | Pusta:", ""},
		{"in group: up by one", "Media", "C", "Media", 1, "Media:A,C,B | Sieć:D,E | Pusta:", ""},
		{"in group without blank lines: up", "Sieć", "E", "Sieć", 0, "Media:A,B,C | Sieć:E,D | Pusta:", ""},
		{"between groups: to the start", "Media", "B", "Sieć", 0, "Media:A,C | Sieć:B,D,E | Pusta:", ""},
		{"between groups: middle", "Media", "A", "Sieć", 1, "Media:B,C | Sieć:D,A,E | Pusta:", "# komentarz A"},
		{"between groups: end", "Sieć", "D", "Media", 3, "Media:A,B,C,D | Sieć:E | Pusta:", ""},
		{"to an empty group", "Media", "C", "Pusta", 0, "Media:A,B | Sieć:D,E | Pusta:C", ""},
		{"last service of a group to another", "Sieć", "D", "Pusta", 0, "Media:A,B,C | Sieć:E | Pusta:D", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Parse([]byte(moveSrc))
			if err != nil {
				t.Fatal(err)
			}
			if err := d.MoveServiceTo(c.z, c.svc, c.to, c.idx); err != nil {
				t.Fatal(err)
			}
			out := d.Bytes()
			if got := layoutOf(t, out); got != c.want {
				t.Fatalf("layout = %s\nwant   = %s\n%s", got, c.want, out)
			}
			s := string(out)
			// nothing is lost: comments, unknown field, footer, group comments
			for _, f := range []string{"# komentarz A", "# koniec linii B", `extra: "nieznane pole"`, "# stopka", "# Media — pierwsza", "# Pusta", "title: T"} {
				if strings.Count(s, f) != 1 {
					t.Errorf("fragment %q occurs %d times\n%s", f, strings.Count(s, f), s)
				}
			}
			if c.commentOnService != "" {
				if !strings.Contains(s, "      "+c.commentOnService+"\n      - name: "+c.svc+"\n") {
					t.Errorf("comment does not stand above the entry:\n%s", s)
				}
			}
			// no double blank lines
			if strings.Contains(s, "\n\n\n") {
				t.Errorf("double blank line:\n%s", s)
			}
			// moving back we return to the original (except the group whose style changed)
			if c.z == c.to {
				origIndent := serviceIndex(t, []byte(moveSrc), c.z, c.svc)
				if err := d.MoveServiceTo(c.z, c.svc, c.to, origIndent); err != nil {
					t.Fatal(err)
				}
				if string(d.Bytes()) != moveSrc {
					t.Errorf("round trip move changed the file:\n%s", d.Bytes())
				}
			}
		})
	}
}

func serviceIndex(t *testing.T, src []byte, group, svc string) int {
	t.Helper()
	cfg, _ := Load(src)
	for _, g := range cfg.Groups {
		if g.Name != group {
			continue
		}
		for i, s := range g.Services {
			if s.Name == svc {
				return i
			}
		}
	}
	t.Fatalf("missing %s/%s", group, svc)
	return -1
}

func TestMoveServiceToIndentAndBlankLineStyle(t *testing.T) {
	d, _ := Parse([]byte(moveSrc))
	if err := d.MoveServiceTo("Sieć", "D", "Media", 1); err != nil {
		t.Fatal(err)
	}
	want := "      - name: A\n        url: http://a\n        extra: \"nieznane pole\"\n\n      - name: D\n        url: http://d\n\n      - name: B"
	if !strings.Contains(string(d.Bytes()), want) {
		t.Errorf("blank-line separation style did not carry over:\n%s", d.Bytes())
	}
	// into a group without blank lines — the entry must not get any
	d, _ = Parse([]byte(moveSrc))
	if err := d.MoveServiceTo("Media", "B", "Sieć", 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "        url: http://d\n      - name: B   # koniec linii B\n        url: http://b\n      - name: E") {
		t.Errorf("entry got a blank line in a group that has none:\n%s", d.Bytes())
	}
}

func TestMoveServiceToOtherGroupIndent(t *testing.T) {
	src := "groups:\n  - name: G1\n    services:\n      - name: A\n        url: a\n  - name: G2\n    services:\n    - name: B\n      url: b\n"
	d, _ := Parse([]byte(src))
	if err := d.MoveServiceTo("G1", "A", "G2", 0); err != nil {
		t.Fatal(err)
	}
	want := "  - name: G2\n    services:\n    - name: A\n      url: a\n    - name: B\n"
	if !strings.Contains(string(d.Bytes()), want) {
		t.Errorf("indent not recomputed for the target:\n%s", d.Bytes())
	}
	if got := layoutOf(t, d.Bytes()); got != "G1: | G2:A,B" {
		t.Errorf("layout = %s", got)
	}
}

func TestMoveServiceToNoOpByteIdentical(t *testing.T) {
	d, _ := Parse([]byte(moveSrc))
	for _, c := range []struct {
		z, u string
		i    int
	}{{"Media", "A", 0}, {"Media", "B", 1}, {"Media", "C", 2}, {"Sieć", "E", 1}} {
		if err := d.MoveServiceTo(c.z, c.u, c.z, c.i); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(d.Bytes(), []byte(moveSrc)) {
			t.Fatalf("no-op %v changed bytes", c)
		}
	}
}

func TestMoveServiceToErrors(t *testing.T) {
	cases := []struct {
		name     string
		z, u, to string
		idx      int
		wantErr  error // nil = any
	}{
		{"index too large (in group)", "Media", "A", "Media", 3, errIndex},
		{"index too large (to another)", "Media", "A", "Sieć", 3, errIndex},
		{"negative index", "Media", "A", "Sieć", -1, errIndex},
		{"pusta grupa, indeks 1", "Media", "A", "Pusta", 1, errIndex},
		{"no service", "Media", "Zzz", "Sieć", 0, ErrServiceNotFound},
		{"no source group", "Nope", "A", "Sieć", 0, ErrServiceNotFound},
		{"brak grupy docelowej", "Media", "A", "Nope", 0, ErrGroupNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := Parse([]byte(moveSrc))
			err := d.MoveServiceTo(c.z, c.u, c.to, c.idx)
			if err == nil {
				t.Fatal("want an error")
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) && !errors.Is(err, ErrGroupNotFound) {
				t.Errorf("err = %v", err)
			}
			if string(d.Bytes()) != moveSrc {
				t.Error("error, yet the file changed")
			}
		})
	}
	// duplicate name in the target group
	src := strings.Replace(moveSrc, "name: D", "name: A", 1)
	d, _ := Parse([]byte(src))
	if err := d.MoveServiceTo("Media", "A", "Sieć", 0); err == nil {
		t.Error("duplicate should give an error")
	}
}

func TestMoveServiceDelegatesToEnd(t *testing.T) {
	d, _ := Parse([]byte(moveSrc))
	if err := d.MoveService("Media", "A", "Sieć"); err != nil {
		t.Fatal(err)
	}
	if got := layoutOf(t, d.Bytes()); got != "Media:B,C | Sieć:D,E,A | Pusta:" {
		t.Errorf("layout = %s", got)
	}
}

func TestMoveGroup(t *testing.T) {
	cases := []struct {
		name, group string
		idx         int
		want        string
	}{
		{"first to the end", "Media", 2, "Sieć:D,E | Pusta: | Media:A,B,C"},
		{"first down by one", "Media", 1, "Sieć:D,E | Media:A,B,C | Pusta:"},
		{"last to the start", "Pusta", 0, "Pusta: | Media:A,B,C | Sieć:D,E"},
		{"middle to the start", "Sieć", 0, "Sieć:D,E | Media:A,B,C | Pusta:"},
		{"middle to the end", "Sieć", 2, "Media:A,B,C | Pusta: | Sieć:D,E"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := Parse([]byte(moveSrc))
			if err := d.MoveGroup(c.group, c.idx); err != nil {
				t.Fatal(err)
			}
			out := string(d.Bytes())
			if got := layoutOf(t, d.Bytes()); got != c.want {
				t.Fatalf("layout = %s\nwant   = %s\n%s", got, c.want, out)
			}
			for _, f := range []string{"# komentarz A", "# koniec linii B", `extra: "nieznane pole"`, "# stopka", "# Media — pierwsza", "# Pusta", "title: T"} {
				if strings.Count(out, f) != 1 {
					t.Errorf("fragment %q occurs %d times\n%s", f, strings.Count(out, f), out)
				}
			}
			// group comment travels with the group
			if i := strings.Index(out, "# Media — pierwsza"); !strings.HasPrefix(out[i:], "# Media — pierwsza\n  - name: Media\n") {
				t.Errorf("comment detached from group Media:\n%s", out)
			}
			if i := strings.Index(out, "# Pusta"); !strings.HasPrefix(out[i:], "# Pusta\n  - name: Pusta\n") {
				t.Errorf("comment detached from group Pusta:\n%s", out)
			}
			if strings.Contains(out, "\n\n\n") || !strings.HasSuffix(out, "\n# stopka\n") {
				t.Errorf("spacing or footer:\n%s", out)
			}
			// back in place = the original
			orig := slices.Index([]string{"Media", "Sieć", "Pusta"}, c.group)
			if err := d.MoveGroup(c.group, orig); err != nil {
				t.Fatal(err)
			}
			if string(d.Bytes()) != moveSrc {
				t.Errorf("round trip changed the file:\n%s", d.Bytes())
			}
		})
	}
}

func TestMoveGroupNoOpAndErrors(t *testing.T) {
	d, _ := Parse([]byte(moveSrc))
	if err := d.MoveGroup("Sieć", 1); err != nil || string(d.Bytes()) != moveSrc {
		t.Fatalf("no-op: err=%v, bytes changed=%v", err, string(d.Bytes()) != moveSrc)
	}
	if err := d.MoveGroup("Sieć", 3); !errors.Is(err, errIndex) {
		t.Errorf("err = %v", err)
	}
	if err := d.MoveGroup("Sieć", -1); !errors.Is(err, errIndex) {
		t.Errorf("err = %v", err)
	}
	if err := d.MoveGroup("Nope", 0); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestMoveGroupWithoutBlankLines(t *testing.T) {
	src := "groups:\n  - name: A\n    services:\n      - name: a\n        url: x\n  - name: B\n    services:\n      - name: b\n        url: y\n"
	d, _ := Parse([]byte(src))
	if err := d.MoveGroup("A", 1); err != nil {
		t.Fatal(err)
	}
	want := "groups:\n  - name: B\n    services:\n      - name: b\n        url: y\n  - name: A\n    services:\n      - name: a\n        url: x\n"
	if string(d.Bytes()) != want {
		t.Errorf("got:\n%s", d.Bytes())
	}
}

func TestMoveServiceToWithoutTrailingNewline(t *testing.T) {
	src := strings.TrimRight(moveSrc, "\n# stopka") // ends with `services: []`
	src = strings.TrimSuffix(strings.TrimSuffix(moveSrc, "\n# stopka\n"), "\n")
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveServiceTo("Media", "A", "Pusta", 0); err != nil {
		t.Fatal(err)
	}
	if got := layoutOf(t, d.Bytes()); got != "Media:B,C | Sieć:D,E | Pusta:A" {
		t.Errorf("layout = %s\n%s", got, d.Bytes())
	}
}

func TestSetTitle(t *testing.T) {
	d, _ := Parse(loadBasic(t))
	if err := d.SetTitle("  Nowy tytuł "); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "title: Nowy tytuł      # komentarz na końcu linii\n") {
		t.Errorf("comment or style lost:\n%s", d.Bytes())
	}
	if cfg, _ := Load(d.Bytes()); cfg.Title != "Nowy tytuł" {
		t.Errorf("title = %q", cfg.Title)
	}
	assertOriginalLinesUntouched(t, strings.Replace(string(loadBasic(t)), "Homelab Demo", "Nowy tytuł", 1), string(d.Bytes()))
}

func TestSetTitleQuoting(t *testing.T) {
	for _, v := range []string{"yes", "no", "123", "null", "a: b", "# x", "*gwiazda", "&kotwica", "!tag", "- lista", "x #y", "it's", `say "hi"`, "back\\slash", "Zażółć gęślą 🧄", "~"} {
		d, _ := Parse(loadBasic(t))
		if err := d.SetTitle(v); err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		cfg, err := Load(d.Bytes())
		if err != nil {
			t.Fatalf("%q: file does not parse: %v\n%s", v, err, d.Bytes())
		}
		if cfg.Title != v {
			t.Errorf("wrote %q, loaded %q", v, cfg.Title)
		}
	}
}

func TestSetTitleWhenKeyMissing(t *testing.T) {
	src := "# nagłówek\n\n# o grupach\ngroups:\n  - name: G\n    services: []\n"
	d, _ := Parse([]byte(src))
	if err := d.SetTitle("Panel: dom"); err != nil {
		t.Fatal(err)
	}
	want := "# nagłówek\n\ntitle: \"Panel: dom\"\n\n# o grupach\ngroups:\n  - name: G\n    services: []\n"
	if string(d.Bytes()) != want {
		t.Errorf("got:\n%s", d.Bytes())
	}
	if cfg, _ := Load(d.Bytes()); cfg.Title != "Panel: dom" {
		t.Errorf("title = %q", cfg.Title)
	}
}

func TestSetTitleValidation(t *testing.T) {
	cases := []struct {
		v   string
		err error
	}{
		{"", ErrTitleEmpty}, {"   ", ErrTitleEmpty},
		{strings.Repeat("a", 81), ErrTitleTooLong},
		{"a\nb", ErrTitleChars}, {"a\x00b", ErrTitleChars}, {"a\tb", ErrTitleChars}, {"a b", ErrTitleChars},
	}
	for _, c := range cases {
		d, _ := Parse(loadBasic(t))
		if err := d.SetTitle(c.v); !errors.Is(err, c.err) {
			t.Errorf("%q: err = %v, want %v", c.v, err, c.err)
		}
		if !bytes.Equal(d.Bytes(), loadBasic(t)) {
			t.Errorf("%q: file changed despite the error", c.v)
		}
	}
	// 80 multi-byte characters fit (the limit is in characters)
	d, _ := Parse(loadBasic(t))
	if err := d.SetTitle(strings.Repeat("ż", 80)); err != nil {
		t.Errorf("80 characters: %v", err)
	}
}

// the tail of the previous entry (a commented-out service indented deeper than the dash
// of the next element) must not travel with the next element.
const moveTailSrc = `groups:
  - name: G
    services:
      - name: B
        url: http://b
      # - name: Wyłączona
      #   url: http://old
  # komentarz H
  - name: H
    services:
      - name: X
        url: http://x
        # - ogon X
      - name: Y
        url: http://y
`

func TestMoveGroupPreviousGroupTailStays(t *testing.T) {
	d, err := Parse([]byte(moveTailSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveGroup("H", 0); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	iH := strings.Index(got, "  # komentarz H\n  - name: H")
	iG := strings.Index(got, "  - name: G")
	iTail := strings.Index(got, "      # - name: Wyłączona")
	if iH < 0 || iH > iG {
		t.Fatalf("comment H does not stand above H, before G:\n%s", got)
	}
	if iTail < iG {
		t.Fatalf("tail of group G escaped above H:\n%s", got)
	}
}

func TestMoveServicePreviousServiceTailStays(t *testing.T) {
	d, err := Parse([]byte(moveTailSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveServiceTo("H", "Y", "H", 0); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	iY := strings.Index(got, "- name: Y")
	iX := strings.Index(got, "- name: X")
	iTail := strings.Index(got, "# - ogon X")
	if iY > iX {
		t.Fatalf("Y is not first:\n%s", got)
	}
	if iTail < iX {
		t.Fatalf("tail of X went along with Y:\n%s", got)
	}
}

func TestRemoveServiceDoesNotTakePreviousTail(t *testing.T) {
	d, err := Parse([]byte(moveTailSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveService("H", "Y"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "# - ogon X") {
		t.Fatalf("tail of X vanished along with Y:\n%s", d.Bytes())
	}
}
