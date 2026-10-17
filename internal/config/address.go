package config

import (
	"net/url"
	"regexp"
	"strings"
)

// A service address ends up in two places of different weight: in the health
// check (internal/health knows http, https and tcp) and in the tile's href
// attribute. The latter is an XSS vector — `javascript:` in `url:` would run on
// click — so "can be checked" and "may be clicked" are two separate questions.

// ServiceSchemes — the schemes the health check actually supports.
var ServiceSchemes = []string{"http", "https", "tcp"}

func parseURL(address string) (*url.URL, bool) {
	if address == "" || address != strings.TrimSpace(address) {
		return nil, false
	}
	u, err := url.Parse(address) // rejects control characters
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// ValidServiceAddress says whether the address may stand in a service's `url:`.
// Empty is valid (a service without a check, just a tile).
func ValidServiceAddress(address string) bool {
	if address == "" {
		return true
	}
	u, ok := parseURL(address)
	return ok && Allowed(strings.ToLower(u.Scheme), ServiceSchemes)
}

// ClickableAddress — whether it may be put into href. Only http(s); tcp:// is a
// valid check target, but the browser has nothing to do with it.
func ClickableAddress(address string) bool {
	u, ok := parseURL(address)
	if !ok {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return s == "http" || s == "https"
}

var userinfoPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://)[^/?#]*@`)

// HasCredentials says whether the address carries user[:password]@.
func HasCredentials(address string) bool {
	if u, err := url.Parse(address); err == nil {
		return u.User != nil
	}
	return userinfoPattern.MatchString(address)
}

// RedactURL drops the userinfo (user:password@) from an address. Use it for
// everything that is SHOWN or SENT — notifications, HTML, JSON, logs. The health
// check keeps using the full address, because that is what authenticates it.
func RedactURL(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return userinfoPattern.ReplaceAllString(address, "$1")
	}
	if u.User == nil {
		return address
	}
	u.User = nil
	return u.String()
}
