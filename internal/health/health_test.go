package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func statusServer(code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code == 302 {
			w.Header().Set("Location", "/gdzie-indziej")
		}
		w.WriteHeader(code)
	}))
}

// This tests the product's thesis. The competition fails every one of these cases.
func TestCheckCodeSemantics(t *testing.T) {
	cases := []struct {
		code int
		want State
		why  string
	}{
		{200, StateUp, "oczywiste"},
		{301, StateUp, "Glance uznaje przekierowanie za awarię"},
		{302, StateUp, "przekierowanie to nie awaria"},
		{401, StateUp, "usługa żyje, tylko wymaga logowania"},
		{403, StateUp, "j.w. — Homepage świeci tu zielono z innego powodu: dopiero >403 to dla niego awaria"},
		{404, StateDegraded, "Homarr uznaje 404 za sukces, bo błąd zaczyna dopiero od 500"},
		{500, StateDegraded, "serwer żyje, ale się wywala — to nie to samo co brak połączenia"},
		{503, StateDegraded, "j.w."},
	}

	m := New()
	defer m.Close()
	for _, c := range cases {
		s := statusServer(c.code)
		got := Check(context.Background(), m.client, Target{URL: s.URL, Timeout: 2 * time.Second})
		if got.State != c.want {
			t.Errorf("kod %d → %s, chcę %s (%s)", c.code, got.State, c.want, c.why)
		}
		if got.Code != c.code {
			t.Errorf("kod %d → zapisany %d", c.code, got.Code)
		}
		s.Close()
	}
}

func TestCheckDistinguishesNoConnectionFromBadCode(t *testing.T) {
	s := statusServer(200)
	address := s.URL
	s.Close() // nobody is listening now

	m := New()
	defer m.Close()
	got := Check(context.Background(), m.client, Target{URL: address, Timeout: 2 * time.Second})
	if got.State != StateDown {
		t.Fatalf("stan = %s, chcę down", got.State)
	}
	if got.Err != "connection refused" {
		t.Errorf("błąd = %q, chcę czytelnego 'connection refused'", got.Err)
	}
}

func TestCheckCustomExpectList(t *testing.T) {
	s := statusServer(418)
	defer s.Close()

	m := New()
	defer m.Close()
	// a user who knows their service returns 418 must be able to declare it
	got := Check(context.Background(), m.client, Target{URL: s.URL, Expect: []int{418}, Timeout: 2 * time.Second})
	if got.State != StateUp {
		t.Fatalf("stan = %s, chcę up dla jawnie oczekiwanego 418", got.State)
	}
}

func TestCheckTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer s.Close()

	m := New()
	defer m.Close()
	got := Check(context.Background(), m.client, Target{URL: s.URL, Timeout: 50 * time.Millisecond})
	if got.State != StateDown || got.Err != "timeout" {
		t.Fatalf("stan = %s, err = %q; chcę down/timeout", got.State, got.Err)
	}
}

// The scheduler must run server-side, with no browser open.
func TestMonitorChecksOnItsOwn(t *testing.T) {
	s := statusServer(200)
	defer s.Close()

	m := New()
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changes, unsubscribe := m.Subscribe()
	defer unsubscribe()

	m.SetTargets(ctx, []Target{{Key: "test", URL: s.URL, Interval: 50 * time.Millisecond, Timeout: time.Second}})

	select {
	case <-changes:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler nie odpalił sprawdzenia sam z siebie")
	}

	if got := m.Snapshot()["test"]; got.State != StateUp {
		t.Fatalf("stan = %s, chcę up", got.State)
	}
	// history must grow without the browser's involvement
	time.Sleep(200 * time.Millisecond)
	if len(m.History("test")) < 2 {
		t.Fatalf("historia = %d wpisów, chcę >= 2", len(m.History("test")))
	}
}

// SHYG-41: after a key change the card keeps its state and chart.
func TestMoveKeysKeepsStateAndChart(t *testing.T) {
	m := New()
	defer m.Close()
	m.record("A/x", Result{State: StateUp, MS: 7, At: time.Now()})
	m.record("A/x", Result{State: StateDown, At: time.Now()})
	m.record("A/inny", Result{State: StateUp, At: time.Now()})

	m.MoveKeys(map[string]string{"A/x": "B/x", "Brak/brak": "C/c"})

	if got := m.Snapshot()["B/x"].State; got != StateDown {
		t.Errorf("stan pod nowym kluczem = %q, chcę down", got)
	}
	if _, has := m.Snapshot()["A/x"]; has {
		t.Error("stary klucz został w snapshocie")
	}
	if n := len(m.History("B/x")); n != 2 {
		t.Errorf("wykres pod nowym kluczem ma %d punktów, chcę 2", n)
	}
	if n := len(m.History("A/x")); n != 0 {
		t.Errorf("wykres pod starym kluczem ma %d punktów", n)
	}
	if m.Snapshot()["A/inny"].State != StateUp {
		t.Error("cudzy stan ruszony")
	}

	// SetTargets on a new key does not delete the state (no return to "unknown")
	m.SetTargets(context.Background(), []Target{{Key: "B/x", URL: "http://127.0.0.1:1", Interval: time.Hour}})
	if got := m.Snapshot()["B/x"].State; got == StateUnknown || got == "" {
		t.Errorf("SetTargets zgubił stan: %q", got)
	}
}

// SHYG-44: SetTargets called concurrently (reload from watch() and from a panel save)
// wrote m.cancel without a mutex — orphaned loops and a race on the WaitGroup.
// The test only makes full sense under -race; without it it at least checks there is no panic or leak.
func TestSetTargetsConcurrently(t *testing.T) {
	m := New()
	targets := []Target{
		{Key: "A/a", URL: "http://127.0.0.1:1", Interval: time.Hour, Timeout: 100 * time.Millisecond},
		{Key: "A/b", URL: "http://127.0.0.1:1", Interval: time.Hour, Timeout: 100 * time.Millisecond},
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				m.SetTargets(context.Background(), targets)
			}
		}()
	}
	wg.Wait()

	// afterwards exactly one full set of loops is running and Close ends them
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close wisi — zostały osierocone pętle")
	}
	m.wg.Wait() // nothing may remain after Close
}
