package server

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Ochrona przed żądaniami z obcej strony i przed ślepą wiarą w nagłówki proxy.
//
// ! multipart/form-data jest jednym z trzech typów „prostego żądania", więc
// endpoint wgrywania tapety NIE dostaje przypadkowej ochrony preflightem, którą
// mają dzisiejsze żądania JSON. Jedna warstwa domyka to razem z dziurami, które
// panel ma dziś: POST /setup bez ciasteczka na świeżej instancji (auto-submit z
// obcej strony = założenie konta administratora), CSRF na /login i /logout oraz
// POST /api/check i /api/notify/test, które nie mają ciała do sprawdzenia.

// wymagajSameOrigin przepuszcza odczyty, a mutacje tylko z naszego originu.
func wymagajSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !zTegoSamegoOrigin(r) {
			http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// zTegoSamegoOrigin czyta trzy źródła, od najpewniejszego. Sec-Fetch-Site
// wysyła każda żywa przeglądarka i nie da się go podrobić z JS-a; Origin i
// Referer są zapasem dla klientów bez niego (curl, stare silniki, skrypty).
func zTegoSamegoOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "same-site", "cross-site":
		// ! sąsiad na tej samej domenie rejestrowalnej to typowa topologia
		// homelabu i dokładnie ten przypadek, którego SameSite=Lax nie łapie
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		return tenSamHost(o, r)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return tenSamHost(ref, r)
	}
	return false
}

func tenSamHost(surowy string, r *http.Request) bool {
	u, err := url.Parse(surowy)
	if err != nil || u.Host == "" {
		return false
	}
	if !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && !poHTTPS(r)
}

// ── zaufane proxy ────────────────────────────────────────────────────────────

// zaufaneProxy — sieci, którym wolno mówić X-Forwarded-*. Domyślnie PUSTA.
//
// ! Bez tej listy limiter prób logowania omijało się jednym nagłówkiem na
// żądanie (każde z innym X-Forwarded-For to inny „adres"), a X-Forwarded-Proto
// pozwalał wymusić albo zdjąć flagę Secure z ciasteczka sesji.
var zaufaneProxy []netip.Prefix

// UstawZaufaneProxy przyjmuje listę CIDR-ów (dopuszcza też gołe adresy).
func UstawZaufaneProxy(lista []string) error {
	var out []netip.Prefix
	for _, s := range lista {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, err2 := netip.ParseAddr(s)
			if err2 != nil {
				return fmt.Errorf("zaufane proxy %q: %w", s, err)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p)
	}
	zaufaneProxy = out
	return nil
}

func odZaufanegoProxy(r *http.Request) bool {
	if len(zaufaneProxy) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range zaufaneProxy {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// adresIP zwraca adres klienta. X-Forwarded-For honorujemy wyłącznie od proxy
// z listy zaufanych.
func adresIP(r *http.Request) string {
	if odZaufanegoProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// poHTTPS mówi, czy żądanie przyszło szyfrowanym kanałem — od tego zależy flaga
// Secure na ciasteczku sesji.
func poHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return odZaufanegoProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
