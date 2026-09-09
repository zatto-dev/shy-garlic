package config

import (
	"strings"
	"testing"
)

func TestDiffPustyGdyBezZmian(t *testing.T) {
	src := wczytajBasic(t)
	if d := Diff(src, src, 3); d != "" {
		t.Fatalf("chcę pustego diffa, mam:\n%s", d)
	}
}

func TestDiffPokazujeDokładnieToCoZmienione(t *testing.T) {
	src := wczytajBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://192.168.0.195:8990"); err != nil {
		t.Fatal(err)
	}

	diff := Diff(src, d.Bytes(), 2)
	dodane, usunięte := PodsumujDiff(diff)
	if dodane != 1 || usunięte != 1 {
		t.Fatalf("chcę +1/-1, mam +%d/-%d:\n%s", dodane, usunięte, diff)
	}
	if !strings.Contains(diff, "-        url: http://192.168.0.195:8989") ||
		!strings.Contains(diff, "+        url: http://192.168.0.195:8990") {
		t.Fatalf("diff nie pokazuje zmiany URL:\n%s", diff)
	}
	// kontekst ma być widoczny, żeby user wiedział, gdzie to jest
	if !strings.Contains(diff, ` icon: sonarr`) {
		t.Errorf("brak kontekstu wokół zmiany:\n%s", diff)
	}
	if !strings.HasPrefix(diff, "@@ ") {
		t.Errorf("brak nagłówka hunka:\n%s", diff)
	}
}

func TestDiffDodaniaUsługi(t *testing.T) {
	src := wczytajBasic(t)
	d, _ := Parse(src)
	if err := d.AddService("Sieć", Service{Name: "AdGuard", URL: "http://192.168.0.29:3000"}); err != nil {
		t.Fatal(err)
	}
	diff := Diff(src, d.Bytes(), 3)
	dodane, usunięte := PodsumujDiff(diff)
	if usunięte != 0 {
		t.Errorf("dodanie usługi nie może niczego usuwać, mam -%d:\n%s", usunięte, diff)
	}
	if dodane != 2 {
		t.Errorf("chcę +2 (name, url), mam +%d:\n%s", dodane, diff)
	}
}

func TestDiffNaPustymIWielkim(t *testing.T) {
	if d := Diff(nil, []byte("a\nb\n"), 3); !strings.Contains(d, "+a") || !strings.Contains(d, "+b") {
		t.Fatalf("diff z pustego pliku:\n%s", d)
	}
	if d := Diff([]byte("a\nb\n"), nil, 3); !strings.Contains(d, "-a") {
		t.Fatalf("diff do pustego pliku:\n%s", d)
	}
}
