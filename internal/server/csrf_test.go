package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWymagajSameOriginBlokujeObcyOrigin(t *testing.T) {
	uchwyt := wymagajSameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 418 = „przeszło"
	}))

	cases := []struct {
		nazwa    string
		metoda   string
		nagłówki map[string]string
		kod      int
	}{
		{"same-origin", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusTeapot},
		{"none (pasek adresu)", http.MethodPost, map[string]string{"Sec-Fetch-Site": "none"}, http.StatusTeapot},
		{"cross-site", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site (sąsiad w domenie)", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"Origin zgodny", http.MethodPost, map[string]string{"Origin": "http://panel.dom"}, http.StatusTeapot},
		{"Origin obcy", http.MethodPost, map[string]string{"Origin": "http://zly.dom"}, http.StatusForbidden},
		{"Referer zgodny", http.MethodPost, map[string]string{"Referer": "http://panel.dom/ustawienia"}, http.StatusTeapot},
		{"Referer obcy", http.MethodPost, map[string]string{"Referer": "http://zly.dom/pulapka"}, http.StatusForbidden},
		{"nic", http.MethodPost, nil, http.StatusForbidden},
		{"DELETE z obcym Origin", http.MethodDelete, map[string]string{"Origin": "http://zly.dom"}, http.StatusForbidden},
		{"PUT bez niczego", http.MethodPut, nil, http.StatusForbidden},
		{"GET bez niczego", http.MethodGet, nil, http.StatusTeapot},
		{"HEAD bez niczego", http.MethodHead, nil, http.StatusTeapot},
	}

	for _, c := range cases {
		r := httptest.NewRequest(c.metoda, "http://panel.dom/api/services", strings.NewReader("{}"))
		for k, v := range c.nagłówki {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		uchwyt.ServeHTTP(w, r)
		if w.Code != c.kod {
			t.Errorf("%s: kod = %d, chcę %d", c.nazwa, w.Code, c.kod)
		}
	}
}

func TestZaufaneProxyDomyślnieNieUfa(t *testing.T) {
	if err := UstawZaufaneProxy(nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://panel.dom/login", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Forwarded-Proto", "https")

	// bez listy zaufanych nagłówki proxy nie mają prawa nic zmienić — inaczej
	// limiter prób logowania omija się jednym nagłówkiem na żądanie
	if got := adresIP(r); got != "203.0.113.9" {
		t.Errorf("adresIP = %q, chcę adresu połączenia", got)
	}
	if poHTTPS(r) {
		t.Error("X-Forwarded-Proto od niezaufanego nie ma prawa włączyć flagi Secure")
	}

	if err := UstawZaufaneProxy([]string{"203.0.113.0/24"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = UstawZaufaneProxy(nil) })
	if got := adresIP(r); got != "1.2.3.4" {
		t.Errorf("adresIP zza zaufanego proxy = %q, chcę 1.2.3.4", got)
	}
	if !poHTTPS(r) {
		t.Error("zaufane proxy ma móc powiedzieć, że ruch szedł po HTTPS")
	}
}

func TestUstawZaufaneProxyOdrzucaŚmieci(t *testing.T) {
	t.Cleanup(func() { _ = UstawZaufaneProxy(nil) })
	if err := UstawZaufaneProxy([]string{"nie-cidr"}); err == nil {
		t.Fatal("chcę błędu przy złym CIDR")
	}
	if err := UstawZaufaneProxy([]string{"10.0.0.0/8", " 192.168.0.7 ", ""}); err != nil {
		t.Fatalf("goły adres i spacje mają przechodzić: %v", err)
	}
}
