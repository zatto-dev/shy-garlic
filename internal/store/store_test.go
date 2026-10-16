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
