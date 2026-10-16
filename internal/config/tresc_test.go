package config

import (
	"strings"
	"testing"
)

func TestLoadCzytaKontrolęTreści(t *testing.T) {
	src := []byte(`defaults: &d
  check:
    path: /healthz
    contains: ok
groups:
  - name: G
    services:
      - name: A
        url: http://a
        check:
          path: /api/health?deep=1
          contains: '"status":"ok"'
          not_contains: maintenance
      - name: B
        url: http://b
        <<: *d
`)
	a := usługa(t, src, "A").Check
	if a.Path != "/api/health?deep=1" || a.Contains != `"status":"ok"` || a.NotContains != "maintenance" {
		t.Errorf("A: %+v", a)
	}
	// dziedziczenie przez merge-key działa jak dla reszty `check`
	b := usługa(t, src, "B").Check
	if b.Path != "/healthz" || b.Contains != "ok" {
		t.Errorf("B: %+v", b)
	}
	// reszta check dostaje domyślne tak jak dotąd
	if a.Interval == 0 || len(a.Expect) == 0 {
		t.Errorf("domyślne: %+v", a)
	}
}

func TestLoadPathBezUkośnikaDostajeUkośnik(t *testing.T) {
	src := []byte(`groups:
  - name: G
    services:
      - name: A
        url: http://a
        check: {path: health}
`)
	if got := usługa(t, src, "A").Check.Path; got != "/health" {
		t.Errorf("path = %q, chcę /health", got)
	}
}

func TestAddServiceZapisujeKontrolęTreści(t *testing.T) {
	d := parsuj(t, źródłoOpisów)
	err := d.AddService("G", Service{Name: "Nowa", URL: "http://n",
		Check: Check{Interval: Duration(10e9), Path: "/h", Contains: "ok: yes", NotContains: "err"}})
	if err != nil {
		t.Fatal(err)
	}
	c := usługa(t, d.Bytes(), "Nowa").Check
	if c.Path != "/h" || c.Contains != "ok: yes" || c.NotContains != "err" {
		t.Errorf("%+v\n%s", c, d.Bytes())
	}
	if !strings.Contains(string(d.Bytes()), "          path: /h\n") {
		t.Errorf("wcięcie:\n%s", d.Bytes())
	}
}
