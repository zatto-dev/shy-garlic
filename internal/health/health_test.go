package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serwerZwracający(code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code == 302 {
			w.Header().Set("Location", "/gdzie-indziej")
		}
		w.WriteHeader(code)
	}))
}

// To jest test tezy produktu. Konkurencja oblewa każdy z tych przypadków.
func TestCheckSemantykaKodów(t *testing.T) {
	cases := []struct {
		kod  int
		chcę State
		czemu string
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
		s := serwerZwracający(c.kod)
		got := Check(context.Background(), m.client, Target{URL: s.URL, Timeout: 2 * time.Second})
		if got.State != c.chcę {
			t.Errorf("kod %d → %s, chcę %s (%s)", c.kod, got.State, c.chcę, c.czemu)
		}
		if got.Code != c.kod {
			t.Errorf("kod %d → zapisany %d", c.kod, got.Code)
		}
		s.Close()
	}
}

func TestCheckRozróżniaBrakPołączeniaOdZłegoKodu(t *testing.T) {
	s := serwerZwracający(200)
	adres := s.URL
	s.Close() // teraz nikt nie słucha

	m := New()
	defer m.Close()
	got := Check(context.Background(), m.client, Target{URL: adres, Timeout: 2 * time.Second})
	if got.State != StateDown {
		t.Fatalf("stan = %s, chcę down", got.State)
	}
	if got.Err != "connection refused" {
		t.Errorf("błąd = %q, chcę czytelnego 'connection refused'", got.Err)
	}
}

func TestCheckWłasnaListaExpect(t *testing.T) {
	s := serwerZwracający(418)
	defer s.Close()

	m := New()
	defer m.Close()
	// user, który wie, że jego usługa zwraca 418, ma móc to zadeklarować
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

// Scheduler ma chodzić po stronie serwera, bez otwartej przeglądarki.
func TestMonitorSprawdzaSamZSiebie(t *testing.T) {
	s := serwerZwracający(200)
	defer s.Close()

	m := New()
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	zmiany, odepnij := m.Subscribe()
	defer odepnij()

	m.SetTargets(ctx, []Target{{Key: "test", URL: s.URL, Interval: 50 * time.Millisecond, Timeout: time.Second}})

	select {
	case <-zmiany:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler nie odpalił sprawdzenia sam z siebie")
	}

	if got := m.Snapshot()["test"]; got.State != StateUp {
		t.Fatalf("stan = %s, chcę up", got.State)
	}
	// historia ma rosnąć bez udziału przeglądarki
	time.Sleep(200 * time.Millisecond)
	if len(m.History("test")) < 2 {
		t.Fatalf("historia = %d wpisów, chcę >= 2", len(m.History("test")))
	}
}
