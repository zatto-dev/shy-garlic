package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireSameOriginBlocksForeignOrigin(t *testing.T) {
	handle := requireSameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 418 = "passed"
	}))

	cases := []struct {
		name    string
		method  string
		headers map[string]string
		code    int
	}{
		{"same-origin", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusTeapot},
		{"none (pasek adresu)", http.MethodPost, map[string]string{"Sec-Fetch-Site": "none"}, http.StatusTeapot},
		{"cross-site", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site (neighbour on the domain)", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"Origin zgodny", http.MethodPost, map[string]string{"Origin": "http://panel.dom"}, http.StatusTeapot},
		{"Origin obcy", http.MethodPost, map[string]string{"Origin": "http://zly.dom"}, http.StatusForbidden},
		{"Referer zgodny", http.MethodPost, map[string]string{"Referer": "http://panel.dom/ustawienia"}, http.StatusTeapot},
		{"Referer obcy", http.MethodPost, map[string]string{"Referer": "http://zly.dom/pulapka"}, http.StatusForbidden},
		{"nic", http.MethodPost, nil, http.StatusForbidden},
		{"DELETE z obcym Origin", http.MethodDelete, map[string]string{"Origin": "http://zly.dom"}, http.StatusForbidden},
		{"PUT with nothing", http.MethodPut, nil, http.StatusForbidden},
		{"GET with nothing", http.MethodGet, nil, http.StatusTeapot},
		{"HEAD with nothing", http.MethodHead, nil, http.StatusTeapot},
	}

	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://panel.dom/api/services", strings.NewReader("{}"))
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handle.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("%s: code = %d, want %d", c.name, w.Code, c.code)
		}
	}
}

func TestTrustedProxyDefaultsToNoTrust(t *testing.T) {
	if err := SetTrustedProxy(nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://panel.dom/login", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Forwarded-Proto", "https")

	// without a trusted list proxy headers must not change anything — otherwise the
	// login attempt limiter is bypassed with a single header per request
	if got := ipAddress(r); got != "203.0.113.9" {
		t.Errorf("ipAddress = %q, want the connection address", got)
	}
	if overHTTPS(r) {
		t.Error("X-Forwarded-Proto from an untrusted peer must not turn on the Secure flag")
	}

	if err := SetTrustedProxy([]string{"203.0.113.0/24"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetTrustedProxy(nil) })
	if got := ipAddress(r); got != "1.2.3.4" {
		t.Errorf("ipAddress behind a trusted proxy = %q, want 1.2.3.4", got)
	}
	if !overHTTPS(r) {
		t.Error("a trusted proxy must be able to say the traffic went over HTTPS")
	}
}

func TestSetTrustedProxyRejectsJunk(t *testing.T) {
	t.Cleanup(func() { _ = SetTrustedProxy(nil) })
	if err := SetTrustedProxy([]string{"nie-cidr"}); err == nil {
		t.Fatal("want an error for a bad CIDR")
	}
	if err := SetTrustedProxy([]string{"10.0.0.0/8", " 192.168.0.7 ", ""}); err != nil {
		t.Fatalf("a bare address and spaces must pass: %v", err)
	}
}
