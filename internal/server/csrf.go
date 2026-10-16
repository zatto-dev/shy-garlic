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
	out, err := parsujPrefiksy("zaufane proxy", lista)
	if err != nil {
		return err
	}
	zaufaneProxy = out
	return nil
}

// parsujPrefiksy zamienia listę CIDR-ów i gołych adresów na prefiksy; puste
// wpisy i spacje są pomijane.
func parsujPrefiksy(co string, lista []string) ([]netip.Prefix, error) {
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
				return nil, fmt.Errorf("%s %q: %w", co, s, err)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p)
	}
	return out, nil
}

func wPrefiksach(a netip.Addr, lista []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range lista {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func adresPołączenia(r *http.Request) (netip.Addr, bool) {
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

func odZaufanegoProxy(r *http.Request) bool {
	if len(zaufaneProxy) == 0 {
		return false
	}
	a, ok := adresPołączenia(r)
	return ok && wPrefiksach(a, zaufaneProxy)
}

// adresKlienta ustala, kto naprawdę łączy się z panelem.
//
// Połączenie od niezaufanego peera: jego adres, nagłówki proxy ignorowane.
// Od zaufanego proxy: X-Forwarded-For czytany OD PRAWEJ — każde proxy dopisuje
// na koniec adres, od którego dostało żądanie, więc skrajnie prawy wpis, który
// nie jest zaufanym proxy, to klient widziany przez najbardziej zewnętrzne
// zaufane proxy. Wpisy z lewej klient potrafi wymyślić sam, więc nie mają głosu.
//
// ok == false: nagłówek jest nieczytelny (śmieć zamiast adresu). Wołający ma
// wtedy odmówić, a nie zgadywać; addr zawiera adres połączenia, do limitów.
func adresKlienta(r *http.Request) (addr netip.Addr, ok bool) {
	peer, _ := adresPołączenia(r)
	if !odZaufanegoProxy(r) {
		return peer, peer.IsValid()
	}
	var wpisy []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(h, ",") {
			if e = strings.TrimSpace(e); e != "" {
				wpisy = append(wpisy, e)
			}
		}
	}
	if len(wpisy) == 0 {
		return peer, peer.IsValid()
	}
	for i := len(wpisy) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(wpisy[i])
		if err != nil {
			return peer, false
		}
		a = a.Unmap()
		if wPrefiksach(a, zaufaneProxy) {
			if i == 0 {
				return a, true // cały łańcuch to zaufane proxy — bierzemy pierwsze
			}
			continue
		}
		return a, true
	}
	return peer, false
}

// maWpisyXFF mówi, czy X-Forwarded-For niesie choć jeden niepusty wpis.
func maWpisyXFF(r *http.Request) bool {
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(h, ",") {
			if strings.TrimSpace(e) != "" {
				return true
			}
		}
	}
	return false
}

// adresIP zwraca adres klienta jako tekst — klucz limitów i logów.
// ! Wcześniej brał PIERWSZY wpis X-Forwarded-For, czyli ten, który klient
// wpisuje sam — zmiana jednego nagłówka dawała „nowy adres" w limiterze.
func adresIP(r *http.Request) string {
	if a, ok := adresKlienta(r); ok {
		return a.String()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// nagłówkiProxy — ślady tego, że żądanie przeszło przez pośrednika.
var nagłówkiProxy = []string{
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip",
	"Forwarded", "Cf-Connecting-Ip", "True-Client-Ip", "X-Client-Ip",
}

// zPośrednikaBezZaufania mówi, że żądanie nosi ślady proxy, którego nie
// wpisano na listę zaufanych. Adres połączenia to wtedy adres pośrednika, nie
// klienta, więc nie wolno na jego podstawie niczego odblokować.
func zPośrednikaBezZaufania(r *http.Request) bool {
	if odZaufanegoProxy(r) {
		return false
	}
	for _, h := range nagłówkiProxy {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// poHTTPS mówi, czy żądanie przyszło szyfrowanym kanałem — od tego zależy flaga
// Secure na ciasteczku sesji.
func poHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return odZaufanegoProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
