package config

import (
	"errors"
	"testing"
)

// Regressions found by the fuzz targets (SHYG-20).

func TestLoadReturnsErrorInsteadOfLibraryPanic(t *testing.T) {
	src := "groups:\n  - name: G\n    services:\n      - name: A\n        url: http://a\n        check:\n          expect: !x\n"
	if _, err := Load([]byte(src)); err == nil {
		t.Fatal("want a load error")
	}
}

func TestParseRefusesLoneCR(t *testing.T) {
	src := "title: x\rgroups:\n  - name: G\n    services:\n      - name: A\n"
	if _, err := Parse([]byte(src)); !errors.Is(err, errLoneCR) {
		t.Fatalf("err = %v", err)
	}
	// CRLF and LF still parse
	for _, ok := range []string{"title: x\r\ngroups: []\r\n", "title: x\ngroups: []\n"} {
		if _, err := Parse([]byte(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestDescriptionCollapsesSpacesLikeLoad(t *testing.T) {
	got, err := NormalizeDescription("  two  spaces   here ")
	if err != nil || got != "two spaces here" {
		t.Fatalf("%q, %v", got, err)
	}
	if _, err := NormalizeDescription("line\nbreak"); !errors.Is(err, ErrDescriptionChars) {
		t.Fatalf("newline must stay rejected, err = %v", err)
	}
}
