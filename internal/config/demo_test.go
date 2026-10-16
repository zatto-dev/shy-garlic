package config

import (
	"os"
	"testing"
)

// TestDemoDiff nie jest testem regresji — wypisuje wynik operacji do wglądu:
// `go test -run TestDemoDiff -v`. Zostaje w repo jako żywa dokumentacja.
func TestDemoDiff(t *testing.T) {
	src := wczytajBasic(t)
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
	t.Logf("zapisano /tmp/shyg-demo-out.yaml (%d bajtów, wejście %d)", len(d.Bytes()), len(src))
}
