package store

import (
	"context"
	"path/filepath"
	"testing"
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
