package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func otwórzTestowy(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestUstawienieBrakKlucza(t *testing.T) {
	st := otwórzTestowy(t)
	v, err := st.Ustawienie(context.Background(), "notify.webhook")
	if err != nil {
		t.Fatalf("brak klucza ma być ciszą, dostałem błąd: %v", err)
	}
	if v != "" {
		t.Fatalf("brak klucza ma dać pusty string, dostałem %q", v)
	}
}

func TestUstawienieZapisIOdczyt(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	if err := st.UstawUstawienie(ctx, "notify.webhook", "https://example.test/hook"); err != nil {
		t.Fatalf("UstawUstawienie: %v", err)
	}
	v, err := st.Ustawienie(ctx, "notify.webhook")
	if err != nil {
		t.Fatalf("Ustawienie: %v", err)
	}
	if v != "https://example.test/hook" {
		t.Fatalf("chcę zapisany adres, mam %q", v)
	}
}

func TestUstawienieNadpisanie(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	_ = st.UstawUstawienie(ctx, "k", "pierwsza")
	if err := st.UstawUstawienie(ctx, "k", "druga"); err != nil {
		t.Fatalf("nadpisanie: %v", err)
	}
	v, _ := st.Ustawienie(ctx, "k")
	if v != "druga" {
		t.Fatalf("chcę druga, mam %q", v)
	}
}

// Pusta wartość kasuje wiersz, a nie zapisuje pustego stringa. Dzięki temu
// „wyczyść webhook" i „nigdy nie było webhooka" to ten sam stan i reszta kodu
// nie musi ich rozróżniać.
func TestUstawieniePustaKasuje(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	_ = st.UstawUstawienie(ctx, "k", "coś")
	if err := st.UstawUstawienie(ctx, "k", ""); err != nil {
		t.Fatalf("kasowanie: %v", err)
	}
	v, err := st.Ustawienie(ctx, "k")
	if err != nil || v != "" {
		t.Fatalf("chcę pusto bez błędu, mam %q / %v", v, err)
	}
}

// ── SHYG-41: historia przeżywa zmianę klucza ─────────────────────────────────

func zapiszN(t *testing.T, st *Store, klucz string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := st.ZapiszWynik(context.Background(), Wynik{
			Klucz: klucz, Stan: "up", Kod: 200, MS: 5, Kiedy: time.Now().Add(-time.Duration(n-i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrzenieśKluczeZachowujeHistorię(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	zapiszN(t, st, "Media/Sonarr", 3)
	zapiszN(t, st, "Media/Inna", 2)

	if err := st.PrzenieśKlucze(ctx, map[string]string{"Media/Sonarr": "Sieć/Sonarr"}); err != nil {
		t.Fatalf("PrzenieśKlucze: %v", err)
	}
	if h, _ := st.Historia(ctx, "Sieć/Sonarr", 10); len(h) != 3 {
		t.Errorf("pod nowym kluczem %d wierszy, chcę 3", len(h))
	}
	if h, _ := st.Historia(ctx, "Media/Sonarr", 10); len(h) != 0 {
		t.Errorf("pod starym kluczem zostało %d wierszy", len(h))
	}
	if h, _ := st.Historia(ctx, "Media/Inna", 10); len(h) != 2 {
		t.Errorf("cudza historia ruszona: %d wierszy, chcę 2", len(h))
	}
	if _, n, _ := st.Dostępność(ctx, "Sieć/Sonarr", time.Hour); n != 3 {
		t.Errorf("Dostępność widzi %d próbek, chcę 3", n)
	}
}

// Zmiana nazwy grupy to wiele par naraz; nazwa z „/" nie może mieszać kluczy.
func TestPrzenieśKluczeWieleParINazwaZUkośnikiem(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	zapiszN(t, st, "A/x", 1)
	zapiszN(t, st, "A/y", 2)
	zapiszN(t, st, "A/b/c", 4) // grupa „A/b", usługa „c" — to NIE jest grupa A

	err := st.PrzenieśKlucze(ctx, map[string]string{"A/x": "B/x", "A/y": "B/y"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int{"B/x": 1, "B/y": 2, "A/b/c": 4, "A/x": 0} {
		if h, _ := st.Historia(ctx, k, 10); len(h) != want {
			t.Errorf("%s: %d wierszy, chcę %d", k, len(h), want)
		}
	}
}

func TestPrzenieśKluczeŁączyZIstniejącymiIPustaMapaToNic(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	zapiszN(t, st, "A/x", 2)
	zapiszN(t, st, "B/x", 3)
	if err := st.PrzenieśKlucze(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.PrzenieśKlucze(ctx, map[string]string{"A/x": "B/x", "Z/z": "Y/y"}); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.Historia(ctx, "B/x", 10); len(h) != 5 {
		t.Errorf("po scaleniu %d wierszy, chcę 5", len(h))
	}
}

// ── SHYG-43d: sesje ──────────────────────────────────────────────────────────

func TestUsuńInneSesjeZostawiaWskazanąIInnychUżytkowników(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	a, _ := st.DodajUżytkownika(ctx, "anna", "haslo12345")
	b, _ := st.DodajUżytkownika(ctx, "bartek", "haslo12345")
	t1, _, _ := st.NowaSesja(ctx, a.ID)
	t2, _, _ := st.NowaSesja(ctx, a.ID)
	t3, _, _ := st.NowaSesja(ctx, b.ID)

	if err := st.UsuńInneSesje(ctx, a.ID, t1); err != nil {
		t.Fatalf("UsuńInneSesje: %v", err)
	}
	if _, err := st.Sesja(ctx, t1); err != nil {
		t.Errorf("wskazana sesja ma zostać: %v", err)
	}
	if _, err := st.Sesja(ctx, t2); err == nil {
		t.Error("druga sesja użytkownika ma zniknąć")
	}
	if _, err := st.Sesja(ctx, t3); err != nil {
		t.Errorf("sesja innego użytkownika ma zostać: %v", err)
	}
}

// Token w bazie to skrót — wyciek pliku .db nie daje działających ciasteczek.
func TestSesjaWBaziePrzechowywanaJakoSkrót(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	u, _ := st.DodajUżytkownika(ctx, "anna", "haslo12345")
	tok, _, err := st.NowaSesja(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wBazie string
	if err := st.db.QueryRow(`SELECT token FROM sessions`).Scan(&wBazie); err != nil {
		t.Fatal(err)
	}
	if wBazie == tok || len(wBazie) != 64 {
		t.Errorf("w bazie leży %q (token %q) — ma być sha256 hex", wBazie, tok)
	}
	if _, err := st.Sesja(ctx, tok); err != nil {
		t.Errorf("sesja po tokenie nie działa: %v", err)
	}
	if _, err := st.Sesja(ctx, wBazie); err == nil {
		t.Error("skrót z bazy nie może działać jako token")
	}
	if err := st.UsuńSesję(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sesja(ctx, tok); err == nil {
		t.Error("sesja po wylogowaniu nadal działa")
	}
}

// Stara baza trzyma tokeny jawnie; po aktualizacji zalogowani zostają zalogowani.
func TestMigracjaJawnychTokenówSesji(t *testing.T) {
	ścieżka := filepath.Join(t.TempDir(), "stara.db")
	st, err := Open(ścieżka)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, _ := st.DodajUżytkownika(ctx, "anna", "haslo12345")
	const stary = "jawnyTokenZeStarejWersjiAplikacji0123456789ab"
	if _, err := st.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		stary, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(ścieżka) // migruj() przy ponownym otwarciu
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Sesja(ctx, stary); err != nil {
		t.Errorf("sesja ze starej wersji przestała działać: %v", err)
	}
	var wBazie string
	_ = st.db.QueryRow(`SELECT token FROM sessions`).Scan(&wBazie)
	if wBazie == stary {
		t.Error("jawny token został w bazie")
	}
	// idempotentne: kolejne otwarcie nie hashuje skrótu drugi raz
	st.Close()
	st, err = Open(ścieżka)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Sesja(ctx, stary); err != nil {
		t.Errorf("sesja padła po drugiej migracji: %v", err)
	}
}
