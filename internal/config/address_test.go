package config

import "testing"

func TestValidServiceAddress(t *testing.T) {
	for _, c := range []struct {
		address       string
		ok, clickable bool
	}{
		{"", true, false}, // service without a check
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
		if got := ValidServiceAddress(c.address); got != c.ok {
			t.Errorf("ValidServiceAddress(%q) = %v, want %v", c.address, got, c.ok)
		}
		if got := ClickableAddress(c.address); got != c.clickable {
			t.Errorf("ClickableAddress(%q) = %v, want %v", c.address, got, c.clickable)
		}
	}
}
