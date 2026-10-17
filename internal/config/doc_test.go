package config

import (
	"bytes"
	"os"
	"testing"
)

func loadBasic(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/basic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// The most important test in the project: reading alone must not touch a single byte.
func TestParseAndBytesAreByteIdentical(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Fatal("Bytes() differs from the input — the engine is not round-trip-safe")
	}
}

func TestValueRangePointsAtValue(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path []any
		want string
	}{
		{[]any{"groups", 0, "services", 1, "url"}, "http://10.0.0.5:8989"},
		{[]any{"groups", 0, "services", 1, "name"}, `"Sonarr"`}, // quotes in the range
		{[]any{"groups", 0, "services", 0, "name"}, "Jellyfin"},
		{[]any{"title"}, "Homelab Demo"},                                // comment cut off
		{[]any{"groups", 1, "services", 0, "check", "interval"}, "10s"}, // comment cut off
		{[]any{"defaults", "check", "expect"}, "[200, 301, 302, 401, 403]"},
		{[]any{"groups", 1, "name"}, "Sieć"}, // multi-byte character in the line
	}

	for _, c := range cases {
		start, end, err := d.valueRange(c.path...)
		if err != nil {
			t.Errorf("valueRange(%v): %v", c.path, err)
			continue
		}
		if got := string(src[start:end]); got != c.want {
			t.Errorf("valueRange(%v) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestValueRangeReportsMissingPath(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.valueRange("groups", 9, "name"); err == nil {
		t.Fatal("want an error for a nonexistent index")
	}
}
