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

// Wysyłka nie może blokować wołającego: Obserwuj jest wywoływane z pętli, która
// zapisuje historię sprawdzeń do SQLite. Wcześniej ponowienia (do ok. 2,5 s)
// zatrzymywały zapis wyników wszystkich usług.
func TestObserwujNieBlokujeNaWolnymWebhooku(t *testing.T) {
	zwolnij := make(chan struct{})
	var zaczęto atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zaczęto.Add(1)
		<-zwolnij
		w.WriteHeader(204)
	}))
	defer srv.Close()
	p := Nowy(srv.URL, 1)

	start := time.Now()
	p.Obserwuj(context.Background(), zdarzenie("down"))
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Obserwuj czekało %v na webhook", d)
	}
	// wysyłka naprawdę trwa w tle
	czekaj(t, func() bool { return zaczęto.Load() == 1 })
	close(zwolnij)
	p.Poczekaj()
	if p.stanZgłoszony("Media/Jellyfin") != "down" {
		t.Error("po udanej wysyłce stan ma być zgłoszony")
	}
}

func czekaj(t *testing.T, warunek func() bool) {
	t.Helper()
	koniec := time.Now().Add(3 * time.Second)
	for !warunek() {
		if time.Now().After(koniec) {
			t.Fatal("warunek nie spełnił się w 3 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *Powiadamiacz) stanZgłoszony(klucz string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.stan[klucz]; s != nil {
		return s.zgłoszony
	}
	return ""
}

// Kolejne sprawdzenia tej samej awarii, gdy pierwsza wysyłka jeszcze trwa, nie
// mogą wystrzelić drugiego powiadomienia.
func TestNiePowielaAlertuWTrakcieWysyłki(t *testing.T) {
	zwolnij := make(chan struct{})
	var żądań atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		żądań.Add(1)
		<-zwolnij
	}))
	defer srv.Close()
	p := Nowy(srv.URL, 1)

	for i := 0; i < 5; i++ {
		p.Obserwuj(context.Background(), zdarzenie("down"))
	}
	czekaj(t, func() bool { return żądań.Load() >= 1 })
	for i := 0; i < 5; i++ {
		p.Obserwuj(context.Background(), zdarzenie("down"))
	}
	close(zwolnij)
	p.Poczekaj()
	if n := żądań.Load(); n != 1 {
		t.Fatalf("żądań = %d, chcę 1 mimo 10 sprawdzeń w trakcie wysyłki", n)
	}
}

// Zachowana semantyka SHYG-42: nieudana wysyłka nie oznacza stanu jako
// zgłoszonego, więc następne sprawdzenie próbuje jeszcze raz — także gdy
// wysyłka idzie w tle.
func TestNieudanaWysyłkaWTleZostawiaStanDoPonowienia(t *testing.T) {
	krótkieOpóźnienia(t)
	srv, licznik, mu := zawodnyWebhook(1 + len(opóźnieniaPonowień))
	defer srv.Close()
	p := Nowy(srv.URL, 1)

	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	if got := p.stanZgłoszony("Media/Jellyfin"); got != "up" {
		t.Fatalf("po nieudanej serii zgłoszony = %q, chcę up (do ponowienia)", got)
	}
	p.Obserwuj(context.Background(), zdarzenie("down"))
	p.Poczekaj()
	if got := p.stanZgłoszony("Media/Jellyfin"); got != "down" {
		t.Fatalf("po udanym ponowieniu zgłoszony = %q, chcę down", got)
	}
	mu.Lock()
	n := *licznik
	mu.Unlock()
	if n != 2+len(opóźnieniaPonowień) {
		t.Errorf("żądań = %d", n)
	}
}

// Dla jednej usługi wiadomości wychodzą w kolejności zdarzeń, nawet gdy pierwsza
// idzie wolno.
func TestKolejnośćWiadomościJednejUsługi(t *testing.T) {
	var mu sync.Mutex
	var stany []string
	var pierwsza atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct{ State string }
		_ = json.Unmarshal(b, &m)
		if !pierwsza.Swap(true) {
			time.Sleep(150 * time.Millisecond) // pierwsza wiadomość jest wolna
		}
		mu.Lock()
		stany = append(stany, m.State)
		mu.Unlock()
	}))
	defer srv.Close()
	p := Nowy(srv.URL, 1)
	p.UstawFormat(FormatGeneric)

	p.Obserwuj(context.Background(), zdarzenie("degraded"))
	p.Obserwuj(context.Background(), zdarzenie("down")) // degraded→down: druga wiadomość
	p.Poczekaj()

	mu.Lock()
	defer mu.Unlock()
	if len(stany) != 2 || stany[0] != "degraded" || stany[1] != "down" {
		t.Fatalf("kolejność = %v, chcę [degraded down]", stany)
	}
}

// Zablokowany webhook jednej usługi nie wstrzymuje powiadomień innych.
func TestWolnaUsługaNieBlokujeInnych(t *testing.T) {
	zwolnij := make(chan struct{})
	var mu sync.Mutex
	dostarczone := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct{ Service string }
		_ = json.Unmarshal(b, &m)
		if m.Service == "Wolna" {
			<-zwolnij
		}
		mu.Lock()
		dostarczone[m.Service] = true
		mu.Unlock()
	}))
	defer srv.Close()
	p := Nowy(srv.URL, 1)
	p.UstawFormat(FormatGeneric)

	wolna := zdarzenie("down")
	wolna.Klucz, wolna.Nazwa = "G/Wolna", "Wolna"
	szybka := zdarzenie("down")
	szybka.Klucz, szybka.Nazwa = "G/Szybka", "Szybka"
	p.Obserwuj(context.Background(), wolna)
	p.Obserwuj(context.Background(), szybka)

	czekaj(t, func() bool { mu.Lock(); defer mu.Unlock(); return dostarczone["Szybka"] })
	mu.Lock()
	if dostarczone["Wolna"] {
		t.Error("wolna już dostarczona — test nic nie dowodzi")
	}
	mu.Unlock()
	close(zwolnij)
	p.Poczekaj()
}

// Anulowanie kontekstu (zamykanie panelu) przerywa ponowienia i nie zgłasza
// niedostarczonego alertu jako dostarczonego.
func TestAnulowanieKontekstuPrzerywaPonowienia(t *testing.T) {
	stare := opóźnieniaPonowień
	opóźnieniaPonowień = []time.Duration{time.Hour, time.Hour}
	defer func() { opóźnieniaPonowień = stare }()

	srv, licznik, mu := zawodnyWebhook(1000)
	defer srv.Close()
	p := Nowy(srv.URL, 1)
	ctx, anuluj := context.WithCancel(context.Background())

	p.Obserwuj(ctx, zdarzenie("down"))
	czekaj(t, func() bool { mu.Lock(); defer mu.Unlock(); return *licznik >= 1 })
	anuluj()

	zrobione := make(chan struct{})
	go func() { p.Poczekaj(); close(zrobione) }()
	select {
	case <-zrobione:
	case <-time.After(2 * time.Second):
		t.Fatal("po anulowaniu wysyłka dalej czeka na ponowienie")
	}
	if got := p.stanZgłoszony("Media/Jellyfin"); got != "up" {
		t.Errorf("zgłoszony = %q po anulowanym alercie, chcę up", got)
	}
}

// Przeniesienie usługi w trakcie wysyłki: wynik ląduje w stanie, który
// przeżył przeniesienie, więc po powrocie nie ma dubla.
func TestPrzeniesienieWTrakcieWysyłkiNieGubiStanu(t *testing.T) {
	zwolnij := make(chan struct{})
	var żądań atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		żądań.Add(1)
		<-zwolnij
	}))
	defer srv.Close()
	p := Nowy(srv.URL, 1)

	p.Obserwuj(context.Background(), zdarzenie("down"))
	czekaj(t, func() bool { return żądań.Load() == 1 })
	p.Przenieś(map[string]string{"Media/Jellyfin": "Sieć/Jellyfin"})
	close(zwolnij)
	p.Poczekaj()

	if got := p.stanZgłoszony("Sieć/Jellyfin"); got != "down" {
		t.Fatalf("zgłoszony pod nowym kluczem = %q, chcę down", got)
	}
	z := zdarzenie("down")
	z.Klucz = "Sieć/Jellyfin"
	p.Obserwuj(context.Background(), z)
	p.Poczekaj()
	if żądań.Load() != 1 {
		t.Errorf("po przeniesieniu alert poszedł drugi raz (%d)", żądań.Load())
	}
}

func TestKolejkaUsługiJestOgraniczona(t *testing.T) {
	zwolnij := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-zwolnij }))
	defer srv.Close()
	p := Nowy(srv.URL, 1)
	// miotanie stanami, których `decyduj` nie odfiltruje (degraded↔down↔…) przy
	// zablokowanym webhooku nie może rosnąć bez końca
	for i := 0; i < 200; i++ {
		stan := []string{"degraded", "down"}[i%2]
		p.Obserwuj(context.Background(), zdarzenie(stan))
	}
	p.mu.Lock()
	n := 0
	if k := p.kolejki["Media/Jellyfin"]; k != nil {
		n = len(k.zdarzenia)
	}
	p.mu.Unlock()
	if n > maksKolejkiUsługi {
		t.Fatalf("kolejka urosła do %d (limit %d)", n, maksKolejkiUsługi)
	}
	close(zwolnij)
	p.Poczekaj()
}
