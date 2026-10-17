package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
)

// First-run setup.
//
// A fresh instance has no accounts, and /setup creates the first one without
// authentication. Anyone who reached the panel first became its administrator,
// and the administrator can edit the config file. Three layers close that:
//
//  1. a one-time setup token, printed in the panel's log at start and required
//     on the form — only someone who can read the log (the operator) can finish setup;
//  2. the first account is created in a single SQL statement (store.AddFirstUser),
//     so parallel requests cannot create several;
//  3. --allowed-host, which refuses requests for any other Host name (DNS rebinding).
//
// SHYG_ADMIN_USER / SHYG_ADMIN_PASSWORD still skip setup entirely.

// setupGate holds the token. It lives in memory only: a restart without accounts
// prints a new one.
type setupGate struct {
	mu    sync.Mutex
	token string
}

// normalizeSetupToken makes the comparison forgiving about case, spaces and dashes,
// because the token is copied by hand from a log.
func normalizeSetupToken(v string) string {
	v = strings.ToLower(v)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, v)
}

func newSetupToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:16] + "-" + h[16:24] + "-" + h[24:32]
}

// SetupToken returns the current setup token, creating (and logging) it on first use.
func (s *Server) SetupToken() string {
	s.setup.mu.Lock()
	defer s.setup.mu.Unlock()
	if s.setup.token == "" {
		s.setup.token = newSetupToken()
		slog.Warn("first run: no account exists yet. Open /setup and enter this setup token to create the administrator "+
			"(or set SHYG_ADMIN_USER and SHYG_ADMIN_PASSWORD to skip setup)",
			"setup_token", s.setup.token)
	}
	return s.setup.token
}

func (s *Server) checkSetupToken(given string) bool {
	want := normalizeSetupToken(s.SetupToken())
	got := normalizeSetupToken(given)
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func (s *Server) clearSetupToken() {
	s.setup.mu.Lock()
	s.setup.token = ""
	s.setup.mu.Unlock()
}

// ── allowed Host names ───────────────────────────────────────────────────────

// hostOnly lower-cases a Host header value and drops the port, the brackets of an
// IPv6 literal and a trailing dot.
func hostOnly(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(h, ".")
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// parseAllowedHosts normalizes the --allowed-host list; empty entries are skipped.
func parseAllowedHosts(list []string) map[string]bool {
	out := map[string]bool{}
	for _, h := range list {
		if h = hostOnly(h); h != "" {
			out[h] = true
		}
	}
	return out
}

// requireAllowedHost refuses requests whose Host header is not on the list. With an
// empty list it does nothing (the default, same as before). Loopback names always
// pass: a browser on the same machine, a container health check — and a rebinding
// page cannot make a victim's browser send "Host: localhost".
func requireAllowedHost(allowed map[string]bool, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := hostOnly(r.Host)
		if !allowed[h] && !loopbackHost(h) {
			http.Error(w, "misdirected request: this host name is not allowed (see --allowed-host)", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginForLog shortens a typed login for the log: enough to correlate attempts,
// not enough to read a password that was typed into the wrong field.
func loginForLog(login string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(login)))
	return "sha256:" + hex.EncodeToString(sum[:6])
}
