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

// serwerZTreścią odpowiada zadanym kodem i ciałem, a ścieżki, o które go
// pytano, zapamiętuje — test sprawdza, dokąd naprawdę poszło żądanie.
func serwerZTreścią(kod int, ciało string) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var ścieżki []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ścieżki = append(ścieżki, r.URL.RequestURI())
		mu.Unlock()
		w.WriteHeader(kod)
		_, _ = w.Write([]byte(ciało))
	}))
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ścieżki...)
	}
}

func sprawdź(t *testing.T, cel Target) Result {
	t.Helper()
	m := New()
	defer m.Close()
	if cel.Timeout == 0 {
		cel.Timeout = 2 * time.Second
	}
	return Check(context.Background(), m.client, cel)
}

func TestCheckContainsZnalezioneToUp(t *testing.T) {
	s, _ := serwerZTreścią(200, `<html><title>Jellyfin</title></html>`)
	defer s.Close()
	if r := sprawdź(t, Target{URL: s.URL, Contains: "Jellyfin"}); r.State != StateUp || r.Err != "" {
		t.Fatalf("%+v", r)
	}
}

// Serwer odpowiada, tylko nie tym, co trzeba — to degraded, nie down: tak samo
// jak zły kod HTTP.
func TestCheckContainsBrakToDegradedZPowodem(t *testing.T) {
	s, _ := serwerZTreścią(200, `Welcome to nginx!`)
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL, Contains: "Jellyfin"})
	if r.State != StateDegraded {
		t.Fatalf("stan = %s, chcę degraded", r.State)
	}
	if r.Code != 200 {
		t.Errorf("kod = %d: serwer odpowiedział 200 i to ma być widać", r.Code)
	}
	if !strings.HasPrefix(r.Err, PrefiksTreści) || !strings.Contains(r.Err, "Jellyfin") {
		t.Errorf("powód = %q", r.Err)
	}
}

func TestCheckNotContains(t *testing.T) {
	s, _ := serwerZTreścią(200, `<h1>Maintenance mode</h1>`)
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL, NotContains: "Maintenance"})
	if r.State != StateDegraded || !strings.Contains(r.Err, "Maintenance") {
		t.Fatalf("%+v", r)
	}
	if r := sprawdź(t, Target{URL: s.URL, NotContains: "Error 500"}); r.State != StateUp {
		t.Fatalf("brak zakazanego tekstu to up: %+v", r)
	}
}

func TestCheckContainsINotContainsRazem(t *testing.T) {
	s, _ := serwerZTreścią(200, `ok but with warning`)
	defer s.Close()
	if r := sprawdź(t, Target{URL: s.URL, Contains: "ok", NotContains: "warning"}); r.State != StateDegraded {
		t.Fatalf("%+v", r)
	}
	if r := sprawdź(t, Target{URL: s.URL, Contains: "ok", NotContains: "panic"}); r.State != StateUp {
		t.Fatalf("%+v", r)
	}
}

// Zły kod ma pierwszeństwo: nie ma sensu czytać ciała odpowiedzi, która już
// jest błędem, a powód „HTTP 500" mówi więcej niż „brak tekstu".
func TestCheckZłyKodPrzedTreścią(t *testing.T) {
	s, _ := serwerZTreścią(500, `Jellyfin`)
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL, Contains: "Jellyfin"})
	if r.State != StateDegraded || r.Err != "HTTP 500" {
		t.Fatalf("%+v", r)
	}
}

func TestCheckPathZastępujeŚcieżkęAdresu(t *testing.T) {
	s, ścieżki := serwerZTreścią(200, `{"status":"ok"}`)
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL + "/ui/index.html?x=1", Path: "/api/health?deep=1", Contains: `"ok"`})
	if r.State != StateUp {
		t.Fatalf("%+v", r)
	}
	if got := ścieżki(); len(got) != 1 || got[0] != "/api/health?deep=1" {
		t.Fatalf("żądanie poszło na %v; chcę /api/health?deep=1 (ścieżka i query z adresu zastąpione)", got)
	}
}

func TestCheckBezPathUżywaAdresuJakDotąd(t *testing.T) {
	s, ścieżki := serwerZTreścią(200, ``)
	defer s.Close()
	sprawdź(t, Target{URL: s.URL + "/admin?a=b"})
	if got := ścieżki(); len(got) != 1 || got[0] != "/admin?a=b" {
		t.Fatalf("%v", got)
	}
}

// path jest ścieżką NA TYM SAMYM hoście: adres w środku nie może przekierować
// sprawdzania gdzie indziej.
func TestCheckPathNieMożeZmienićHosta(t *testing.T) {
	s, ścieżki := serwerZTreścią(200, ``)
	defer s.Close()
	for _, zły := range []string{"//evil.example/x", "http://evil.example/x", "health"} {
		r := sprawdź(t, Target{URL: s.URL, Path: zły})
		if r.State != StateDown || !strings.Contains(r.Err, "check.path") {
			t.Errorf("path %q: %+v", zły, r)
		}
	}
	if got := ścieżki(); len(got) != 0 {
		t.Errorf("serwer dostał żądania mimo złego path: %v", got)
	}
}

func TestCheckCiałoCzytaneTylkoDoLimitu(t *testing.T) {
	// igła leży ZA limitem — nie wolno jej znaleźć, a odpowiedź ma wyjaśnić dlaczego
	ciało := strings.Repeat("a", maksCiała+100) + "IGŁA"
	s, _ := serwerZTreścią(200, ciało)
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL, Contains: "IGŁA"})
	if r.State != StateDegraded || !strings.Contains(r.Err, "1 MiB") {
		t.Fatalf("%+v", r)
	}
	// ta sama treść w granicach limitu jest znajdowana
	s2, _ := serwerZTreścią(200, strings.Repeat("a", maksCiała-10)+"IGŁA")
	defer s2.Close()
	if r := sprawdź(t, Target{URL: s2.URL, Contains: "IGŁA"}); r.State != StateUp {
		t.Fatalf("igła tuż przed limitem: %+v", r)
	}
}

// Serwer przysyła nagłówki i milknie: timeout obejmuje też czytanie ciała.
func TestCheckTimeoutPodczasCzytaniaCiała(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL, Contains: "x", Timeout: 150 * time.Millisecond})
	if r.State != StateDown || r.Err != "timeout" {
		t.Fatalf("%+v", r)
	}
}

func TestCheckContainsNieDotyczyTCP(t *testing.T) {
	s, _ := serwerZTreścią(200, ``)
	defer s.Close()
	adres := "tcp://" + strings.TrimPrefix(s.URL, "http://")
	if r := sprawdź(t, Target{URL: adres, Contains: "x", Path: "/y"}); r.State != StateUp {
		t.Fatalf("%+v", r)
	}
}

func TestCheckPowódTreściJestKrótki(t *testing.T) {
	s, _ := serwerZTreścią(200, `x`)
	defer s.Close()
	r := sprawdź(t, Target{URL: s.URL, Contains: strings.Repeat("ż", 500)})
	if n := len([]rune(r.Err)); n > 100 {
		t.Errorf("powód ma %d znaków — nie zmieści się w kafelku", n)
	}
}
