package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Sending must not block the caller: Observe is called from the loop that
// saves the check history to SQLite. Previously retries (up to ~2.5 s)
// stalled saving the results of all services.
func TestObserveDoesNotBlockOnSlowWebhook(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		<-release
		w.WriteHeader(204)
	}))
	defer srv.Close()
	p := New(srv.URL, 1)

	start := time.Now()
	p.Observe(context.Background(), makeEvent("down"))
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Observe waited %v for the webhook", d)
	}
	// sending really takes place in the background
	waitFor(t, func() bool { return started.Load() == 1 })
	close(release)
	p.Wait()
	if p.reportedState("Media/Jellyfin") != "down" {
		t.Error("po udanej wysyłce stan ma być zgłoszony")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(end) {
			t.Fatal("warunek nie spełnił się w 3 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *Notifier) reportedState(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.state[key]; s != nil {
		return s.reported
	}
	return ""
}

// Subsequent checks of the same outage, while the first send is still in progress, must
// not fire a second notification.
func TestDoesNotDuplicateAlertDuringSend(t *testing.T) {
	release := make(chan struct{})
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		<-release
	}))
	defer srv.Close()
	p := New(srv.URL, 1)

	for i := 0; i < 5; i++ {
		p.Observe(context.Background(), makeEvent("down"))
	}
	waitFor(t, func() bool { return requestCount.Load() >= 1 })
	for i := 0; i < 5; i++ {
		p.Observe(context.Background(), makeEvent("down"))
	}
	close(release)
	p.Wait()
	if n := requestCount.Load(); n != 1 {
		t.Fatalf("żądań = %d, chcę 1 mimo 10 sprawdzeń w trakcie wysyłki", n)
	}
}

// SHYG-42 semantics preserved: a failed send does not mark the state as
// reported, so the next check tries again — also when
// sending happens in the background.
func TestFailedBackgroundSendLeavesStateForRetry(t *testing.T) {
	shortDelays(t)
	srv, counter, mu := failingWebhook(1 + len(retryDelays))
	defer srv.Close()
	p := New(srv.URL, 1)

	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	if got := p.reportedState("Media/Jellyfin"); got != "up" {
		t.Fatalf("po nieudanej serii zgłoszony = %q, chcę up (do ponowienia)", got)
	}
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	if got := p.reportedState("Media/Jellyfin"); got != "down" {
		t.Fatalf("po udanym ponowieniu zgłoszony = %q, chcę down", got)
	}
	mu.Lock()
	n := *counter
	mu.Unlock()
	if n != 2+len(retryDelays) {
		t.Errorf("żądań = %d", n)
	}
}

// For one service, messages go out in event order, even when the first
// one is slow.
func TestMessageOrderOfOneService(t *testing.T) {
	var mu sync.Mutex
	var states []string
	var first atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct{ State string }
		_ = json.Unmarshal(b, &m)
		if !first.Swap(true) {
			time.Sleep(150 * time.Millisecond) // the first message is slow
		}
		mu.Lock()
		states = append(states, m.State)
		mu.Unlock()
	}))
	defer srv.Close()
	p := New(srv.URL, 1)
	p.SetFormat(FormatGeneric)

	p.Observe(context.Background(), makeEvent("degraded"))
	p.Observe(context.Background(), makeEvent("down")) // degraded→down: the second message
	p.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(states) != 2 || states[0] != "degraded" || states[1] != "down" {
		t.Fatalf("kolejność = %v, chcę [degraded down]", states)
	}
}

// A blocked webhook of one service does not hold up notifications of others.
func TestSlowServiceDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	delivered := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct{ Service string }
		_ = json.Unmarshal(b, &m)
		if m.Service == "Wolna" {
			<-release
		}
		mu.Lock()
		delivered[m.Service] = true
		mu.Unlock()
	}))
	defer srv.Close()
	p := New(srv.URL, 1)
	p.SetFormat(FormatGeneric)

	slow := makeEvent("down")
	slow.Key, slow.Name = "G/Wolna", "Wolna"
	fast := makeEvent("down")
	fast.Key, fast.Name = "G/Szybka", "Szybka"
	p.Observe(context.Background(), slow)
	p.Observe(context.Background(), fast)

	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return delivered["Szybka"] })
	mu.Lock()
	if delivered["Wolna"] {
		t.Error("wolna już dostarczona — test nic nie dowodzi")
	}
	mu.Unlock()
	close(release)
	p.Wait()
}

// Cancelling the context (panel shutdown) stops retries and does not report an
// undelivered alert as delivered.
func TestContextCancelStopsRetries(t *testing.T) {
	saved := retryDelays
	retryDelays = []time.Duration{time.Hour, time.Hour}
	defer func() { retryDelays = saved }()

	srv, counter, mu := failingWebhook(1000)
	defer srv.Close()
	p := New(srv.URL, 1)
	ctx, cancel := context.WithCancel(context.Background())

	p.Observe(ctx, makeEvent("down"))
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return *counter >= 1 })
	cancel()

	finished := make(chan struct{})
	go func() { p.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("po anulowaniu wysyłka dalej czeka na ponowienie")
	}
	if got := p.reportedState("Media/Jellyfin"); got != "up" {
		t.Errorf("zgłoszony = %q po anulowanym alercie, chcę up", got)
	}
}

// Moving a service during a send: the result lands in the state that
// survived the move, so after recovery there is no duplicate.
func TestMoveDuringSendDoesNotLoseState(t *testing.T) {
	release := make(chan struct{})
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		<-release
	}))
	defer srv.Close()
	p := New(srv.URL, 1)

	p.Observe(context.Background(), makeEvent("down"))
	waitFor(t, func() bool { return requestCount.Load() == 1 })
	p.Move(map[string]string{"Media/Jellyfin": "Sieć/Jellyfin"})
	close(release)
	p.Wait()

	if got := p.reportedState("Sieć/Jellyfin"); got != "down" {
		t.Fatalf("zgłoszony pod nowym kluczem = %q, chcę down", got)
	}
	z := makeEvent("down")
	z.Key = "Sieć/Jellyfin"
	p.Observe(context.Background(), z)
	p.Wait()
	if requestCount.Load() != 1 {
		t.Errorf("po przeniesieniu alert poszedł drugi raz (%d)", requestCount.Load())
	}
}

func TestServiceQueueIsBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	p := New(srv.URL, 1)
	// flipping between states that `decide` does not filter out (degraded↔down↔…) with a
	// blocked webhook must not grow without end
	for i := 0; i < 200; i++ {
		state := []string{"degraded", "down"}[i%2]
		p.Observe(context.Background(), makeEvent(state))
	}
	p.mu.Lock()
	n := 0
	if k := p.queues["Media/Jellyfin"]; k != nil {
		n = len(k.events)
	}
	p.mu.Unlock()
	if n > maxServiceQueue {
		t.Fatalf("kolejka urosła do %d (limit %d)", n, maxServiceQueue)
	}
	close(release)
	p.Wait()
}
