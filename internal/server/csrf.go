package server

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Protection against requests from a foreign site and against blind trust in proxy headers.
//
// ! multipart/form-data is one of the three "simple request" types, so the
// wallpaper upload endpoint does NOT get the accidental preflight protection that
// today's JSON requests have. One layer closes this together with the holes the
// panel has today: POST /setup without a cookie on a fresh instance (auto-submit
// from a foreign site = creating an administrator account), CSRF on /login and
// /logout, and POST /api/check and /api/notify/test, which have no body to check.

// requireSameOrigin lets reads through, and mutations only from our own origin.
func requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !fromSameOrigin(r) {
			plainError(w, r, http.StatusForbidden, "error_cross_origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fromSameOrigin reads three sources, from the most reliable. Sec-Fetch-Site is
// sent by every live browser and cannot be forged from JS; Origin and
// Referer are a fallback for clients without it (curl, old engines, scripts).
func fromSameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "same-site", "cross-site":
		// ! a neighbour on the same registrable domain is the typical homelab topology
		// and exactly the case SameSite=Lax does not catch
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		return sameHost(o, r)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return sameHost(ref, r)
	}
	return false
}

func sameHost(raw string, r *http.Request) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && !overHTTPS(r)
}

// ── trusted proxy ────────────────────────────────────────────────────────────

// trustedProxy — networks allowed to say X-Forwarded-*. EMPTY by default.
//
// ! Without this list the login attempt limiter could be bypassed with a single
// header per request (each with a different X-Forwarded-For is a different
// "address"), and X-Forwarded-Proto allowed forcing or removing the Secure flag
// on the session cookie.
var trustedProxy []netip.Prefix

// SetTrustedProxy accepts a list of CIDRs (bare addresses are allowed too).
func SetTrustedProxy(list []string) error {
	out, err := parsePrefixes("zaufane proxy", list)
	if err != nil {
		return err
	}
	trustedProxy = out
	return nil
}

// parsePrefixes turns a list of CIDRs and bare addresses into prefixes; empty
// entries and spaces are skipped.
func parsePrefixes(what string, list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, err2 := netip.ParseAddr(s)
			if err2 != nil {
				return nil, fmt.Errorf("%s %q: %w", what, s, err)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p)
	}
	return out, nil
}

func inPrefixes(a netip.Addr, list []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func connAddress(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func fromTrustedProxy(r *http.Request) bool {
	if len(trustedProxy) == 0 {
		return false
	}
	a, ok := connAddress(r)
	return ok && inPrefixes(a, trustedProxy)
}

// clientAddress determines who is really connecting to the panel.
//
// A connection from an untrusted peer: its address, proxy headers ignored.
// From a trusted proxy: X-Forwarded-For read FROM THE RIGHT — each proxy appends
// at the end the address it received the request from, so the rightmost entry that
// is not a trusted proxy is the client as seen by the outermost trusted proxy.
// Entries on the left can be made up by the client, so they have no say.
//
// ok == false: the header is unreadable (junk instead of an address). The caller
// should then refuse rather than guess; addr holds the connection address, for limits.
func clientAddress(r *http.Request) (addr netip.Addr, ok bool) {
	peer, _ := connAddress(r)
	if !fromTrustedProxy(r) {
		return peer, peer.IsValid()
	}
	var entries []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(h, ",") {
			if e = strings.TrimSpace(e); e != "" {
				entries = append(entries, e)
			}
		}
	}
	if len(entries) == 0 {
		return peer, peer.IsValid()
	}
	for i := len(entries) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(entries[i])
		if err != nil {
			return peer, false
		}
		a = a.Unmap()
		if inPrefixes(a, trustedProxy) {
			if i == 0 {
				return a, true // the whole chain is trusted proxies — we take the first one
			}
			continue
		}
		return a, true
	}
	return peer, false
}

// hasXFFEntries tells whether X-Forwarded-For carries at least one non-empty entry.
func hasXFFEntries(r *http.Request) bool {
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(h, ",") {
			if strings.TrimSpace(e) != "" {
				return true
			}
		}
	}
	return false
}

// ipAddress returns the client address as text — the key for limits and logs.
// ! It used to take the FIRST X-Forwarded-For entry, i.e. the one the client
// writes itself — changing one header gave a "new address" in the limiter.
func ipAddress(r *http.Request) string {
	if a, ok := clientAddress(r); ok {
		return a.String()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// proxyHeaders — traces that the request passed through an intermediary.
var proxyHeaders = []string{
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip",
	"Forwarded", "Cf-Connecting-Ip", "True-Client-Ip", "X-Client-Ip",
}

// fromUntrustedProxy says the request bears traces of a proxy that was not put
// on the trusted list. The connection address is then the intermediary's, not the
// client's, so nothing may be unlocked on its basis.
func fromUntrustedProxy(r *http.Request) bool {
	if fromTrustedProxy(r) {
		return false
	}
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// overHTTPS tells whether the request came over an encrypted channel — the Secure
// flag on the session cookie depends on it.
func overHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return fromTrustedProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
