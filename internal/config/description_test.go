package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const descriptionSource = `# nagłówek
defaults: &defaults
  description: Wspólny opis
  tags: [wspólny]

groups:
  - name: G
    services:
      - name: A
        url: http://a
      - name: B
        url: http://b
        description: "Cytowany opis"   # komentarz zostaje
        tags: [x, y]   # i ten też
      - name: C
        url: http://c
        tags:
          - jeden
          - dwa
        check:
          interval: 5s
      - description: opis jako pierwszy klucz
        name: D
        url: http://d
      - name: E
        url: http://e
        <<: *defaults
`

func serviceFrom(t *testing.T, src []byte, name string) Service {
	t.Helper()
	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, src)
	}
	for _, g := range cfg.Groups {
		for _, s := range g.Services {
			if s.Name == name {
				return s
			}
		}
	}
	t.Fatalf("no service %q", name)
	return Service{}
}

func mustParse(t *testing.T, src string) *Document {
	t.Helper()
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLoadReadsDescriptionAndTags(t *testing.T) {
	src := []byte(descriptionSource)
	if s := serviceFrom(t, src, "A"); s.Description != "" || len(s.Tags) != 0 {
		t.Errorf("A without fields: %+v", s)
	}
	b := serviceFrom(t, src, "B")
	if b.Description != "Cytowany opis" || !reflect.DeepEqual([]string(b.Tags), []string{"x", "y"}) {
		t.Errorf("B: %+v", b)
	}
	if c := serviceFrom(t, src, "C"); !reflect.DeepEqual([]string(c.Tags), []string{"jeden", "dwa"}) {
		t.Errorf("C (block list): %v", c.Tags)
	}
	// inheritance via merge-key works the same as for `check`
	e := serviceFrom(t, src, "E")
	if e.Description != "Wspólny opis" || !reflect.DeepEqual([]string(e.Tags), []string{"wspólny"}) {
		t.Errorf("E (merge): %+v", e)
	}
}

func TestLoadTagsAsSingleStringAndNormalization(t *testing.T) {
	src := []byte(`groups:
  - name: G
    services:
      - name: Jeden
        url: http://a
        tags: media
      - name: Brudne
        url: http://b
        description: "  dwa   wiersze
          w jednym  "
        tags: ["  a ", "", "A", b]
`)
	if s := serviceFrom(t, src, "Jeden"); !reflect.DeepEqual([]string(s.Tags), []string{"media"}) {
		t.Errorf("tags: media → %v", s.Tags)
	}
	b := serviceFrom(t, src, "Brudne")
	if b.Description != "dwa wiersze w jednym" {
		t.Errorf("description not normalized: %q", b.Description)
	}
	if !reflect.DeepEqual([]string(b.Tags), []string{"a", "b"}) {
		t.Errorf("tags not normalized (trim, empty, duplicates regardless of case): %v", b.Tags)
	}
}

func TestNormalizeDescription(t *testing.T) {
	if v, err := NormalizeDescription("  Serwer mediów  "); err != nil || v != "Serwer mediów" {
		t.Errorf("trim: %q %v", v, err)
	}
	if v, err := NormalizeDescription(""); err != nil || v != "" {
		t.Errorf("empty is a valid \"remove\": %q %v", v, err)
	}
	if _, err := NormalizeDescription(strings.Repeat("ż", MaxDescription+1)); !errors.Is(err, ErrDescriptionTooLong) {
		t.Errorf("too long: %v", err)
	}
	if _, err := NormalizeDescription(strings.Repeat("ż", MaxDescription)); err != nil {
		t.Errorf("limit counted in characters, not bytes: %v", err)
	}
	for _, bad := range []string{"a\nb", "a\x00b", "a\u2028b", "a\tb"} {
		if _, err := NormalizeDescription(bad); !errors.Is(err, ErrDescriptionChars) {
			t.Errorf("%q: %v, want ErrDescriptionChars", bad, err)
		}
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{" media ", "", "Media", "4k"})
	if err != nil || !reflect.DeepEqual(got, []string{"media", "4k"}) {
		t.Errorf("%v %v", got, err)
	}
	if _, err := NormalizeTags([]string{strings.Repeat("a", MaxTag+1)}); !errors.Is(err, ErrTagsInvalid) {
		t.Errorf("tag too long: %v", err)
	}
	if _, err := NormalizeTags([]string{"a\nb"}); !errors.Is(err, ErrTagsInvalid) {
		t.Errorf("control character: %v", err)
	}
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	if _, err := NormalizeTags(many); !errors.Is(err, ErrTagsInvalid) {
		t.Errorf("too many tags: %v", err)
	}
}

// ── description: splice ──────────────────────────────────────────────────────

func TestSetDescriptionAppendsKeyAtBlockEnd(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceDescription("G", "C", "Nowy opis"); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	// C ends with a `check:` block — the key should land after it, at the service's indent
	if !strings.Contains(got, "          interval: 5s\n        description: Nowy opis\n      - description: opis jako") {
		t.Fatalf("description is not at the end of block C:\n%s", got)
	}
	if serviceFrom(t, d.Bytes(), "C").Description != "Nowy opis" {
		t.Error("Load does not see the description")
	}
}

func TestSetDescriptionReplacesOnlyValuePreservesQuotingAndComment(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceDescription("G", "B", "Inny"); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, `description: "Inny"   # komentarz zostaje`) {
		t.Fatalf("quoting or comment lost:\n%s", got)
	}
	changed := diffLines(t, descriptionSource, got)
	if len(changed) != 1 {
		t.Errorf("changed lines = %v, want 1", changed)
	}
}

func TestSetDescriptionQuotesWhenNeeded(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceDescription("G", "A", "Uwaga: to jest # test"); err != nil {
		t.Fatal(err)
	}
	if got := serviceFrom(t, d.Bytes(), "A").Description; got != "Uwaga: to jest # test" {
		t.Errorf("round-trip of a description with special characters: %q", got)
	}
}

func TestSetDescriptionEmptyRemovesLine(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceDescription("G", "B", ""); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if strings.Contains(got, "Cytowany opis") || strings.Contains(got, "komentarz zostaje") {
		t.Errorf("description line remained:\n%s", got)
	}
	if !strings.Contains(got, "        tags: [x, y]   # i ten też\n") {
		t.Errorf("neighbouring line suffered:\n%s", got)
	}
}

func TestSetDescriptionEmptyWhenDescriptionIsFirstKeyKeepsDash(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceDescription("G", "D", ""); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, "      - name: D\n        url: http://d\n      - name: E") {
		t.Fatalf("service D got derailed:\n%s", got)
	}
	if s := serviceFrom(t, d.Bytes(), "D"); s.Description != "" || s.URL != "http://d" {
		t.Errorf("D: %+v", s)
	}
}

// goccy rejects a local key next to the same key brought in by a merge
// ("duplicate key"), so overriding an inherited value is not possible —
// the panel must say so outright instead of writing a file that cannot be loaded.
func TestSetDescriptionRefusesWhenDescriptionComesFromMerge(t *testing.T) {
	d := mustParse(t, descriptionSource)
	for _, description := range []string{"", "Mój opis"} {
		if err := d.SetServiceDescription("G", "E", description); !errors.Is(err, ErrInheritedField) {
			t.Errorf("description %q: err = %v, want ErrInheritedField", description, err)
		}
	}
	if string(d.Bytes()) != descriptionSource {
		t.Error("file changed despite the refusal")
	}
}

func TestSetDescriptionNoService(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceDescription("G", "Nie ma", "x"); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("err = %v", err)
	}
}

// ── tags: splice ─────────────────────────────────────────────────────────────

func TestSetTagsNewKeyDefaultsToFlow(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceTags("G", "A", []string{"media", "4k"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "        url: http://a\n        tags: [media, 4k]\n") {
		t.Fatalf("flow list did not land after url:\n%s", d.Bytes())
	}
	if got := serviceFrom(t, d.Bytes(), "A").Tags; !reflect.DeepEqual([]string(got), []string{"media", "4k"}) {
		t.Errorf("Load: %v", got)
	}
}

func TestSetTagsNewKeyAsBlockWhenFileWritesThatWay(t *testing.T) {
	// the only tag list in the file is a block one (service C) — the new one should be block too
	d := mustParse(t, strings.ReplaceAll(descriptionSource, "        tags: [x, y]   # i ten też\n", ""))
	if err := d.SetServiceTags("G", "A", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "        url: http://a\n        tags:\n          - a\n          - b\n") {
		t.Fatalf("block list:\n%s", d.Bytes())
	}
	if got := serviceFrom(t, d.Bytes(), "A").Tags; !reflect.DeepEqual([]string(got), []string{"a", "b"}) {
		t.Errorf("Load: %v", got)
	}
}

func TestSetTagsReplacesFlowPreservesComment(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceTags("G", "B", []string{"z", "do not: panic"}); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, `tags: [z, "do not: panic"]   # i ten też`) {
		t.Fatalf("flow not replaced in place:\n%s", got)
	}
	if serviceFrom(t, d.Bytes(), "B").Tags[1] != "do not: panic" {
		t.Error("round-trip tagu ze znakami specjalnymi")
	}
}

func TestSetTagsQuotesFlowChars(t *testing.T) {
	d := mustParse(t, descriptionSource)
	tags := []string{"a,b", "c]d", "{e}", "yes", "123"}
	if err := d.SetServiceTags("G", "A", tags); err != nil {
		t.Fatal(err)
	}
	if got := serviceFrom(t, d.Bytes(), "A").Tags; !reflect.DeepEqual([]string(got), tags) {
		t.Errorf("round-trip: %v\n%s", got, d.Bytes())
	}
}

func TestSetTagsReplacesBlockList(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceTags("G", "C", []string{"trzy"}); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if !strings.Contains(got, "        tags:\n          - trzy\n        check:\n          interval: 5s\n") {
		t.Fatalf("block list:\n%s", got)
	}
	if strings.Contains(got, "jeden") || strings.Contains(got, "dwa") {
		t.Error("old items remained")
	}
}

func TestSetTagsEmptyRemovesKey(t *testing.T) {
	d := mustParse(t, descriptionSource)
	for _, n := range []string{"B", "C"} {
		if err := d.SetServiceTags("G", n, nil); err != nil {
			t.Fatal(err)
		}
		if s := serviceFrom(t, d.Bytes(), n); len(s.Tags) != 0 {
			t.Errorf("%s: tags remained: %v\n%s", n, s.Tags, d.Bytes())
		}
	}
	if !strings.Contains(string(d.Bytes()), "        check:\n          interval: 5s\n") {
		t.Errorf("C lost its check:\n%s", d.Bytes())
	}
}

func TestSetTagsRefusesWhenTagsComeFromMerge(t *testing.T) {
	d := mustParse(t, descriptionSource)
	for _, tags := range [][]string{nil, {"moje"}} {
		if err := d.SetServiceTags("G", "E", tags); !errors.Is(err, ErrInheritedField) {
			t.Errorf("tags %v: err = %v, want ErrInheritedField", tags, err)
		}
	}
	if string(d.Bytes()) != descriptionSource {
		t.Error("file changed despite the refusal")
	}
}

func TestServiceWithoutMergeCanGetDescriptionAndRemoveIt(t *testing.T) {
	// the merge carries ONLY `check`, so description and tags are local — no refusal
	src := `defaults: &d
  check: {interval: 5s}
groups:
  - name: G
    services:
      - name: S
        url: http://s
        <<: *d
`
	d := mustParse(t, src)
	if err := d.SetServiceDescription("G", "S", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceTags("G", "S", []string{"t"}); err != nil {
		t.Fatal(err)
	}
	s := serviceFrom(t, d.Bytes(), "S")
	if s.Description != "ok" || len(s.Tags) != 1 || s.Check.Interval == 0 {
		t.Errorf("%+v\n%s", s, d.Bytes())
	}
	if err := d.SetServiceDescription("G", "S", ""); err != nil {
		t.Fatal(err)
	}
	if serviceFrom(t, d.Bytes(), "S").Description != "" {
		t.Error("description not removed")
	}
}

func TestSetTagsRejectsBadInput(t *testing.T) {
	d := mustParse(t, descriptionSource)
	if err := d.SetServiceTags("G", "A", []string{"a\nb"}); !errors.Is(err, ErrTagsInvalid) {
		t.Errorf("err = %v", err)
	}
	if string(d.Bytes()) != descriptionSource {
		t.Error("file changed despite the error")
	}
}

// ── other places that must know the new fields ───────────────────────────────

func TestAddServiceWritesDescriptionAndTags(t *testing.T) {
	d := mustParse(t, descriptionSource)
	err := d.AddService("G", Service{Name: "Nowa", URL: "http://n", Description: "Opis: x", Tags: Tags{"a", "b c"}})
	if err != nil {
		t.Fatal(err)
	}
	s := serviceFrom(t, d.Bytes(), "Nowa")
	if s.Description != "Opis: x" || !reflect.DeepEqual([]string(s.Tags), []string{"a", "b c"}) {
		t.Errorf("%+v\n%s", s, d.Bytes())
	}
}

func TestMovingServiceTakesDescriptionAndTags(t *testing.T) {
	src := descriptionSource + `  - name: H
    services: []
`
	d := mustParse(t, src)
	if err := d.MoveService("G", "B", "H"); err != nil {
		t.Fatal(err)
	}
	s := serviceFrom(t, d.Bytes(), "B")
	if s.Description != "Cytowany opis" || len(s.Tags) != 2 {
		t.Errorf("%+v\n%s", s, d.Bytes())
	}
}
