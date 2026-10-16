package config

import (
	"os"
	"testing"
)

// TestDemoDiff is not a regression test — it prints the result of an operation
// for inspection: `go test -run TestDemoDiff -v`. It stays in the repo as living documentation.
func TestDemoDiff(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Sieć", Service{Name: "AdGuard", URL: "http://10.0.0.2:3000", Icon: "adguard-home"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://10.0.0.5:8990"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/shyg-demo-out.yaml", d.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("written /tmp/shyg-demo-out.yaml (%d bytes, input %d)", len(d.Bytes()), len(src))
}
