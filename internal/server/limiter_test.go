package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// clock for limiter tests — advanced by hand.
type clock struct{ t time.Time }

func (z *clock) now() time.Time { return z.t }

func limiterWithClock(threshold, global int) (*limiter, *clock) {
	z := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	l := newLimiterWithThreshold(threshold)
	l.globalMax = global
	l.now = z.now
	return l, z
}

func TestLimiterIPv6CountedBy64(t *testing.T) {
	l, _ := limiterWithClock(5, 0)
	// five failures from DIFFERENT addresses of the same /64 = one key
	for i := 0; i < 5; i++ {
		l.recordFailure(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if l.allow("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Error("the next address from the same /64 should be blocked")
	}
	if !l.allow("2001:db8:1:3::1") {
		t.Error("a different /64 must not be blocked")
	}
}

func TestLimiterIPv4ByFullAddress(t *testing.T) {
	l, _ := limiterWithClock(5, 0)
	for i := 0; i < 5; i++ {
		l.recordFailure("192.0.2.1")
	}
	if l.allow("192.0.2.1") {
		t.Error("the same IPv4 address should be blocked")
	}
	if !l.allow("192.0.2.2") {
		t.Error("a neighbouring IPv4 address must not be blocked")
	}
	// IPv4 in the ::ffff: form is the same address
	if l.allow("::ffff:192.0.2.1") {
		t.Error("::ffff:192.0.2.1 is the same key as 192.0.2.1")
	}
}

func TestLimiterGlobalFailureLimitPerMinute(t *testing.T) {
	l, z := limiterWithClock(5, 30)
	for i := 0; i < 30; i++ {
		l.recordFailure(fmt.Sprintf("198.51.100.%d", i+1))
	}
	if l.allow("203.0.113.77") {
		t.Fatal("after 30 failures in a minute a new address must wait too")
	}
	z.t = z.t.Add(61 * time.Second)
	if !l.allow("203.0.113.77") {
		t.Fatal("the global block should expire after a minute, not last forever")
	}
}

func TestLimiterMemoryBoundedWithoutResettingAll(t *testing.T) {
	l, z := limiterWithClock(5, 0)
	l.maxKeys = 3
	l.recordFailure("192.0.2.1")
	z.t = z.t.Add(time.Second)
	l.recordFailure("192.0.2.2")
	z.t = z.t.Add(time.Second)
	for i := 0; i < 5; i++ {
		l.recordFailure("192.0.2.3") // blocked
	}
	z.t = z.t.Add(time.Second)
	l.recordFailure("192.0.2.4") // overflow: the oldest is evicted, not all of them
	if len(l.attempts) != 3 {
		t.Fatalf("keys = %d, want 3 (evict one, not a reset)", len(l.attempts))
	}
	if l.allow("192.0.2.3") {
		t.Error("a blocked address regained freedom after the map overflowed")
	}
	if _, is := l.attempts["192.0.2.1"]; is {
		t.Error("the oldest entry should be evicted")
	}
}

func TestLimiterOverflowClearsExpiredFirst(t *testing.T) {
	l, z := limiterWithClock(5, 0)
	l.maxKeys = 2
	l.recordFailure("192.0.2.1")
	l.recordFailure("192.0.2.2")
	z.t = z.t.Add(6 * time.Minute) // both expired
	l.recordFailure("192.0.2.3")
	if len(l.attempts) != 1 {
		t.Errorf("keys = %d, want 1 (expired ones cleaned up)", len(l.attempts))
	}
}

// At the HTTP level: 30 failed logins from different addresses stop the next
// address with a 429 message, and IPv6 from one /64 counts together.
func TestLoginGlobalLimitAndIPv6(t *testing.T) {
	s := testServer(t)
	_ = loggedInCookie(t, s)
	h := s.Handler()
	attempt := func(address string) int {
		data := url.Values{"login": {"admin"}, "password": {"zle-haslo"}}
		r, _ := http.NewRequest("POST", "/login", strings.NewReader(data.Encode()))
		r.RemoteAddr = address
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := &testResponse{headers: http.Header{}}
		h.ServeHTTP(w, r)
		return w.code
	}
	// one /64: the sixth attempt from another address of that /64 → 429
	for i := 0; i < 5; i++ {
		if k := attempt(fmt.Sprintf("[2001:db8:9:9::%x]:1000", i+1)); k != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, k)
		}
	}
	if k := attempt("[2001:db8:9:9:aaaa::1]:1000"); k != http.StatusTooManyRequests {
		t.Errorf("same /64: code %d, want 429", k)
	}
	// globally: further failures from other addresses (30 in total) cut off a new address
	for i := 0; i < 25; i++ {
		attempt(fmt.Sprintf("198.51.100.%d:1000", i+1))
	}
	if k := attempt("203.0.113.200:1000"); k != http.StatusTooManyRequests {
		t.Errorf("after 30 failures globally: code %d, want 429", k)
	}
}

type testResponse struct {
	headers http.Header
	code    int
}

func (o *testResponse) Header() http.Header         { return o.headers }
func (o *testResponse) Write(b []byte) (int, error) { return len(b), nil }
func (o *testResponse) WriteHeader(k int)           { o.code = k }
