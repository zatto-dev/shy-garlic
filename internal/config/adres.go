package config

import (
	"net/url"
	"strings"
)

// Adres usługi ląduje w dwóch miejscach o różnej wadze: w sprawdzaniu zdrowia
// (internal/health zna http, https i tcp) oraz w atrybucie href kafla. To drugie
// to wektor XSS — `javascript:` w `url:` wykonałby się po kliknięciu — więc
// „da się sprawdzić" i „wolno kliknąć" to dwa osobne pytania.

// SchematyUsługi — schematy, które sprawdzanie zdrowia faktycznie obsługuje.
var SchematyUsługi = []string{"http", "https", "tcp"}

func rozbierz(adres string) (*url.URL, bool) {
	if adres == "" || adres != strings.TrimSpace(adres) {
		return nil, false
	}
	u, err := url.Parse(adres) // odrzuca znaki sterujące
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// PoprawnyAdresUsługi mówi, czy adres może stanąć w `url:` usługi. Pusty jest
// poprawny (usługa bez sprawdzania, sam kafelek).
func PoprawnyAdresUsługi(adres string) bool {
	if adres == "" {
		return true
	}
	u, ok := rozbierz(adres)
	return ok && Dozwolone(strings.ToLower(u.Scheme), SchematyUsługi)
}

// AdresKlikalny — czy wolno go wstawić do href. Tylko http(s); tcp:// jest
// poprawnym celem sprawdzania, ale przeglądarka nie ma co z nim zrobić.
func AdresKlikalny(adres string) bool {
	u, ok := rozbierz(adres)
	if !ok {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return s == "http" || s == "https"
}
