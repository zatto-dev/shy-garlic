package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

const ciasteczkoSesji = "shyg_session"

type kluczKontekstu string

const kluczUżytkownika kluczKontekstu = "user"

// UżytkownikZŻądania zwraca zalogowanego użytkownika, jeśli jest.
func UżytkownikZŻądania(r *http.Request) *store.Użytkownik {
	u, _ := r.Context().Value(kluczUżytkownika).(*store.Użytkownik)
	return u
}

// ── ograniczanie prób logowania ──────────────────────────────────────────────

type limiter struct {
	mu    sync.Mutex
	próby map[string][]time.Time
}

func nowyLimiter() *limiter { return &limiter{próby: map[string][]time.Time{}} }

// wolno zwraca false, gdy z tego adresu było już 5 nieudanych prób w 5 minut.
func (l *limiter) wolno(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	próg := time.Now().Add(-5 * time.Minute)
	świeże := l.próby[ip][:0]
	for _, t := range l.próby[ip] {
		if t.After(próg) {
			świeże = append(świeże, t)
		}
	}
	l.próby[ip] = świeże
	return len(świeże) < 5
}

func (l *limiter) zapiszPorażkę(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.próby[ip] = append(l.próby[ip], time.Now())
}

// adresIP i poHTTPS mieszkają w csrf.go — obie zależą od listy zaufanych proxy.

// ── middleware ───────────────────────────────────────────────────────────────

// wymagajLogowania przepuszcza tylko z ważną sesją. Gdy w bazie nie ma jeszcze
// żadnego konta, przekierowuje na /setup — panel nie może stać otworem.
func (s *Server) wymagajLogowania(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n, err := s.st.LiczbaUżytkowników(r.Context()); err == nil && n == 0 {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}

		c, err := r.Cookie(ciasteczkoSesji)
		if err == nil {
			if u, err := s.st.Sesja(r.Context(), c.Value); err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), kluczUżytkownika, u)))
				return
			}
		}

		if strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("Accept") == "text/event-stream" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cel := "/login"
		if r.URL.Path != "/" {
			cel += "?next=" + r.URL.Path
		}
		http.Redirect(w, r, cel, http.StatusSeeOther)
	})
}

func (s *Server) ustawSesję(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, wygasa, err := s.st.NowaSesja(r.Context(), userID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     ciasteczkoSesji,
		Value:    token,
		Path:     "/",
		Expires:  wygasa,
		HttpOnly: true,
		Secure:   poHTTPS(r),
		// ! Strict, nie Lax: panel nie ma ani jednego przepływu wejściowego z
		// zewnątrz, więc koszt jest zerowy, a Lax nie broni przed sąsiadem na
		// tej samej domenie rejestrowalnej — czyli przed typowym homelabem,
		// gdzie obok panelu stoi kilkanaście self-hostowanych apek.
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// ── strony ───────────────────────────────────────────────────────────────────

type widokLogowania struct {
	Błąd   string
	Login  string
	Next   string
	Wersja string
	Setup  bool
	Tytuł  string

	Lang             string
	T                map[string]string
	DrugiJezyk       string
	DrugiJezykNazwa  string
	DrugiJezykKrotko string

	// Wyglad — tło bramy. ! Nigdy „photo" i nigdy z nazwą pliku: /bg/ stoi za
	// logowaniem, a zdjęcie użytkownika (razem z EXIF-em) nie ma prawa wyciec
	// do kogoś, kto się jeszcze nie przedstawił.
	Wyglad wyglądWidok
}

// handleLang przełącza język i wraca tam, skąd użytkownik przyszedł.
//
// Obsługuje POST z formularza i GET z parametrem — bo proxy przed panelem
// (bramki SSO, WAF-y) potrafią zjeść POST-a, a przełącznik języka ma działać
// wszędzie, także w przeglądarce bez JavaScriptu.
func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	l := i18n.Rozpoznaj(r.FormValue("lang"))
	if l == "" {
		l = i18n.Rozpoznaj(r.URL.Query().Get("set"))
	}
	if l == "" {
		l = i18n.EN
	}
	http.SetCookie(w, &http.Cookie{
		Name: i18n.Ciasteczko, Value: string(l), Path: "/",
		MaxAge: 365 * 24 * 3600, HttpOnly: false,
		Secure: poHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	cel := r.FormValue("next")
	if cel == "" || !strings.HasPrefix(cel, "/") || strings.HasPrefix(cel, "//") {
		cel = r.Header.Get("Referer")
	}
	if cel == "" {
		cel = "/"
	}
	http.Redirect(w, r, cel, http.StatusSeeOther)
}

func (s *Server) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	if n, err := s.st.LiczbaUżytkowników(r.Context()); err == nil && n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie(ciasteczkoSesji); err == nil {
		if _, err := s.st.Sesja(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	lang := i18n.Wybierz(r)
	s.renderLogin(w, s.zJęzykiem(lang, widokLogowania{
		Next: r.URL.Query().Get("next"), Wersja: Version, Tytuł: i18n.T(lang, "logowanie")}))
}

func (s *Server) handleLoginPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	login := strings.TrimSpace(r.FormValue("login"))
	hasło := r.FormValue("password")
	next := r.FormValue("next")
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}

	ip := adresIP(r)
	if !s.lim.wolno(ip) {
		slog.Warn("za dużo prób logowania", "ip", ip)
		w.WriteHeader(http.StatusTooManyRequests)
		lang := i18n.Wybierz(r)
		s.renderLogin(w, s.zJęzykiem(lang, widokLogowania{
			Błąd: i18n.T(lang, "za_duzo_prob"), Login: login, Next: next,
			Wersja: Version, Tytuł: i18n.T(lang, "logowanie")}))
		return
	}

	u, err := s.st.Sprawdź(r.Context(), login, hasło)
	if err != nil {
		s.lim.zapiszPorażkę(ip)
		slog.Warn("nieudane logowanie", "login", login, "ip", ip)
		w.WriteHeader(http.StatusUnauthorized)
		lang := i18n.Wybierz(r)
		s.renderLogin(w, s.zJęzykiem(lang, widokLogowania{
			Błąd: i18n.T(lang, "zle_dane"), Login: login, Next: next,
			Wersja: Version, Tytuł: i18n.T(lang, "logowanie")}))
		return
	}
	if err := s.ustawSesję(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	slog.Info("zalogowany", "login", u.Login, "ip", ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(ciasteczkoSesji); err == nil {
		_ = s.st.UsuńSesję(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: ciasteczkoSesji, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: poHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleSetup działa wyłącznie dopóki nie ma ani jednego konta.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.LiczbaUżytkowników(r.Context())
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		lang := i18n.Wybierz(r)
		s.renderLogin(w, s.zJęzykiem(lang, widokLogowania{
			Setup: true, Wersja: Version, Tytuł: i18n.T(lang, "pierwsze_urucho")}))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	login := strings.TrimSpace(r.FormValue("login"))
	hasło := r.FormValue("password")
	u, err := s.st.DodajUżytkownika(r.Context(), login, hasło)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		lang := i18n.Wybierz(r)
		komunikat := i18n.T(lang, "blad_konta")
		if strings.Contains(err.Error(), "8 characters") {
			komunikat = i18n.T(lang, "haslo_za_krotkie")
		} else if strings.Contains(err.Error(), "too short") {
			komunikat = i18n.T(lang, "login_za_krotki")
		}
		s.renderLogin(w, s.zJęzykiem(lang, widokLogowania{Setup: true, Błąd: komunikat, Login: login,
			Wersja: Version, Tytuł: i18n.T(lang, "pierwsze_urucho")}))
		return
	}
	if err := s.ustawSesję(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	slog.Info("utworzono konto administratora", "login", u.Login)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, dane widokLogowania) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "login.html", dane); err != nil {
		slog.Error("render login", "err", err)
	}
}

// BootstrapUżytkownika tworzy konto z ENV przy starcie (SHYG_ADMIN_USER +
// SHYG_ADMIN_PASSWORD) — wygodne przy pierwszym deployu bez wchodzenia w UI.
func BootstrapUżytkownika(ctx context.Context, st *store.Store, login, hasło string) error {
	if login == "" || hasło == "" {
		return nil
	}
	n, err := st.LiczbaUżytkowników(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := st.DodajUżytkownika(ctx, login, hasło); err != nil && !errors.Is(err, store.ErrJużIstnieje) {
		return err
	}
	slog.Info("konto administratora utworzone z ENV", "login", login)
	return nil
}


// zJęzykiem uzupełnia widok logowania o teksty, przełącznik języka i tło.
func (s *Server) zJęzykiem(l i18n.Lang, w widokLogowania) widokLogowania {
	w.Lang = string(l)
	w.T = i18n.Mapa(l)
	d := drugi(l)
	w.DrugiJezyk = string(d)
	w.DrugiJezykNazwa = d.Nazwa()
	w.DrugiJezykKrotko = d.Krótko()
	w.Wyglad = s.wyglądBramy()
	return w
}
