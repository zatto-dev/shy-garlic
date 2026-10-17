package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSettingMissingKey(t *testing.T) {
	st := openTest(t)
	v, err := st.Setting(context.Background(), "notify.webhook")
	if err != nil {
		t.Fatalf("brak klucza ma być ciszą, dostałem błąd: %v", err)
	}
	if v != "" {
		t.Fatalf("brak klucza ma dać pusty string, dostałem %q", v)
	}
}

func TestSettingWriteAndRead(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "notify.webhook", "https://example.test/hook"); err != nil {
		t.Fatalf("UstawUstawienie: %v", err)
	}
	v, err := st.Setting(ctx, "notify.webhook")
	if err != nil {
		t.Fatalf("Ustawienie: %v", err)
	}
	if v != "https://example.test/hook" {
		t.Fatalf("chcę zapisany adres, mam %q", v)
	}
}

func TestSettingOverwrite(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	_ = st.SetSetting(ctx, "k", "pierwsza")
	if err := st.SetSetting(ctx, "k", "druga"); err != nil {
		t.Fatalf("nadpisanie: %v", err)
	}
	v, _ := st.Setting(ctx, "k")
	if v != "druga" {
		t.Fatalf("chcę druga, mam %q", v)
	}
}

// An empty value deletes the row instead of saving an empty string. Thanks to that
// "clear the webhook" and "there never was a webhook" are the same state and the rest of the code
// does not have to tell them apart.
func TestSettingEmptyDeletes(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	_ = st.SetSetting(ctx, "k", "coś")
	if err := st.SetSetting(ctx, "k", ""); err != nil {
		t.Fatalf("kasowanie: %v", err)
	}
	v, err := st.Setting(ctx, "k")
	if err != nil || v != "" {
		t.Fatalf("chcę pusto bez błędu, mam %q / %v", v, err)
	}
}

// ── SHYG-41: history survives a key change ──────────────────────────────────

func saveN(t *testing.T, st *Store, key string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := st.SaveResult(context.Background(), Result{
			Key: key, State: "up", Code: 200, MS: 5, At: time.Now().Add(-time.Duration(n-i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMoveKeysKeepsHistory(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	saveN(t, st, "Media/Sonarr", 3)
	saveN(t, st, "Media/Inna", 2)

	if err := st.MoveKeys(ctx, map[string]string{"Media/Sonarr": "Sieć/Sonarr"}); err != nil {
		t.Fatalf("MoveKeys: %v", err)
	}
	if h, _ := st.History(ctx, "Sieć/Sonarr", 10); len(h) != 3 {
		t.Errorf("pod nowym kluczem %d wierszy, chcę 3", len(h))
	}
	if h, _ := st.History(ctx, "Media/Sonarr", 10); len(h) != 0 {
		t.Errorf("pod starym kluczem zostało %d wierszy", len(h))
	}
	if h, _ := st.History(ctx, "Media/Inna", 10); len(h) != 2 {
		t.Errorf("cudza historia ruszona: %d wierszy, chcę 2", len(h))
	}
	if _, n, _ := st.Availability(ctx, "Sieć/Sonarr", time.Hour); n != 3 {
		t.Errorf("Dostępność widzi %d próbek, chcę 3", n)
	}
}

// Renaming a group means many pairs at once; a name with "/" must not mix up keys.
func TestMoveKeysManyPairsAndNameWithSlash(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	saveN(t, st, "A/x", 1)
	saveN(t, st, "A/y", 2)
	saveN(t, st, "A/b/c", 4) // group "A/b", service "c" — this is NOT group A

	err := st.MoveKeys(ctx, map[string]string{"A/x": "B/x", "A/y": "B/y"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int{"B/x": 1, "B/y": 2, "A/b/c": 4, "A/x": 0} {
		if h, _ := st.History(ctx, k, 10); len(h) != want {
			t.Errorf("%s: %d wierszy, chcę %d", k, len(h), want)
		}
	}
}

func TestMoveKeysMergesWithExistingAndEmptyMapIsNoop(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	saveN(t, st, "A/x", 2)
	saveN(t, st, "B/x", 3)
	if err := st.MoveKeys(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveKeys(ctx, map[string]string{"A/x": "B/x", "Z/z": "Y/y"}); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.History(ctx, "B/x", 10); len(h) != 5 {
		t.Errorf("po scaleniu %d wierszy, chcę 5", len(h))
	}
}

// ── SHYG-43d: sessions ──────────────────────────────────────────────────────

func TestRemoveOtherSessionsKeepsGivenOneAndOtherUsers(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	a, _ := st.AddUser(ctx, "anna", "haslo12345")
	b, _ := st.AddUser(ctx, "bartek", "haslo12345")
	t1, _, _ := st.NewSession(ctx, a.ID)
	t2, _, _ := st.NewSession(ctx, a.ID)
	t3, _, _ := st.NewSession(ctx, b.ID)

	if err := st.RemoveOtherSessions(ctx, a.ID, t1); err != nil {
		t.Fatalf("RemoveOtherSessions: %v", err)
	}
	if _, err := st.Session(ctx, t1); err != nil {
		t.Errorf("wskazana sesja ma zostać: %v", err)
	}
	if _, err := st.Session(ctx, t2); err == nil {
		t.Error("druga sesja użytkownika ma zniknąć")
	}
	if _, err := st.Session(ctx, t3); err != nil {
		t.Errorf("sesja innego użytkownika ma zostać: %v", err)
	}
}

// The token in the database is a hash — a leak of the .db file yields no working cookies.
func TestSessionStoredAsHashInDB(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	u, _ := st.AddUser(ctx, "anna", "haslo12345")
	tok, _, err := st.NewSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var inDB string
	if err := st.db.QueryRow(`SELECT token FROM sessions`).Scan(&inDB); err != nil {
		t.Fatal(err)
	}
	if inDB == tok || len(inDB) != 64 {
		t.Errorf("w bazie leży %q (token %q) — ma być sha256 hex", inDB, tok)
	}
	if _, err := st.Session(ctx, tok); err != nil {
		t.Errorf("sesja po tokenie nie działa: %v", err)
	}
	if _, err := st.Session(ctx, inDB); err == nil {
		t.Error("skrót z bazy nie może działać jako token")
	}
	if err := st.RemoveSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Session(ctx, tok); err == nil {
		t.Error("sesja po wylogowaniu nadal działa")
	}
}

// An old database keeps tokens in plain form; after an upgrade, logged-in users stay logged in.
func TestMigrationOfPlainSessionTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stara.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, _ := st.AddUser(ctx, "anna", "haslo12345")
	const oldKey = "jawnyTokenZeStarejWersjiAplikacji0123456789ab"
	if _, err := st.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		oldKey, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path) // migrate() on reopening
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Session(ctx, oldKey); err != nil {
		t.Errorf("sesja ze starej wersji przestała działać: %v", err)
	}
	var inDB string
	_ = st.db.QueryRow(`SELECT token FROM sessions`).Scan(&inDB)
	if inDB == oldKey {
		t.Error("jawny token został w bazie")
	}
	// idempotent: the next open does not hash a hash a second time
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Session(ctx, oldKey); err != nil {
		t.Errorf("sesja padła po drugiej migracji: %v", err)
	}
}
