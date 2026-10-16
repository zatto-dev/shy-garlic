package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	srv    *httptest.Server
}

func newRecorder() *recorder {
	l := &recorder{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		l.mu.Lock()
		l.bodies = append(l.bodies, m)
		l.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	return l
}

func (l *recorder) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *recorder) title(i int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	embeds, _ := l.bodies[i]["embeds"].([]any)
	if len(embeds) == 0 {
		return ""
	}
	e, _ := embeds[0].(map[string]any)
	t, _ := e["title"].(string)
	return t
}

func makeEvent(state string) Event {
	return Event{Key: "Media/Jellyfin", Name: "Jellyfin", Group: "Media",
		URL: "http://x:8096", State: state, At: time.Now()}
}

// A single network blip must not wake anyone at night.
func TestSingleFailureDoesNotNotify(t *testing.T) {
	l := newRecorder()
	defer l.srv.Close()
	p := New(l.srv.URL, 2)

	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	if l.count() != 0 {
		t.Fatalf("wysłano %d wiadomości po jednej porażce, chcę 0", l.count())
	}

	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	if l.count() != 1 {
		t.Fatalf("wysłano %d po drugiej porażce, chcę 1", l.count())
	}
}

func TestDoesNotSpamSameState(t *testing.T) {
	l := newRecorder()
	defer l.srv.Close()
	p := New(l.srv.URL, 1)

	for i := 0; i < 5; i++ {
		p.Observe(context.Background(), makeEvent("down"))
		p.Wait()
	}
	if l.count() != 1 {
		t.Fatalf("wysłano %d wiadomości przy stałej awarii, chcę 1", l.count())
	}
}

func TestRecoveryReportedImmediately(t *testing.T) {
	l := newRecorder()
	defer l.srv.Close()
	p := New(l.srv.URL, 2)

	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	p.Observe(context.Background(), makeEvent("up"))
	p.Wait()

	if l.count() != 2 {
		t.Fatalf("wiadomości = %d, chcę 2 (awaria + powrót)", l.count())
	}
	if got := l.title(1); got == "" || got[:4] != "🟢"[:4] {
		t.Errorf("druga wiadomość to nie powrót: %q", got)
	}
}

// A service that has been up from the start generates no noise at all.
func TestOnlySuccessesStaySilent(t *testing.T) {
	l := newRecorder()
	defer l.srv.Close()
	p := New(l.srv.URL, 2)

	for i := 0; i < 10; i++ {
		p.Observe(context.Background(), makeEvent("up"))
		p.Wait()
	}
	if l.count() != 0 {
		t.Fatalf("wysłano %d wiadomości dla działającej usługi, chcę 0", l.count())
	}
}

func TestNothingHappensWithoutWebhook(t *testing.T) {
	p := New("", 2)
	if p.Enabled() {
		t.Fatal("pusty webhook ma być wyłączony")
	}
	p.Observe(context.Background(), makeEvent("down")) // must not panic
	p.Wait()
}

func TestDegradedAndDownAreSeparateStates(t *testing.T) {
	l := newRecorder()
	defer l.srv.Close()
	p := New(l.srv.URL, 1)

	p.Observe(context.Background(), makeEvent("degraded"))
	p.Wait()
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	if l.count() != 2 {
		t.Fatalf("wiadomości = %d, chcę 2 — przejście degraded→down to nowa informacja", l.count())
	}
}

func TestSetWebhookEnablesAndDisables(t *testing.T) {
	p := New("", 2)
	if p.Enabled() {
		t.Fatal("pusty webhook ma znaczyć wyłączone")
	}
	p.SetWebhook("https://example.test/hook")
	if !p.Enabled() {
		t.Fatal("po ustawieniu adresu ma być włączone")
	}
	if got := p.Webhook(); got != "https://example.test/hook" {
		t.Fatalf("chcę zapisany adres, mam %q", got)
	}
	p.SetWebhook("")
	if p.Enabled() {
		t.Fatal("wyczyszczenie adresu ma wyłączyć")
	}
}

// A threshold below 1 makes no sense — one failed check is not yet an
// outage, and zero would mean a notification on every blip.
func TestSetThresholdClamps(t *testing.T) {
	p := New("", 5)
	p.SetThreshold(0)
	if p.Threshold() != 1 {
		t.Fatalf("próg 0 ma się wyprostować do 1, mam %d", p.Threshold())
	}
	p.SetThreshold(4)
	if p.Threshold() != 4 {
		t.Fatalf("chcę 4, mam %d", p.Threshold())
	}
}

// Reading from the monitor loop and writing from an HTTP request run concurrently. The test
// makes sense only under -race, but it does no harm without it either.
func TestWebhookConcurrently(t *testing.T) {
	p := New("https://example.test/a", 2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			p.SetWebhook("https://example.test/b")
			p.SetWebhook("https://example.test/a")
		}
		close(done)
	}()
	for i := 0; i < 500; i++ {
		_ = p.Enabled()
		_ = p.Webhook()
	}
	<-done
}

// ── SHYG-42: a failed send must not lose the alert ──────────────────────────

// failingWebhook answers 500 for the first `fail` requests, then 204.
func failingWebhook(fail int) (*httptest.Server, *int, *sync.Mutex) {
	var mu sync.Mutex
	counter := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counter++
		n := counter
		mu.Unlock()
		if n <= fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return srv, &counter, &mu
}

func shortDelays(t *testing.T) {
	t.Helper()
	saved := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = saved })
}

// A transient webhook error within a single call → retry, the alert arrives.
func TestFailedSendIsRetried(t *testing.T) {
	shortDelays(t)
	srv, counter, mu := failingWebhook(1)
	defer srv.Close()
	p := New(srv.URL, 1)

	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()

	mu.Lock()
	defer mu.Unlock()
	if *counter != 2 {
		t.Fatalf("żądań = %d, chcę 2 (porażka + ponowienie)", *counter)
	}
	// delivered → the next "down" must not spam
	mu.Unlock()
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	mu.Lock()
	if *counter != 2 {
		t.Fatalf("po udanej wysyłce kolejny down wysłał ponownie (%d żądań)", *counter)
	}
}

// The webhook is down through all attempts → the alert is NOT considered reported,
// the next check tries again (before: lost forever).
func TestSendFailureLeavesStateForRetryOnNextCheck(t *testing.T) {
	shortDelays(t)
	srv, counter, mu := failingWebhook(1 + len(retryDelays)) // the whole first series fails
	defer srv.Close()
	p := New(srv.URL, 1)

	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	mu.Lock()
	first := *counter
	mu.Unlock()
	if first != 1+len(retryDelays) {
		t.Fatalf("żądań w pierwszej serii = %d, chcę %d", first, 1+len(retryDelays))
	}

	p.Observe(context.Background(), makeEvent("down")) // the next check
	p.Wait()
	mu.Lock()
	defer mu.Unlock()
	if *counter != first+1 {
		t.Fatalf("następne sprawdzenie nie ponowiło alertu (żądań %d)", *counter)
	}
	// and now delivered — silence
	mu.Unlock()
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	mu.Lock()
	if *counter != first+1 {
		t.Fatalf("po dostarczeniu alert poszedł znowu (%d)", *counter)
	}
}

// A failed recovery must be retried too.
func TestFailedRecoveryIsRetried(t *testing.T) {
	shortDelays(t)
	l := newRecorder()
	p := New(l.srv.URL, 1)
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	l.srv.Close() // the webhook disappears

	p.Observe(context.Background(), makeEvent("up"))
	p.Wait()
	p.mu.Lock()
	z := p.state["Media/Jellyfin"].reported
	p.mu.Unlock()
	if z != "down" {
		t.Fatalf("po nieudanym powrocie zgłoszony = %q, chcę down (do ponowienia)", z)
	}
}

// The "unknown" state used to return with the mutex held — the next call hung.
func TestUnknownDoesNotBlockMutex(t *testing.T) {
	p := New("https://example.test/h", 1)
	p.Observe(context.Background(), makeEvent("unknown"))
	p.Wait()
	done := make(chan struct{})
	go func() {
		p.Forget(map[string]bool{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("mutex powiadamiacza został zajęty po stanie unknown")
	}
}

// SHYG-41: the outage continues despite the service being moved — we do not alert from scratch.
func TestMoveKeepsReportedState(t *testing.T) {
	l := newRecorder()
	defer l.srv.Close()
	p := New(l.srv.URL, 1)
	p.Observe(context.Background(), makeEvent("down"))
	p.Wait()
	if l.count() != 1 {
		t.Fatalf("setup: wysłano %d", l.count())
	}

	p.Move(map[string]string{"Media/Jellyfin": "Sieć/Jellyfin"})
	p.Forget(map[string]bool{"Sieć/Jellyfin": true}) // this is what reload() does

	z := makeEvent("down")
	z.Key = "Sieć/Jellyfin"
	p.Observe(context.Background(), z)
	p.Wait()
	if l.count() != 1 {
		t.Fatalf("po przeniesieniu alert poszedł drugi raz (%d)", l.count())
	}
	z.State = "up"
	p.Observe(context.Background(), z)
	p.Wait()
	if l.count() != 2 {
		t.Fatalf("powrót po przeniesieniu nie został zgłoszony (%d)", l.count())
	}
}
