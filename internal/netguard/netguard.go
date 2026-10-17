// Package netguard keeps the panel's outbound connections away from cloud
// metadata endpoints.
//
// Health checks and webhooks go wherever the (signed-in) user points them, and
// private LAN addresses are the whole point of a homelab panel — so those stay
// reachable. Link-local space is different: 169.254.169.254 serves instance
// credentials on every major cloud, and nothing legitimate in a homelab lives there.
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

var blocked = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),    // IPv4 link-local, incl. 169.254.169.254 metadata
	netip.MustParsePrefix("fe80::/10"),         // IPv6 link-local
	netip.MustParsePrefix("192.0.0.192/32"),    // Oracle Cloud metadata
	netip.MustParsePrefix("168.63.129.16/32"),  // Azure WireServer / metadata
	netip.MustParsePrefix("fd00:ec2::254/128"), // AWS IPv6 metadata endpoint
}

// Blocked says whether connections to ip are refused.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	for _, p := range blocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Control is a net.Dialer.Control hook. It runs on the address that was
// actually chosen after DNS resolution, so a name that resolves to a link-local
// address (or flips to one later) is refused too.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("unrecognized address %q", host)
	}
	if Blocked(ip) {
		return fmt.Errorf("link-local address %s is blocked", ip.WithZone(""))
	}
	return nil
}

// Dialer returns a dialer with the guard installed.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: Control}
}
