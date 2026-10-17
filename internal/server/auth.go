package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

const sessionCookie = "shyg_session"

// limitForm — the login/setup/language forms are a few fields; the rest is an
// attempt to clog the memory.
const formLimit = 64 << 10

type contextKey string

const userKey contextKey = "user"

// UserFromRequest returns the logged-in user, if there is one.
func UserFromRequest(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// ── login attempt limiting ───────────────────────────────────────────────────

type limiter struct {
	mu        sync.Mutex
	attempts  map[string][]time.Time
	threshold int

	// globalMax: how many failed attempts per minute we accept in total from ALL
	// addresses before holding off further ones (0 = no global limit). It is a fuse
	// against distributed guessing from many addresses (/64 rotation, botnet); the
	// block is temporary — it expires when the one-minute window moves on.
	globalMax int
	global    []time.Time

	maxKeys int              // hard memory bound
	now     func() time.Time // injected in tests
}

func newLimiter() *limiter {
	l := newLimiterWithThreshold(5)
	l.globalMax = maxLoginFailuresPerMinute
	return l
}

func newLimiterWithThreshold(threshold int) *limiter {
	return &limiter{attempts: map[string][]time.Time{}, threshold: threshold,
		maxKeys: limiterMaxAddresses, now: time.Now}
}

// limiterMaxAddresses — hard upper memory bound: the map grows by one address
// on every first failure, and /metrics may be exposed to the internet.
const limiterMaxAddresses = 10000

// maxLoginFailuresPerMinute — global login fuse (all addresses together).
const maxLoginFailuresPerMinute = 30

const (
	limiterWindow     = 5 * time.Minute
	globalLimitWindow = time.Minute
)

// limiterKey: IPv6 is counted by the /64 prefix (one home network gets
// 2^64 addresses, so a per-full-address limit did nothing), IPv4 by the full address.
func limiterKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

// allow returns false when this address already had `threshold` failed attempts
// in 5 minutes or when the global failure-per-minute limit is exhausted.
func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.globalMax > 0 && len(l.freshGlobal(now)) >= l.globalMax {
		return false
	}
	key := limiterKey(ip)
	threshold := now.Add(-limiterWindow)
	fresh := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if t.After(threshold) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) == 0 {
		delete(l.attempts, key)
	} else {
		l.attempts[key] = fresh
	}
	return len(fresh) < l.threshold
}

func (l *limiter) freshGlobal(now time.Time) []time.Time {
	from := now.Add(-globalLimitWindow)
	i := 0
	for i < len(l.global) && !l.global[i].After(from) {
		i++
	}
	l.global = l.global[i:]
	return l.global
}

func (l *limiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := limiterKey(ip)
	if _, known := l.attempts[key]; !known && len(l.attempts) >= l.maxKeys {
		l.freeSpace(now)
	}
	l.attempts[key] = append(l.attempts[key], now)
	if l.globalMax > 0 && len(l.freshGlobal(now)) < l.globalMax {
		l.global = append(l.global, now)
	}
}

// freeSpace makes room for a new key without forgetting everything at once:
// first the expired entries, and if that is not enough — the single least recently used one.
func (l *limiter) freeSpace(now time.Time) {
	threshold := now.Add(-limiterWindow)
	for k, ts := range l.attempts {
		if len(ts) == 0 || !ts[len(ts)-1].After(threshold) {
			delete(l.attempts, k)
		}
	}
	if len(l.attempts) < l.maxKeys {
		return
	}
	var oldest string
	var when time.Time
	for k, ts := range l.attempts {
		if o := ts[len(ts)-1]; oldest == "" || o.Before(when) {
			oldest, when = k, o
		}
	}
	delete(l.attempts, oldest)
}

// ipAddress and overHTTPS live in csrf.go — both depend on the trusted proxy list.

// safeTarget returns `next` only when it is a path inside the panel;
// in every other case "/". The single source of truth for redirects after
// login and after a language change.
//
// ! The test `HasPrefix("/") && !HasPrefix("//")` alone let `/\evil.com` through:
// browsers treat `\` like `/`, so Location: /\evil.com is an external address.
// We reject a backslash (also encoded), control characters and everything that
// url.Parse sees as a scheme or host.
func safeTarget(next string) string {
	const def = "/"
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") {
		return def
	}
	for _, r := range next {
		if r == '\\' || r < 0x20 || r == 0x7f {
			return def
		}
	}
	// encoded backslash or control character: we decode and check once more
	if decoded, err := url.PathUnescape(next); err != nil {
		return def
	} else {
		for _, r := range decoded {
			if r == '\\' || r < 0x20 || r == 0x7f {
				return def
			}
		}
		if strings.HasPrefix(decoded, "//") {
			return def
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return def
	}
	return next
}

// targetFromReferer — go back to the page the request came from, but only if the
// Referer points to the SAME host. GET /lang is not protected by
// requireSameOrigin, so a foreign Referer was an open redirect.
func targetFromReferer(r *http.Request) string {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return "/"
	}
	u, err := url.Parse(ref)
	if err != nil || !strings.EqualFold(u.Host, r.Host) {
		return "/"
	}
	return safeTarget(u.RequestURI())
}

// ── middleware ───────────────────────────────────────────────────────────────

// requireLogin lets through only a valid session. When there is no account in the
// DB yet, it redirects to /setup — the panel must not stand wide open.
//
// Outside a session there are two narrow exceptions, both only for GET (see tokens.go):
// an API token on three read-only routes and unauthenticated /metrics for
// networks from --metrics-allow. Token routes and /api/* never redirect to
// /login — a machine client gets 401.
func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/metrics" && s.metricsFromCIDR(r) {
			next.ServeHTTP(w, r)
			return
		}
		_, hasBearer := bearerFromHeader(r)
		machine := tokenRoute(r) && (hasBearer || r.URL.Path == "/metrics")

		if n, err := s.st.UserCount(r.Context()); err == nil && n == 0 {
			if machine {
				denyWithoutSession(w, r)
				return
			}
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}

		c, err := r.Cookie(sessionCookie)
		if err == nil {
			if u, err := s.st.Session(r.Context(), c.Value); err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
				return
			}
		}

		if tokenRoute(r) && hasBearer {
			if s.authenticateToken(w, r) {
				next.ServeHTTP(w, r)
			}
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" ||
			r.Header.Get("Accept") == "text/event-stream" {
			denyWithoutSession(w, r)
			return
		}
		target := "/login"
		if r.URL.Path != "/" {
			target += "?next=" + url.QueryEscape(r.URL.Path)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, expires, err := s.st.NewSession(r.Context(), userID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   overHTTPS(r),
		// ! Strict, not Lax: the panel has not a single inbound flow from outside, so the
		// cost is zero, and Lax does not protect against a neighbour on the same
		// registrable domain — that is, against a typical homelab where a dozen
		// self-hosted apps stand next to the panel.
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// ── pages ────────────────────────────────────────────────────────────────────

type loginView struct {
	Error   string
	Login   string
	Next    string
	Version string
	Setup   bool
	Title   string

	Lang            string
	T               map[string]string
	SecondLang      string
	SecondLangName  string
	SecondLangShort string

	// Appearance — the gate background. ! Never "photo" and never with a file name: /bg/ sits
	// behind the login, and the user's photo (together with its EXIF) must not leak
	// to someone who has not introduced themselves yet.
	Appearance appearanceView
}

// handleLang switches the language and goes back to where the user came from.
//
// Handles a POST from a form and a GET with a parameter — because proxies in front
// of the panel (SSO gates, WAFs) can swallow a POST, and the language switch
// must work everywhere, also in a browser without JavaScript.
func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	_ = r.ParseForm()
	l := i18n.ParseLang(r.FormValue("lang"))
	if l == "" {
		l = i18n.ParseLang(r.URL.Query().Get("set"))
	}
	if l == "" {
		l = i18n.EN
	}
	http.SetCookie(w, &http.Cookie{
		Name: i18n.CookieName, Value: string(l), Path: "/",
		MaxAge: 365 * 24 * 3600, HttpOnly: false,
		Secure: overHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	next := r.FormValue("next")
	target := safeTarget(next)
	if target == "/" && next != "/" { // missing or bad next → go back to where it came from
		target = targetFromReferer(r)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	if n, err := s.st.UserCount(r.Context()); err == nil && n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.st.Session(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	lang := i18n.FromRequest(r)
	s.renderLogin(w, s.withLang(lang, loginView{
		Next: r.URL.Query().Get("next"), Version: Version, Title: i18n.T(lang, "logowanie")}))
}

func (s *Server) handleLoginPOST(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	login := strings.TrimSpace(r.FormValue("login"))
	password := r.FormValue("password")
	next := safeTarget(r.FormValue("next"))

	ip := ipAddress(r)
	if !s.lim.allow(ip) {
		slog.Warn("too many login attempts", "ip", ip)
		w.WriteHeader(http.StatusTooManyRequests)
		lang := i18n.FromRequest(r)
		s.renderLogin(w, s.withLang(lang, loginView{
			Error: i18n.T(lang, "za_duzo_prob"), Login: login, Next: next,
			Version: Version, Title: i18n.T(lang, "logowanie")}))
		return
	}

	u, err := s.st.Authenticate(r.Context(), login, password)
	if err != nil {
		s.lim.recordFailure(ip)
		slog.Warn("login failed", "login", loginForLog(login), "ip", ip)
		w.WriteHeader(http.StatusUnauthorized)
		lang := i18n.FromRequest(r)
		s.renderLogin(w, s.withLang(lang, loginView{
			Error: i18n.T(lang, "zle_dane"), Login: login, Next: next,
			Version: Version, Title: i18n.T(lang, "logowanie")}))
		return
	}
	if err := s.setSession(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	slog.Info("logged in", "login", u.Login, "ip", ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.st.RemoveSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: overHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleSetup works only as long as there is not a single account, and only with
// the setup token printed in the log (see setup.go).
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.UserCount(r.Context())
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.SetupToken() // makes sure it exists and has been logged
	lang := i18n.FromRequest(r)
	view := func(message, login string) loginView {
		return s.withLang(lang, loginView{Setup: true, Error: message, Login: login,
			Version: Version, Title: i18n.T(lang, "pierwsze_urucho")})
	}
	if r.Method == http.MethodGet {
		s.renderLogin(w, view("", ""))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	login := strings.TrimSpace(r.FormValue("login"))
	password := r.FormValue("password")

	// a wrong token counts as a failed attempt in the sign-in limiter
	ip := ipAddress(r)
	if !s.lim.allow(ip) {
		slog.Warn("too many setup attempts", "ip", ip)
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderLogin(w, view(i18n.T(lang, "za_duzo_prob"), login))
		return
	}
	if !s.checkSetupToken(r.FormValue("setup_token")) {
		s.lim.recordFailure(ip)
		slog.Warn("setup refused: wrong setup token", "ip", ip)
		w.WriteHeader(http.StatusForbidden)
		s.renderLogin(w, view(i18n.T(lang, "setup_token_bad"), login))
		return
	}

	u, err := s.st.AddFirstUser(r.Context(), login, password)
	if errors.Is(err, store.ErrSetupDone) {
		http.Redirect(w, r, "/login", http.StatusSeeOther) // someone else finished first
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		message := i18n.T(lang, "blad_konta")
		if strings.Contains(err.Error(), "8 characters") {
			message = i18n.T(lang, "haslo_za_krotkie")
		} else if strings.Contains(err.Error(), "too short") {
			message = i18n.T(lang, "login_za_krotki")
		}
		s.renderLogin(w, view(message, login))
		return
	}
	s.clearSetupToken()
	if err := s.setSession(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	slog.Info("admin account created", "login", u.Login)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, data loginView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		slog.Error("render login", "err", err)
	}
}

// BootstrapUser creates an account from ENV at start-up (SHYG_ADMIN_USER +
// SHYG_ADMIN_PASSWORD) — handy on the first deploy without going into the UI.
func BootstrapUser(ctx context.Context, st *store.Store, login, password string) error {
	if login == "" || password == "" {
		return nil
	}
	n, err := st.UserCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := st.AddFirstUser(ctx, login, password); err != nil &&
		!errors.Is(err, store.ErrUserExists) && !errors.Is(err, store.ErrSetupDone) {
		return err
	}
	slog.Info("admin account created from ENV", "login", login)
	return nil
}

// withLang completes the login view with texts, the language switch and the background.
func (s *Server) withLang(l i18n.Lang, w loginView) loginView {
	w.Lang = string(l)
	w.T = i18n.Map(l)
	d := second(l)
	w.SecondLang = string(d)
	w.SecondLangName = d.Name()
	w.SecondLangShort = d.Short()
	w.Appearance = s.gateAppearance()
	return w
}
