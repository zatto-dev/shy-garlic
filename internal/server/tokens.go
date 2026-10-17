package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// Read-only API tokens (SHYG-49).
//
// The rule is one and narrow: the `Authorization: Bearer shyg_…` header is
// accepted ONLY for GET on the routes from `tokenRoutes`. On every other
// route (and especially on every one that changes something) it is ignored as if it
// were not there — a session is required. A token leaked from Prometheus can therefore
// neither change the config, nor create an account, nor create the next token.
var tokenRoutes = map[string]bool{
	"/api/status":  true,
	"/api/history": true,
	"/metrics":     true,
}

func tokenRoute(r *http.Request) bool {
	return r.Method == http.MethodGet && tokenRoutes[r.URL.Path]
}

// bearerFromHeader extracts the token from `Authorization: Bearer <token>`. The scheme is
// case-insensitive (RFC 7235); anything other than exactly
// one token after the scheme is "missing". A token in the query string does not exist —
// URLs land in proxy logs and in the browser history.
func bearerFromHeader(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const schema = "bearer "
	if len(h) <= len(schema) || !strings.EqualFold(h[:len(schema)], schema) {
		return "", false
	}
	t := h[len(schema):]
	if t == "" || strings.ContainsAny(t, " \t") {
		return "", false
	}
	return t, true
}

// denyWithoutSession — 401 in the form a machine client expects.
func denyWithoutSession(w http.ResponseWriter, r *http.Request) {
	if tokenRoute(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Shy Garlic"`)
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// ── limits and logs of failed attempts ───────────────────────────────────────

// badTokenThreshold — how many failed attempts from one address in 5 minutes before it
// gets 429. Higher than for login (5): a Prometheus with a typo in the token
// polls every dozen or so seconds and, once it is fixed, should regain access without a
// long wait, and 2^256 possible tokens cannot be guessed anyway.
const badTokenThreshold = 60

// logGate lets something be logged at most once per `period` per key — a scraper with
// a wrong token must not flood the logs with a line every 15 s.
type logGate struct {
	mu       sync.Mutex
	period   time.Duration
	lastSeen map[string]time.Time
}

func (t *logGate) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastSeen == nil || len(t.lastSeen) >= limiterMaxAddresses {
		t.lastSeen = map[string]time.Time{}
	}
	if o, ok := t.lastSeen[key]; ok && now.Sub(o) < t.period {
		return false
	}
	t.lastSeen[key] = now
	return true
}

// authenticateToken checks the token from the header. Returns true when the request
// may be handled; otherwise it responds itself (401 or 429).
func (s *Server) authenticateToken(w http.ResponseWriter, r *http.Request) bool {
	ip := ipAddress(r)
	if !s.tokenLimiter.allow(ip) {
		w.Header().Set("Retry-After", "300")
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return false
	}
	token, _ := bearerFromHeader(r)
	t, err := s.st.VerifyAPIToken(r.Context(), token)
	switch {
	case err == nil:
		s.markTokenUsed(r, t.ID)
		return true
	case errors.Is(err, store.ErrInvalidToken):
		s.tokenLimiter.recordFailure(ip)
		if s.tokenLogGate.allow(ip, time.Now()) {
			slog.Warn("API token authentication failed", "ip", ip, "path", r.URL.Path)
		}
	default:
		slog.Error("API token check", "err", err)
	}
	denyWithoutSession(w, r)
	return false
}

// markTokenUsed — last_used_at at most once a minute. The cache
// saves even the DB read on frequent scraping.
func (s *Server) markTokenUsed(r *http.Request, id int64) {
	now := time.Now()
	s.usageMu.Lock()
	lastSeen, ok := s.tokenUsage[id]
	if ok && now.Sub(lastSeen) < time.Minute {
		s.usageMu.Unlock()
		return
	}
	s.tokenUsage[id] = now
	s.usageMu.Unlock()
	if err := s.st.MarkTokenUsed(r.Context(), id, now); err != nil {
		slog.Debug("token last_used_at", "err", err)
	}
}

// ── unauthenticated /metrics for the given networks ──────────────────────────

// setMetricsAllow accepts CIDRs (and bare addresses) from which /metrics
// responds without a token. An empty list (default) = a token is always required.
func (s *Server) setMetricsAllow(list []string) error {
	p, err := parsePrefixes("metrics-allow", list)
	if err != nil {
		return err
	}
	s.metricsCIDR = p
	return nil
}

// metricsFromCIDR says whether the client is from a network let in without a token.
//
// Two precautions, because this is the only place where an IP address replaces a credential:
//   - the client address goes through clientAddress (the rightmost untrusted entry of
//     X-Forwarded-For, headers only from trusted proxies);
//   - a request with signs of a proxy that was not put on the trusted list is
//     rejected: we then see the intermediary's address, not the client's, and the intermediary stands
//     usually in the same LAN, so without this rule --metrics-allow
//     192.168.0.0/24 would open the metrics to the whole internet;
//   - a trusted proxy WITHOUT X-Forwarded-For (nginx with only X-Real-IP, an L4 forwarder)
//     is also rejected: the client address would then be the proxy address, i.e. every
//     request from the internet would look like LAN. A token still works.
func (s *Server) metricsFromCIDR(r *http.Request) bool {
	if len(s.metricsCIDR) == 0 || fromUntrustedProxy(r) {
		return false
	}
	if fromTrustedProxy(r) && !hasXFFEntries(r) {
		return false
	}
	a, ok := clientAddress(r)
	if !ok {
		return false
	}
	return inPrefixes(a, s.metricsCIDR)
}

// ── token management API (session only) ──────────────────────────────────────

type tokenAPI struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Hint     string `json:"hint"`
	Author   string `json:"author"`
	Created  string `json:"created"`
	LastUsed string `json:"lastUsed"` // empty = never
}

func toAPIToken(t store.APIToken) tokenAPI {
	o := tokenAPI{ID: t.ID, Name: t.Name, Hint: t.Hint, Author: t.Author,
		Created: t.CreatedAt.UTC().Format(time.RFC3339)}
	if !t.LastUsedAt.IsZero() {
		o.LastUsed = t.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return o
}

func (s *Server) handleTokensList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.APITokens(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]tokenAPI, 0, len(list))
	for _, t := range list {
		out = append(out, toAPIToken(t))
	}
	jsonReply(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad JSON")
		return
	}
	author := ""
	if u := UserFromRequest(r); u != nil {
		author = u.Login
	}
	token, entry, err := s.st.NewAPIToken(r.Context(), req.Name, author)
	switch {
	case errors.Is(err, store.ErrTokenName):
		jsonError(w, http.StatusBadRequest, i18n.T(lang, "error_token_name"))
		return
	case errors.Is(err, store.ErrTooManyTokens):
		jsonError(w, http.StatusConflict, i18n.T(lang, "error_token_limit"))
		return
	case err != nil:
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("API token created", "name", entry.Name, "author", author)
	// ! this is the only response with the whole token; nothing may remember it
	w.Header().Set("Cache-Control", "no-store")
	o := toAPIToken(entry)
	jsonReply(w, http.StatusCreated, map[string]any{
		"token": token, "id": o.ID, "name": o.Name, "hint": o.Hint, "created": o.Created,
	})
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.st.RemoveAPIToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrTokenNotFound) {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.usageMu.Lock()
	delete(s.tokenUsage, id)
	s.usageMu.Unlock()
	slog.Info("API token revoked", "id", id)
	jsonReply(w, http.StatusOK, map[string]any{"ok": true})
}
