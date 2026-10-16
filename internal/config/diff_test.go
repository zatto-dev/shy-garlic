package config

import (
	"strings"
	"testing"
)

func TestDiffEmptyWhenNoChanges(t *testing.T) {
	src := loadBasic(t)
	if d := Diff(src, src, 3); d != "" {
		t.Fatalf("want empty diff, got:\n%s", d)
	}
}

func TestDiffShowsExactlyWhatChanged(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://10.0.0.5:8990"); err != nil {
		t.Fatal(err)
	}

	diff := Diff(src, d.Bytes(), 2)
	added, removed := SummarizeDiff(diff)
	if added != 1 || removed != 1 {
		t.Fatalf("want +1/-1, got +%d/-%d:\n%s", added, removed, diff)
	}
	if !strings.Contains(diff, "-        url: http://10.0.0.5:8989") ||
		!strings.Contains(diff, "+        url: http://10.0.0.5:8990") {
		t.Fatalf("diff does not show the URL change:\n%s", diff)
	}
	// context should be visible so the user knows where it is
	if !strings.Contains(diff, ` icon: sonarr`) {
		t.Errorf("no context around the change:\n%s", diff)
	}
	if !strings.HasPrefix(diff, "@@ ") {
		t.Errorf("no hunk header:\n%s", diff)
	}
}

func TestDiffServiceAddition(t *testing.T) {
	src := loadBasic(t)
	d, _ := Parse(src)
	if err := d.AddService("Sieć", Service{Name: "AdGuard", URL: "http://10.0.0.2:3000"}); err != nil {
		t.Fatal(err)
	}
	diff := Diff(src, d.Bytes(), 3)
	added, removed := SummarizeDiff(diff)
	if removed != 0 {
		t.Errorf("adding a service must not remove anything, got -%d:\n%s", removed, diff)
	}
	if added != 2 {
		t.Errorf("want +2 (name, url), got +%d:\n%s", added, diff)
	}
}

func TestDiffOnEmptyAndLarge(t *testing.T) {
	if d := Diff(nil, []byte("a\nb\n"), 3); !strings.Contains(d, "+a") || !strings.Contains(d, "+b") {
		t.Fatalf("diff from an empty file:\n%s", d)
	}
	if d := Diff([]byte("a\nb\n"), nil, 3); !strings.Contains(d, "-a") {
		t.Fatalf("diff to an empty file:\n%s", d)
	}
}
