package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenAPIUtwórzIZweryfikuj(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()

	token, wpis, err := st.NowyTokenAPI(ctx, "prometheus", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, PrefiksTokenu) || len(token) != len(PrefiksTokenu)+64 {
		t.Fatalf("kształt tokenu: %q (%d)", token, len(token))
	}
	if wpis.Nazwa != "prometheus" || wpis.Autor != "admin" || wpis.ID == 0 {
		t.Errorf("wpis: %+v", wpis)
	}
	if wpis.Wskazówka == "" || strings.Contains(token, strings.TrimPrefix(wpis.Wskazówka, PrefiksTokenu)+"x") {
		t.Errorf("wskazówka: %q", wpis.Wskazówka)
	}

	got, err := st.SprawdźTokenAPI(ctx, token)
	if err != nil || got.ID != wpis.ID {
		t.Fatalf("SprawdźTokenAPI: %+v %v", got, err)
	}
	// każdy inny ciąg: odmowa tym samym błędem
	for _, zły := range []string{"", "shyg_", token + "0", token[:len(token)-1], strings.ToUpper(token), "inny"} {
		if _, err := st.SprawdźTokenAPI(ctx, zły); !errors.Is(err, ErrZłyToken) {
			t.Errorf("%q: err = %v, chcę ErrZłyToken", zły, err)
		}
	}
}

func TestTokenAPIWBazieLeżySkrótNieToken(t *testing.T) {
	st := otwórzTestowy(t)
	token, _, err := st.NowyTokenAPI(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	var zapisany string
	if err := st.db.QueryRow(`SELECT hash FROM api_tokens`).Scan(&zapisany); err != nil {
		t.Fatal(err)
	}
	if zapisany == token || strings.Contains(zapisany, strings.TrimPrefix(token, PrefiksTokenu)) {
		t.Fatalf("w bazie leży jawny token: %q", zapisany)
	}
	if zapisany != skrótTokenu(token) {
		t.Errorf("hash = %q, chcę sha256 tokenu", zapisany)
	}
}

func TestTokenAPIUnikalnePrzyKażdymWywołaniu(t *testing.T) {
	st := otwórzTestowy(t)
	a, _, _ := st.NowyTokenAPI(context.Background(), "a", "")
	b, _, _ := st.NowyTokenAPI(context.Background(), "a", "")
	if a == b {
		t.Fatal("dwa tokeny takie same")
	}
}

func TestTokenAPIOdwołanieIListaBezSekretów(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	t1, w1, _ := st.NowyTokenAPI(ctx, "pierwszy", "admin")
	t2, w2, _ := st.NowyTokenAPI(ctx, "drugi", "admin")

	lista, err := st.TokenyAPI(ctx)
	if err != nil || len(lista) != 2 {
		t.Fatalf("lista: %v %v", lista, err)
	}
	if lista[0].Nazwa != "pierwszy" || lista[1].Nazwa != "drugi" || !lista[0].OstatnieUżycie.IsZero() {
		t.Errorf("lista: %+v", lista)
	}

	if err := st.UsuńTokenAPI(ctx, w1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SprawdźTokenAPI(ctx, t1); !errors.Is(err, ErrZłyToken) {
		t.Errorf("odwołany token wciąż działa: %v", err)
	}
	if _, err := st.SprawdźTokenAPI(ctx, t2); err != nil {
		t.Errorf("cudzy token ucierpiał: %v", err)
	}
	if err := st.UsuńTokenAPI(ctx, 9999); !errors.Is(err, ErrBrakTokenu) {
		t.Errorf("usunięcie nieistniejącego: %v", err)
	}
	if lista, _ := st.TokenyAPI(ctx); len(lista) != 1 || lista[0].ID != w2.ID {
		t.Errorf("po odwołaniu: %+v", lista)
	}
}

func TestTokenAPILimitSztuk(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	for i := 0; i < MaksTokenówAPI; i++ {
		if _, _, err := st.NowyTokenAPI(ctx, "t", ""); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, _, err := st.NowyTokenAPI(ctx, "t", ""); !errors.Is(err, ErrZaDużoTokenów) {
		t.Fatalf("err = %v, chcę ErrZaDużoTokenów", err)
	}
}

// last_used_at pisze się najwyżej raz na minutę: Prometheus odpytuje co kilka
// sekund i nie ma powodu, żeby każdy scrape był zapisem do SQLite.
func TestTokenAPIOstatnieUżycieNajwyżejRazNaMinutę(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	_, w, _ := st.NowyTokenAPI(ctx, "x", "")
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	czytaj := func() time.Time {
		l, _ := st.TokenyAPI(ctx)
		return l[0].OstatnieUżycie
	}
	if err := st.OznaczUżycieTokenu(ctx, w.ID, t0); err != nil {
		t.Fatal(err)
	}
	if got := czytaj(); !got.Equal(t0) {
		t.Fatalf("pierwsze użycie: %v", got)
	}
	_ = st.OznaczUżycieTokenu(ctx, w.ID, t0.Add(30*time.Second))
	if got := czytaj(); !got.Equal(t0) {
		t.Errorf("zapis po 30 s nie powinien przejść: %v", got)
	}
	_ = st.OznaczUżycieTokenu(ctx, w.ID, t0.Add(61*time.Second))
	if got := czytaj(); !got.Equal(t0.Add(61 * time.Second)) {
		t.Errorf("po minucie zapis ma przejść: %v", got)
	}
}

func TestDostępnościWszystkichUsług(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	teraz := time.Now()
	zapisz := func(klucz, stan string, ile time.Duration) {
		if err := st.ZapiszWynik(ctx, Wynik{Klucz: klucz, Stan: stan, Kiedy: teraz.Add(-ile)}); err != nil {
			t.Fatal(err)
		}
	}
	zapisz("G/a", "up", time.Hour)
	zapisz("G/a", "up", 2*time.Hour)
	zapisz("G/a", "down", 3*time.Hour)
	zapisz("G/a", "up", 3*24*time.Hour) // poza oknem
	zapisz("G/b", "degraded", time.Hour)

	m, err := st.Dostępności(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a := m["G/a"]; a.Próbek != 3 || a.Udanych != 2 {
		t.Errorf("G/a: %+v", a)
	}
	if b := m["G/b"]; b.Próbek != 1 || b.Udanych != 0 {
		t.Errorf("G/b: %+v", b)
	}
	if _, ma := m["G/c"]; ma {
		t.Error("usługa bez próbek w oknie nie powinna być w mapie")
	}
}

// Usunięcie konta odwołuje tokeny, które to konto wystawiło; cudze zostają.
func TestUsunięcieUżytkownikaOdwołujeJegoTokeny(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	anna, _ := st.DodajUżytkownika(ctx, "anna", "haslo12345")
	bartek, _ := st.DodajUżytkownika(ctx, "bartek", "haslo12345")
	tAnny, _, err := st.NowyTokenAPI(ctx, "anny", "anna")
	if err != nil {
		t.Fatal(err)
	}
	tBartka, _, _ := st.NowyTokenAPI(ctx, "bartka", "bartek")
	tSkryptu, _, _ := st.NowyTokenAPI(ctx, "obcy", "nieznany") // autor bez konta

	if err := st.UsuńUżytkownika(ctx, anna.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SprawdźTokenAPI(ctx, tAnny); !errors.Is(err, ErrZłyToken) {
		t.Errorf("token usuniętego konta dalej działa: %v", err)
	}
	for nazwa, tok := range map[string]string{"bartka": tBartka, "bez konta": tSkryptu} {
		if _, err := st.SprawdźTokenAPI(ctx, tok); err != nil {
			t.Errorf("token %s zniknął: %v", nazwa, err)
		}
	}
	_ = bartek
}

// Stara baza (api_tokens bez user_id) dostaje kolumnę i przypisanie po loginie autora.
func TestMigracjaTokenówDopisujeUserID(t *testing.T) {
	ścieżka := filepath.Join(t.TempDir(), "stara.db")
	db, err := sql.Open("sqlite", ścieżka)
	if err != nil {
		t.Fatal(err)
	}
	token := PrefiksTokenu + strings.Repeat("ab", 32)
	_, err = db.Exec(`
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, login TEXT NOT NULL UNIQUE COLLATE NOCASE, hash TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT INTO users (id, login, hash) VALUES (7, 'Anna', 'x'), (8, 'bartek', 'x');
CREATE TABLE api_tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, last_used_at DATETIME);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_tokens (name, hash, hint, author) VALUES ('t', ?, 'shyg_abab', 'anna')`, skrótTokenu(token)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(ścieżka)
	if err != nil {
		t.Fatalf("Open starej bazy: %v", err)
	}
	defer st.Close()
	// drugie otwarcie (idempotencja migracji) nie może paść
	st2, err := Open(ścieżka)
	if err != nil {
		t.Fatalf("drugie Open: %v", err)
	}
	st2.Close()

	ctx := context.Background()
	if _, err := st.SprawdźTokenAPI(ctx, token); err != nil {
		t.Fatalf("token po migracji: %v", err)
	}
	if err := st.UsuńUżytkownika(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SprawdźTokenAPI(ctx, token); !errors.Is(err, ErrZłyToken) {
		t.Errorf("token przypisany po loginie przetrwał usunięcie konta: %v", err)
	}
}
