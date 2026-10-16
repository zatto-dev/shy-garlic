package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// zegar do testów limitera — przesuwany ręcznie.
type zegar struct{ t time.Time }

func (z *zegar) teraz() time.Time { return z.t }

func limiterZZegarem(próg, globalnie int) (*limiter, *zegar) {
	z := &zegar{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	l := nowyLimiterZProgiem(próg)
	l.maksGlobalnie = globalnie
	l.teraz = z.teraz
	return l, z
}

func TestLimiterIPv6LiczonyPo64(t *testing.T) {
	l, _ := limiterZZegarem(5, 0)
	// pięć porażek z RÓŻNYCH adresów tej samej /64 = jeden klucz
	for i := 0; i < 5; i++ {
		l.zapiszPorażkę(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if l.wolno("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Error("kolejny adres z tej samej /64 powinien być zablokowany")
	}
	if !l.wolno("2001:db8:1:3::1") {
		t.Error("inna /64 nie ma prawa być zablokowana")
	}
}

func TestLimiterIPv4PoPełnymAdresie(t *testing.T) {
	l, _ := limiterZZegarem(5, 0)
	for i := 0; i < 5; i++ {
		l.zapiszPorażkę("192.0.2.1")
	}
	if l.wolno("192.0.2.1") {
		t.Error("ten sam adres IPv4 powinien być zablokowany")
	}
	if !l.wolno("192.0.2.2") {
		t.Error("sąsiedni adres IPv4 nie ma prawa być zablokowany")
	}
	// IPv4 w postaci ::ffff: to ten sam adres
	if l.wolno("::ffff:192.0.2.1") {
		t.Error("::ffff:192.0.2.1 to ten sam klucz co 192.0.2.1")
	}
}

func TestLimiterGlobalnyLimitPorażekNaMinutę(t *testing.T) {
	l, z := limiterZZegarem(5, 30)
	for i := 0; i < 30; i++ {
		l.zapiszPorażkę(fmt.Sprintf("198.51.100.%d", i+1))
	}
	if l.wolno("203.0.113.77") {
		t.Fatal("po 30 porażkach w minutę nowy adres też ma czekać")
	}
	z.t = z.t.Add(61 * time.Second)
	if !l.wolno("203.0.113.77") {
		t.Fatal("blokada globalna ma wygasnąć po minucie, nie trwać w nieskończoność")
	}
}

func TestLimiterPamięćOgraniczonaBezResetuWszystkich(t *testing.T) {
	l, z := limiterZZegarem(5, 0)
	l.maksKluczy = 3
	l.zapiszPorażkę("192.0.2.1")
	z.t = z.t.Add(time.Second)
	l.zapiszPorażkę("192.0.2.2")
	z.t = z.t.Add(time.Second)
	for i := 0; i < 5; i++ {
		l.zapiszPorażkę("192.0.2.3") // zablokowany
	}
	z.t = z.t.Add(time.Second)
	l.zapiszPorażkę("192.0.2.4") // przepełnienie: wylatuje najstarszy, nie wszyscy
	if len(l.próby) != 3 {
		t.Fatalf("kluczy = %d, chcę 3 (wyrzucenie jednego, nie reset)", len(l.próby))
	}
	if l.wolno("192.0.2.3") {
		t.Error("zablokowany adres odzyskał wolność po przepełnieniu mapy")
	}
	if _, jest := l.próby["192.0.2.1"]; jest {
		t.Error("najstarszy wpis powinien wylecieć")
	}
}

func TestLimiterPrzepełnienieNajpierwCzyściWygasłe(t *testing.T) {
	l, z := limiterZZegarem(5, 0)
	l.maksKluczy = 2
	l.zapiszPorażkę("192.0.2.1")
	l.zapiszPorażkę("192.0.2.2")
	z.t = z.t.Add(6 * time.Minute) // oba wygasły
	l.zapiszPorażkę("192.0.2.3")
	if len(l.próby) != 1 {
		t.Errorf("kluczy = %d, chcę 1 (wygasłe sprzątnięte)", len(l.próby))
	}
}

// Na poziomie HTTP: 30 nieudanych logowań z różnych adresów zatrzymuje kolejny
// adres komunikatem 429, a IPv6 z jednej /64 liczy się razem.
func TestLogowanieGlobalnyLimitIIPv6(t *testing.T) {
	s := serwerTestowy(t)
	_ = ciasteczkoZalogowanego(t, s)
	h := s.Handler()
	próba := func(adres string) int {
		dane := url.Values{"login": {"admin"}, "password": {"zle-haslo"}}
		r, _ := http.NewRequest("POST", "/login", strings.NewReader(dane.Encode()))
		r.RemoteAddr = adres
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := &odpowiedźTestowa{nagłówki: http.Header{}}
		h.ServeHTTP(w, r)
		return w.kod
	}
	// jedna /64: szósta próba z innego adresu tej /64 → 429
	for i := 0; i < 5; i++ {
		if k := próba(fmt.Sprintf("[2001:db8:9:9::%x]:1000", i+1)); k != http.StatusUnauthorized {
			t.Fatalf("próba %d: %d", i, k)
		}
	}
	if k := próba("[2001:db8:9:9:aaaa::1]:1000"); k != http.StatusTooManyRequests {
		t.Errorf("ta sama /64: kod %d, chcę 429", k)
	}
	// globalnie: dalsze porażki z innych adresów (łącznie 30) odcinają nowy adres
	for i := 0; i < 25; i++ {
		próba(fmt.Sprintf("198.51.100.%d:1000", i+1))
	}
	if k := próba("203.0.113.200:1000"); k != http.StatusTooManyRequests {
		t.Errorf("po 30 porażkach globalnie: kod %d, chcę 429", k)
	}
}

type odpowiedźTestowa struct {
	nagłówki http.Header
	kod      int
}

func (o *odpowiedźTestowa) Header() http.Header         { return o.nagłówki }
func (o *odpowiedźTestowa) Write(b []byte) (int, error) { return len(b), nil }
func (o *odpowiedźTestowa) WriteHeader(k int)           { o.kod = k }
