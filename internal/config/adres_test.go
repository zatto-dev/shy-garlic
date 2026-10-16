package config

import "testing"

func TestPoprawnyAdresUsługi(t *testing.T) {
	for _, c := range []struct {
		adres      string
		ok, klikal bool
	}{
		{"", true, false}, // usługa bez sprawdzania
		{"http://x", true, true},
		{"https://x.example:8443/p?q=1", true, true},
		{"HTTPS://X.EXAMPLE", true, true},
		{"tcp://10.0.0.5:22", true, false},
		{"javascript:alert(1)", false, false},
		{"JaVaScRiPt:alert(1)", false, false},
		{"data:text/html,x", false, false},
		{"vbscript:x", false, false},
		{"file:///etc/passwd", false, false},
		{"ftp://h/x", false, false},
		{"//host/x", false, false},
		{"host:80", false, false},
		{"http://", false, false},
		{"https:///x", false, false},
		{" https://x", false, false},
		{"https://x\n", false, false},
		{"http://x/\x00", false, false},
	} {
		if got := PoprawnyAdresUsługi(c.adres); got != c.ok {
			t.Errorf("PoprawnyAdresUsługi(%q) = %v, chcę %v", c.adres, got, c.ok)
		}
		if got := AdresKlikalny(c.adres); got != c.klikal {
			t.Errorf("AdresKlikalny(%q) = %v, chcę %v", c.adres, got, c.klikal)
		}
	}
}
