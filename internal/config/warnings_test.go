package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// SHYG-45: a typo in a key (`chek:`) was silently ignored. It must not be
// an error (a file with an unknown field must still load), but it must be visible.

func warningKeys(w []Warning) string {
	var out []string
	for _, o := range w {
		out = append(out, fmt.Sprintf("%d:%s@%s", o.Line, o.Key, o.Path))
	}
	return strings.Join(out, "|")
}

func TestWarningsUnknownKey(t *testing.T) {
	src := `title: Dom
groups:
  - name: Media
    services:
      - name: Sonarr
        url: http://x
      - name: Radarr
        url: http://y
        chek:
          interval: 5s
`
	w := Warnings([]byte(src))
	if got := warningKeys(w); got != "9:chek@groups[0].services[1]" {
		t.Fatalf("warnings = %q", got)
	}
	if w[0].Suggestion != "check" {
		t.Errorf("suggestion = %q, want check", w[0].Suggestion)
	}
	// Load still accepts the file — it is a warning, not an error
	if _, err := Load([]byte(src)); err != nil {
		t.Errorf("Load rejected a file with an unknown key: %v", err)
	}
}

func TestWarningsAllLevels(t *testing.T) {
	src := `titel: x
appearance:
  view: grid
  widok2: a
  background:
    preset: aurora
    blurr: 3
notify:
  treshold: 2
groups:
  - name: A
    nme: b
    services:
      - name: S
        check:
          intervall: 5s
          expect: [200]
`
	got := warningKeys(Warnings([]byte(src)))
	want := "1:titel@|4:widok2@appearance|7:blurr@appearance.background|9:treshold@notify|12:nme@groups[0]|16:intervall@groups[0].services[0].check"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

// README pattern: `defaults: &defaults` + `<<: *defaults`, and also x-*.
func TestWarningsNoFalseAlarms(t *testing.T) {
	for _, p := range []string{"../../examples/homelab.yaml", "testdata/basic.yaml", "testdata/appearance.yaml"} {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if w := Warnings(src); len(w) != 0 {
			t.Errorf("%s: false warnings %s", p, warningKeys(w))
		}
	}
	src := `x-wspólne: &w
  interval: 10s
groups:
  - name: A
    services:
      - name: S
        check:
          <<: *w
          timeout: 2s
      - &szablon
        name: T
        url: http://t
      - <<: *szablon
        name: U
`
	if w := Warnings([]byte(src)); len(w) != 0 {
		t.Errorf("anchors/merge keys: warnings %s", warningKeys(w))
	}
}

// A typo hidden in an anchor is visible too — once, despite many merges.
func TestWarningsInAnchorAfterMerge(t *testing.T) {
	src := `defaults: &d
  intervall: 5s
groups:
  - name: A
    services:
      - name: S1
        check: {<<: *d}
      - name: S2
        check: {<<: *d}
`
	w := Warnings([]byte(src))
	if len(w) != 1 || w[0].Key != "intervall" || w[0].Line != 2 {
		t.Fatalf("warnings = %s, want one for line 2", warningKeys(w))
	}
}

func TestWarningsInvalidFileMeansEmptyWarnings(t *testing.T) {
	if w := Warnings([]byte("groups: [\n")); len(w) != 0 {
		t.Errorf("broken YAML must yield an empty list (Load reports the error), got %v", w)
	}
}

func TestAddressWarnings(t *testing.T) {
	cfg, err := Load([]byte(`groups:
  - name: G
    services:
      - name: Zly
        url: "javascript:alert(1)"
      - name: Ok
        url: https://ok.example
      - name: Tcp
        url: tcp://10.0.0.5:22
      - name: Pusty
`))
	if err != nil {
		t.Fatal(err)
	}
	w := AddressWarnings(cfg)
	if len(w) != 1 || w[0].Kind != KindAddress || w[0].Service != "Zly" || w[0].Group != "G" {
		t.Fatalf("address warnings = %+v", w)
	}
}

// ── error line number ────────────────────────────────────────────────────────

func TestParseFileErrorReportsLine(t *testing.T) {
	for name, c := range map[string]struct {
		src  string
		line int
	}{
		"tabulator": {"title: x\ngroups:\n\t- name: A\n", 3},
		"quote":     {"title: \"niezamknięty\ngroups: []\n", 1},
		"bad type":  {"title: x\ngroups: 5\n", 2},
		"duration":  {"groups:\n  - name: A\n    services:\n      - name: S\n        check:\n          interval: abc\n", 6},
		"bracket":   {"groups: [\n  - a\n", 0}, // line unknown, but the message is there
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load([]byte(c.src))
			if err == nil {
				t.Fatal("Load accepted a broken file")
			}
			b := ParseFileError(err)
			if b.Message == "" {
				t.Error("pusty komunikat")
			}
			if strings.Contains(b.Message, "\n") {
				t.Errorf("multi-line message (a source fragment crept in): %q", b.Message)
			}
			if c.line > 0 && b.Line != c.line {
				t.Errorf("line = %d, want %d (message %q)", b.Line, c.line, b.Message)
			}
		})
	}
}

// Every model field must be in the schema — otherwise a valid key gets
// a warning. A full file with all fields must not trigger any.
func TestWarningsCoverModel(t *testing.T) {
	src := `title: T
appearance:
  view: grid
  background: {preset: photo, image: a.png, blur: 1, dim: 2, tone: dark}
notify: {threshold: 3}
discovery: {docker: false}
groups:
  - name: G
    services:
      - name: S
        url: http://s
        icon: i
        size: md
        description: d
        tags: [a]
        check: {interval: 5s, timeout: 2s, expect: [200], insecure: true, path: /h, contains: ok, not_contains: err}
`
	if w := Warnings([]byte(src)); len(w) != 0 {
		t.Fatalf("valid keys reported as unknown: %s", warningKeys(w))
	}
}
