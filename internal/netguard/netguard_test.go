package netguard

import (
	"net/netip"
	"testing"
)

func TestBlocked(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"fe80::1", true},
		{"fe80::1%eth0", true},
		{"febf::1", true},
		{"fd00:ec2::254", true},
		{"169.253.255.255", false},
		{"169.255.0.0", false},
		{"fec0::1", false},
		{"192.168.1.10", false}, // the LAN is the point of a homelab panel
		{"10.0.0.5", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"8.8.8.8", false},
	}
	for _, c := range cases {
		if got := Blocked(netip.MustParseAddr(c.ip)); got != c.want {
			t.Errorf("Blocked(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestControlRefusesLinkLocal(t *testing.T) {
	if err := Control("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("169.254.169.254 must be refused")
	}
	if err := Control("tcp", "[fe80::1%eth0]:80", nil); err == nil {
		t.Error("fe80::1 must be refused")
	}
	if err := Control("tcp", "192.168.1.1:80", nil); err != nil {
		t.Errorf("LAN address refused: %v", err)
	}
}
