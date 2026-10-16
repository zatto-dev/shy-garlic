package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// bodyServer replies with the given code and body and remembers the paths it
// was asked for — the test checks where the request really went.
func bodyServer(code int, body string) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func check(t *testing.T, target Target) Result {
	t.Helper()
	m := New()
	defer m.Close()
	if target.Timeout == 0 {
		target.Timeout = 2 * time.Second
	}
	return Check(context.Background(), m.client, target)
}

func TestCheckContainsFoundIsUp(t *testing.T) {
	s, _ := bodyServer(200, `<html><title>Jellyfin</title></html>`)
	defer s.Close()
	if r := check(t, Target{URL: s.URL, Contains: "Jellyfin"}); r.State != StateUp || r.Err != "" {
		t.Fatalf("%+v", r)
	}
}

// The server replies, just not with what it should — that is degraded, not down:
// same as a bad HTTP code.
func TestCheckContainsMissingIsDegradedWithReason(t *testing.T) {
	s, _ := bodyServer(200, `Welcome to nginx!`)
	defer s.Close()
	r := check(t, Target{URL: s.URL, Contains: "Jellyfin"})
	if r.State != StateDegraded {
		t.Fatalf("stan = %s, chcę degraded", r.State)
	}
	if r.Code != 200 {
		t.Errorf("kod = %d: serwer odpowiedział 200 i to ma być widać", r.Code)
	}
	if !strings.HasPrefix(r.Err, ContentPrefix) || !strings.Contains(r.Err, "Jellyfin") {
		t.Errorf("powód = %q", r.Err)
	}
}

func TestCheckNotContains(t *testing.T) {
	s, _ := bodyServer(200, `<h1>Maintenance mode</h1>`)
	defer s.Close()
	r := check(t, Target{URL: s.URL, NotContains: "Maintenance"})
	if r.State != StateDegraded || !strings.Contains(r.Err, "Maintenance") {
		t.Fatalf("%+v", r)
	}
	if r := check(t, Target{URL: s.URL, NotContains: "Error 500"}); r.State != StateUp {
		t.Fatalf("brak zakazanego tekstu to up: %+v", r)
	}
}

func TestCheckContainsAndNotContainsTogether(t *testing.T) {
	s, _ := bodyServer(200, `ok but with warning`)
	defer s.Close()
	if r := check(t, Target{URL: s.URL, Contains: "ok", NotContains: "warning"}); r.State != StateDegraded {
		t.Fatalf("%+v", r)
	}
	if r := check(t, Target{URL: s.URL, Contains: "ok", NotContains: "panic"}); r.State != StateUp {
		t.Fatalf("%+v", r)
	}
}

// A bad code takes precedence: there is no point reading the body of a response
// that is already an error, and the reason "HTTP 500" says more than "text missing".
func TestCheckBadCodeBeforeContent(t *testing.T) {
	s, _ := bodyServer(500, `Jellyfin`)
	defer s.Close()
	r := check(t, Target{URL: s.URL, Contains: "Jellyfin"})
	if r.State != StateDegraded || r.Err != "HTTP 500" {
		t.Fatalf("%+v", r)
	}
}

func TestCheckPathReplacesAddressPath(t *testing.T) {
	s, paths := bodyServer(200, `{"status":"ok"}`)
	defer s.Close()
	r := check(t, Target{URL: s.URL + "/ui/index.html?x=1", Path: "/api/health?deep=1", Contains: `"ok"`})
	if r.State != StateUp {
		t.Fatalf("%+v", r)
	}
	if got := paths(); len(got) != 1 || got[0] != "/api/health?deep=1" {
		t.Fatalf("żądanie poszło na %v; chcę /api/health?deep=1 (ścieżka i query z adresu zastąpione)", got)
	}
}

func TestCheckWithoutPathUsesAddressAsBefore(t *testing.T) {
	s, paths := bodyServer(200, ``)
	defer s.Close()
	check(t, Target{URL: s.URL + "/admin?a=b"})
	if got := paths(); len(got) != 1 || got[0] != "/admin?a=b" {
		t.Fatalf("%v", got)
	}
}

// path is a path ON THE SAME host: an address inside it cannot redirect the
// check elsewhere.
func TestCheckPathCannotChangeHost(t *testing.T) {
	s, paths := bodyServer(200, ``)
	defer s.Close()
	for _, bad := range []string{"//evil.example/x", "http://evil.example/x", "health"} {
		r := check(t, Target{URL: s.URL, Path: bad})
		if r.State != StateDown || !strings.Contains(r.Err, "check.path") {
			t.Errorf("path %q: %+v", bad, r)
		}
	}
	if got := paths(); len(got) != 0 {
		t.Errorf("serwer dostał żądania mimo złego path: %v", got)
	}
}

func TestCheckBodyReadOnlyUpToLimit(t *testing.T) {
	// the needle lies BEYOND the limit — it must not be found, and the response must explain why
	body := strings.Repeat("a", maxBody+100) + "IGŁA"
	s, _ := bodyServer(200, body)
	defer s.Close()
	r := check(t, Target{URL: s.URL, Contains: "IGŁA"})
	if r.State != StateDegraded || !strings.Contains(r.Err, "1 MiB") {
		t.Fatalf("%+v", r)
	}
	// the same content within the limit is found
	s2, _ := bodyServer(200, strings.Repeat("a", maxBody-10)+"IGŁA")
	defer s2.Close()
	if r := check(t, Target{URL: s2.URL, Contains: "IGŁA"}); r.State != StateUp {
		t.Fatalf("igła tuż przed limitem: %+v", r)
	}
}

// The server sends headers and goes silent: the timeout covers reading the body too.
func TestCheckTimeoutWhileReadingBody(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer s.Close()
	r := check(t, Target{URL: s.URL, Contains: "x", Timeout: 150 * time.Millisecond})
	if r.State != StateDown || r.Err != "timeout" {
		t.Fatalf("%+v", r)
	}
}

func TestCheckContainsDoesNotApplyToTCP(t *testing.T) {
	s, _ := bodyServer(200, ``)
	defer s.Close()
	address := "tcp://" + strings.TrimPrefix(s.URL, "http://")
	if r := check(t, Target{URL: address, Contains: "x", Path: "/y"}); r.State != StateUp {
		t.Fatalf("%+v", r)
	}
}

func TestCheckContentReasonIsShort(t *testing.T) {
	s, _ := bodyServer(200, `x`)
	defer s.Close()
	r := check(t, Target{URL: s.URL, Contains: strings.Repeat("ż", 500)})
	if n := len([]rune(r.Err)); n > 100 {
		t.Errorf("powód ma %d znaków — nie zmieści się w kafelku", n)
	}
}
