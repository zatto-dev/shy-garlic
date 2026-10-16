package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// GH #6: editing a service lost changes in the "Checking" section.

const checkSource = `defaults: &defaults
  check:
    insecure: true

groups:
  - name: G
    services:
      - name: Goła
        url: https://a.home.arpa
      - name: Blok
        url: https://b.home.arpa
        check:
          interval: 1m   # rzadziej, bo wolna
          expect:
            - 200
            - 302
          path: /health
      - name: Flow
        url: https://c.home.arpa
        check: {timeout: 10s, insecure: true}
      - name: Dziedziczy
        url: https://d.home.arpa
        <<: *defaults
      - name: Tylko
        url: https://e.home.arpa
        check:
          insecure: true
`

func ptrTrue() *bool                        { v := true; return &v }
func ptrFalse() *bool                       { v := false; return &v }
func ptrDuration(d time.Duration) *Duration { v := Duration(d); return &v }
func ptrCodes(k ...int) *[]int              { return &k }

func TestSetCheckAppendsBlockWhenMissing(t *testing.T) {
	d := mustParse(t, checkSource)
	if err := d.SetCheck("G", "Goła", CheckChange{Insecure: ptrTrue()}); err != nil {
		t.Fatal(err)
	}
	want := "        url: https://a.home.arpa\n        check:\n          insecure: true\n      - name: Blok"
	if !strings.Contains(string(d.Bytes()), want) {
		t.Fatalf("missing check block:\n%s", d.Bytes())
	}
	if !serviceFrom(t, d.Bytes(), "Goła").Check.Insecure {
		t.Fatal("insecure was not loaded")
	}
}

func TestSetCheckReplacesAndAppendsInBlock(t *testing.T) {
	d := mustParse(t, checkSource)
	err := d.SetCheck("G", "Blok", CheckChange{
		Interval: ptrDuration(2 * time.Minute), Expect: ptrCodes(200, 401), Insecure: ptrTrue()})
	if err != nil {
		t.Fatal(err)
	}
	src := string(d.Bytes())
	for _, w := range []string{"interval: 2m0s   # rzadziej, bo wolna", "expect: [200, 401]",
		"insecure: true", "path: /health"} {
		if !strings.Contains(src, w) {
			t.Errorf("missing %q:\n%s", w, src)
		}
	}
	c := serviceFrom(t, d.Bytes(), "Blok").Check
	if c.Interval != Duration(2*time.Minute) || !reflect.DeepEqual(c.Expect, []int{200, 401}) ||
		!c.Insecure || c.Path != "/health" {
		t.Fatalf("%+v", c)
	}
}

func TestSetCheckUncheckRemovesKey(t *testing.T) {
	d := mustParse(t, checkSource)
	if err := d.SetCheck("G", "Blok", CheckChange{Interval: ptrDuration(0), Expect: ptrCodes()}); err != nil {
		t.Fatal(err)
	}
	src := string(d.Bytes())
	if strings.Contains(src, "rzadziej") || strings.Contains(src, "- 302") {
		t.Fatalf("keys remained:\n%s", src)
	}
	if c := serviceFrom(t, d.Bytes(), "Blok").Check; c.Path != "/health" || c.Interval != Duration(DefaultInterval) {
		t.Fatalf("%+v", c)
	}
}

func TestSetCheckLastKeyRemovesBlock(t *testing.T) {
	d := mustParse(t, checkSource)
	if err := d.SetCheck("G", "Tylko", CheckChange{Insecure: ptrFalse()}); err != nil {
		t.Fatal(err)
	}
	if want := "        url: https://e.home.arpa\n"; !strings.HasSuffix(string(d.Bytes()), want) {
		t.Fatalf("empty check remained:\n%s", d.Bytes())
	}
}

func TestSetCheckFlowRewritesBlock(t *testing.T) {
	d := mustParse(t, checkSource)
	if err := d.SetCheck("G", "Flow", CheckChange{Insecure: ptrFalse()}); err != nil {
		t.Fatal(err)
	}
	c := serviceFrom(t, d.Bytes(), "Flow").Check
	if c.Insecure || c.Timeout != Duration(10*time.Second) {
		t.Fatalf("%+v\n%s", c, d.Bytes())
	}
}

func TestSetCheckInheritedRefuses(t *testing.T) {
	d := mustParse(t, checkSource)
	before := string(d.Bytes())
	err := d.SetCheck("G", "Dziedziczy", CheckChange{Interval: ptrDuration(time.Minute)})
	if !errors.Is(err, ErrInheritedField) {
		t.Fatalf("err = %v", err)
	}
	if string(d.Bytes()) != before {
		t.Fatal("file changed")
	}
}

func TestSetCheckNoChangesTouchesNothing(t *testing.T) {
	d := mustParse(t, checkSource)
	before := string(d.Bytes())
	err := d.SetCheck("G", "Blok", CheckChange{
		Interval: ptrDuration(time.Minute), Expect: ptrCodes(200, 302), Insecure: ptrFalse()})
	if err != nil {
		t.Fatal(err)
	}
	if string(d.Bytes()) != before {
		t.Fatalf("change without change:\n%s", d.Bytes())
	}
}

// `interval: 1m  # comment` loaded as 1s — goccy gave the scalar's bytes
// together with the comment.
func TestDurationWithCommentAndBareNumber(t *testing.T) {
	for entry, want := range map[string]time.Duration{
		"interval: 1m   # x":   time.Minute,
		"interval: 1m":         time.Minute,
		`interval: "45s" # x`:  45 * time.Second,
		"interval: 30":         30 * time.Second,
		"interval: 1.5   # x":  1500 * time.Millisecond,
		"interval: 2h0m0s # x": 2 * time.Hour,
	} {
		src := "groups:\n  - name: G\n    services:\n      - name: A\n        url: http://a\n        check:\n          " + entry + "\n"
		if got := serviceFrom(t, []byte(src), "A").Check.Interval; got != Duration(want) {
			t.Errorf("%q: %v, want %v", entry, got, want)
		}
	}
}
