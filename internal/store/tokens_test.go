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

func TestAPITokenCreateAndVerify(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()

	token, entry, err := st.NewAPIToken(ctx, "prometheus", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || len(token) != len(TokenPrefix)+64 {
		t.Fatalf("kształt tokenu: %q (%d)", token, len(token))
	}
	if entry.Name != "prometheus" || entry.Author != "admin" || entry.ID == 0 {
		t.Errorf("wpis: %+v", entry)
	}
	if entry.Hint == "" || strings.Contains(token, strings.TrimPrefix(entry.Hint, TokenPrefix)+"x") {
		t.Errorf("wskazówka: %q", entry.Hint)
	}

	got, err := st.VerifyAPIToken(ctx, token)
	if err != nil || got.ID != entry.ID {
		t.Fatalf("VerifyAPIToken: %+v %v", got, err)
	}
	// every other string: refusal with the same error
	for _, bad := range []string{"", "shyg_", token + "0", token[:len(token)-1], strings.ToUpper(token), "inny"} {
		if _, err := st.VerifyAPIToken(ctx, bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%q: err = %v, want ErrInvalidToken", bad, err)
		}
	}
}

func TestAPITokenDBHoldsHashNotToken(t *testing.T) {
	st := openTest(t)
	token, _, err := st.NewAPIToken(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	var saved string
	if err := st.db.QueryRow(`SELECT hash FROM api_tokens`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved == token || strings.Contains(saved, strings.TrimPrefix(token, TokenPrefix)) {
		t.Fatalf("w bazie leży jawny token: %q", saved)
	}
	if saved != tokenHash(token) {
		t.Errorf("hash = %q, chcę sha256 tokenu", saved)
	}
}

func TestAPITokenUniqueOnEveryCall(t *testing.T) {
	st := openTest(t)
	a, _, _ := st.NewAPIToken(context.Background(), "a", "")
	b, _, _ := st.NewAPIToken(context.Background(), "a", "")
	if a == b {
		t.Fatal("dwa tokeny takie same")
	}
}

func TestAPITokenRevocationAndListWithoutSecrets(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	t1, w1, _ := st.NewAPIToken(ctx, "pierwszy", "admin")
	t2, w2, _ := st.NewAPIToken(ctx, "drugi", "admin")

	list, err := st.APITokens(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("lista: %v %v", list, err)
	}
	if list[0].Name != "pierwszy" || list[1].Name != "drugi" || !list[0].LastUsedAt.IsZero() {
		t.Errorf("lista: %+v", list)
	}

	if err := st.RemoveAPIToken(ctx, w1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.VerifyAPIToken(ctx, t1); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("odwołany token wciąż działa: %v", err)
	}
	if _, err := st.VerifyAPIToken(ctx, t2); err != nil {
		t.Errorf("cudzy token ucierpiał: %v", err)
	}
	if err := st.RemoveAPIToken(ctx, 9999); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("usunięcie nieistniejącego: %v", err)
	}
	if list, _ := st.APITokens(ctx); len(list) != 1 || list[0].ID != w2.ID {
		t.Errorf("po odwołaniu: %+v", list)
	}
}

func TestAPITokenCountLimit(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	for i := 0; i < MaxAPITokens; i++ {
		if _, _, err := st.NewAPIToken(ctx, "t", ""); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, _, err := st.NewAPIToken(ctx, "t", ""); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("err = %v, want ErrTooManyTokens", err)
	}
}

// last_used_at is written at most once a minute: Prometheus polls every few
// seconds and there is no reason for every scrape to be a write to SQLite.
func TestAPITokenLastUsedAtMostOncePerMinute(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	_, w, _ := st.NewAPIToken(ctx, "x", "")
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	read := func() time.Time {
		l, _ := st.APITokens(ctx)
		return l[0].LastUsedAt
	}
	if err := st.MarkTokenUsed(ctx, w.ID, t0); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.Equal(t0) {
		t.Fatalf("pierwsze użycie: %v", got)
	}
	_ = st.MarkTokenUsed(ctx, w.ID, t0.Add(30*time.Second))
	if got := read(); !got.Equal(t0) {
		t.Errorf("zapis po 30 s nie powinien przejść: %v", got)
	}
	_ = st.MarkTokenUsed(ctx, w.ID, t0.Add(61*time.Second))
	if got := read(); !got.Equal(t0.Add(61 * time.Second)) {
		t.Errorf("po minucie zapis ma przejść: %v", got)
	}
}

func TestAvailabilitiesOfAllServices(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Now()
	save := func(key, state string, count time.Duration) {
		if err := st.SaveResult(ctx, Result{Key: key, State: state, At: now.Add(-count)}); err != nil {
			t.Fatal(err)
		}
	}
	save("G/a", "up", time.Hour)
	save("G/a", "up", 2*time.Hour)
	save("G/a", "down", 3*time.Hour)
	save("G/a", "up", 3*24*time.Hour) // outside the window
	save("G/b", "degraded", time.Hour)

	m, err := st.Availabilities(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a := m["G/a"]; a.Samples != 3 || a.Successes != 2 {
		t.Errorf("G/a: %+v", a)
	}
	if b := m["G/b"]; b.Samples != 1 || b.Successes != 0 {
		t.Errorf("G/b: %+v", b)
	}
	if _, has := m["G/c"]; has {
		t.Error("usługa bez próbek w oknie nie powinna być w mapie")
	}
}

// Removing an account revokes the tokens that account issued; others' stay.
func TestRemovingUserRevokesTheirTokens(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	anna, _ := st.AddUser(ctx, "anna", "haslo12345")
	bartek, _ := st.AddUser(ctx, "bartek", "haslo12345")
	annaToken, _, err := st.NewAPIToken(ctx, "anny", "anna")
	if err != nil {
		t.Fatal(err)
	}
	bartekToken, _, _ := st.NewAPIToken(ctx, "bartka", "bartek")
	scriptToken, _, _ := st.NewAPIToken(ctx, "obcy", "nieznany") // author without an account

	if err := st.RemoveUser(ctx, anna.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.VerifyAPIToken(ctx, annaToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("token usuniętego konta dalej działa: %v", err)
	}
	for name, tok := range map[string]string{"bartka": bartekToken, "bez konta": scriptToken} {
		if _, err := st.VerifyAPIToken(ctx, tok); err != nil {
			t.Errorf("token %s zniknął: %v", name, err)
		}
	}
	_ = bartek
}

// An old database (api_tokens without user_id) gets the column and an assignment by the author's login.
func TestTokenMigrationBackfillsUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stara.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	token := TokenPrefix + strings.Repeat("ab", 32)
	_, err = db.Exec(`
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, login TEXT NOT NULL UNIQUE COLLATE NOCASE, hash TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT INTO users (id, login, hash) VALUES (7, 'Anna', 'x'), (8, 'bartek', 'x');
CREATE TABLE api_tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, last_used_at DATETIME);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_tokens (name, hash, hint, author) VALUES ('t', ?, 'shyg_abab', 'anna')`, tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open starej bazy: %v", err)
	}
	defer st.Close()
	// the second open (migration idempotency) must not fail
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("drugie Open: %v", err)
	}
	st2.Close()

	ctx := context.Background()
	if _, err := st.VerifyAPIToken(ctx, token); err != nil {
		t.Fatalf("token po migracji: %v", err)
	}
	if err := st.RemoveUser(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := st.VerifyAPIToken(ctx, token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("token przypisany po loginie przetrwał usunięcie konta: %v", err)
	}
}
