package config

import (
	"strings"
	"testing"
)

func TestLoadReadsContentCheck(t *testing.T) {
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
	a := serviceFrom(t, src, "A").Check
	if a.Path != "/api/health?deep=1" || a.Contains != `"status":"ok"` || a.NotContains != "maintenance" {
		t.Errorf("A: %+v", a)
	}
	// inheritance via merge-key works like for the rest of `check`
	b := serviceFrom(t, src, "B").Check
	if b.Path != "/healthz" || b.Contains != "ok" {
		t.Errorf("B: %+v", b)
	}
	// the rest of check gets defaults as before
	if a.Interval == 0 || len(a.Expect) == 0 {
		t.Errorf("defaults: %+v", a)
	}
}

func TestLoadPathWithoutSlashGetsSlash(t *testing.T) {
	src := []byte(`groups:
  - name: G
    services:
      - name: A
        url: http://a
        check: {path: health}
`)
	if got := serviceFrom(t, src, "A").Check.Path; got != "/health" {
		t.Errorf("path = %q, want /health", got)
	}
}

func TestAddServiceWritesContentCheck(t *testing.T) {
	d := mustParse(t, descriptionSource)
	err := d.AddService("G", Service{Name: "Nowa", URL: "http://n",
		Check: Check{Interval: Duration(10e9), Path: "/h", Contains: "ok: yes", NotContains: "err"}})
	if err != nil {
		t.Fatal(err)
	}
	c := serviceFrom(t, d.Bytes(), "Nowa").Check
	if c.Path != "/h" || c.Contains != "ok: yes" || c.NotContains != "err" {
		t.Errorf("%+v\n%s", c, d.Bytes())
	}
	if !strings.Contains(string(d.Bytes()), "          path: /h\n") {
		t.Errorf("indent:\n%s", d.Bytes())
	}
}
