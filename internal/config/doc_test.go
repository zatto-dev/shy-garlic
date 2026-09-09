package config

import (
	"bytes"
	"os"
	"testing"
)

func wczytajBasic(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/basic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// Najważniejszy test w projekcie: sam odczyt nie może ruszyć ani jednego bajtu.
func TestParseIBytesSąBajtIdentyczne(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Fatal("Bytes() różni się od wejścia — silnik nie jest round-trip-safe")
	}
}

func TestValueRangeWskazujeWartość(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path []any
		want string
	}{
		{[]any{"groups", 0, "services", 1, "url"}, "http://192.168.0.195:8989"},
		{[]any{"groups", 0, "services", 1, "name"}, `"Sonarr"`}, // cudzysłowy w zakresie
		{[]any{"groups", 0, "services", 0, "name"}, "Jellyfin"},
		{[]any{"title"}, "Homelab Mariusza"},                             // komentarz odcięty
		{[]any{"groups", 1, "services", 0, "check", "interval"}, "10s"},  // komentarz odcięty
		{[]any{"defaults", "check", "expect"}, "[200, 301, 302, 401, 403]"},
		{[]any{"groups", 1, "name"}, "Sieć"}, // wielobajtowy znak w linii
	}

	for _, c := range cases {
		start, end, err := d.valueRange(c.path...)
		if err != nil {
			t.Errorf("valueRange(%v): %v", c.path, err)
			continue
		}
		if got := string(src[start:end]); got != c.want {
			t.Errorf("valueRange(%v) = %q, chcę %q", c.path, got, c.want)
		}
	}
}

func TestValueRangeZgłaszaBrakŚcieżki(t *testing.T) {
	d, err := Parse(wczytajBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.valueRange("groups", 9, "name"); err == nil {
		t.Fatal("chcę błędu dla nieistniejącego indeksu")
	}
}
