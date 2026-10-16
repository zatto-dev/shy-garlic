// Package store trzyma to, czego nie da się trzymać w pliku YAML:
// użytkowników, sesje i historię sprawdzeń.
//
// SQLite w czystym Go (modernc.org/sqlite) — bez cgo, więc binarka zostaje
// statyczna i obraz nadal jest jednoplikowy.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var (
	ErrZłeDane     = errors.New("invalid credentials")
	ErrBrakUżytkow = errors.New("user not found")
	ErrJużIstnieje = errors.New("user already exists")
)

// CzasSesji — jak długo żyje ciasteczko logowania.
const CzasSesji = 30 * 24 * time.Hour

type Store struct{ db *sql.DB }

type Użytkownik struct {
	ID        int64
	Login     string
	Utworzony time.Time
}

type Wynik struct {
	Klucz string
	Stan  string
	Kod   int
	MS    int64
	Błąd  string
	Kiedy time.Time
}

func Open(ścieżka string) (*Store, error) {
	db, err := sql.Open("sqlite", ścieżka+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite nie lubi równoległych zapisów; jeden pisarz wystarcza w zupełności
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migruj(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migruj() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  login      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  hash       TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS checks (
  id    INTEGER PRIMARY KEY AUTOINCREMENT,
  key   TEXT NOT NULL,
  state TEXT NOT NULL,
  code  INTEGER,
  ms    INTEGER,
  err   TEXT,
  at    DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS checks_key_at ON checks(key, at DESC);
CREATE TABLE IF NOT EXISTS config_versions (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  author TEXT NOT NULL DEFAULT '',
  opis   TEXT NOT NULL DEFAULT '',
  tresc  TEXT NOT NULL,
  diff   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS config_versions_at ON config_versions(at DESC);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`)
	if err != nil {
		return err
	}
	return s.zahaszujSesje()
}

// skrótTokenu — w bazie leży sha256 tokenu, nie sam token: wyciek pliku .db
// (backup, snapshot) nie daje wtedy działających ciasteczek. Token ma 256 bitów
// losowości, więc szybki skrót wystarcza (bcrypt byłby tu bez sensu).
func skrótTokenu(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// zahaszujSesje przepisuje jawne tokeny ze starych baz na skróty, żeby po
// aktualizacji nikt nie został wylogowany. Idempotentne: skrót ma 64 znaki
// (hex), jawny token 43 (base64 z 32 bajtów).
func (s *Store) zahaszujSesje() error {
	rows, err := s.db.Query(`SELECT token FROM sessions WHERE length(token) <> 64`)
	if err != nil {
		return err
	}
	var jawne []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		jawne = append(jawne, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range jawne {
		if _, err := s.db.Exec(`UPDATE sessions SET token = ? WHERE token = ?`, skrótTokenu(t), t); err != nil {
			return err
		}
	}
	return nil
}

// ── ustawienia instancji ─────────────────────────────────────────────────────
//
// Klucz-wartość dla rzeczy, które NIE mogą trafić do pliku konfiguracyjnego,
// bo ten jest pokazywany w UI i commitowany do gita. Dziś mieszka tu jedno:
// adres webhooka powiadomień, czyli poświadczenie.

// Ustawienie zwraca wartość albo pusty string, gdy klucza nie ma. Brak klucza
// nie jest błędem — wywołujący i tak musiałby go odróżniać od pustej wartości,
// a nie ma po co.
func (s *Store) Ustawienie(ctx context.Context, klucz string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, klucz).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// UstawUstawienie zapisuje wartość. Pusta kasuje wiersz.
func (s *Store) UstawUstawienie(ctx context.Context, klucz, wartość string) error {
	if wartość == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, klucz)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value, at) VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, at = CURRENT_TIMESTAMP`, klucz, wartość)
	return err
}

// ── użytkownicy ──────────────────────────────────────────────────────────────

func (s *Store) LiczbaUżytkowników(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) DodajUżytkownika(ctx context.Context, login, hasło string) (*Użytkownik, error) {
	if len(login) < 2 {
		return nil, errors.New("login too short")
	}
	if len(hasło) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(hasło), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (login, hash) VALUES (?, ?)`, login, string(h))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJużIstnieje, err)
	}
	id, _ := res.LastInsertId()
	return &Użytkownik{ID: id, Login: login, Utworzony: time.Now()}, nil
}

func (s *Store) ZmieńHasło(ctx context.Context, userID int64, nowe string) error {
	if len(nowe) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(nowe), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET hash = ? WHERE id = ?`, string(h), userID)
	return err
}

// Sprawdź weryfikuje login i hasło. Przy złym loginie i tak liczy bcrypta,
// żeby czas odpowiedzi nie zdradzał, które konta istnieją.
func (s *Store) Sprawdź(ctx context.Context, login, hasło string) (*Użytkownik, error) {
	var u Użytkownik
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, login, hash, created_at FROM users WHERE login = ?`, login).
		Scan(&u.ID, &u.Login, &hash, &u.Utworzony)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(hasło))
		return nil, ErrZłeDane
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(hasło)) != nil {
		return nil, ErrZłeDane
	}
	return &u, nil
}

func (s *Store) Użytkownicy(ctx context.Context) ([]Użytkownik, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, login, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Użytkownik
	for rows.Next() {
		var u Użytkownik
		if err := rows.Scan(&u.ID, &u.Login, &u.Utworzony); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UsuńUżytkownika(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// ── sesje ────────────────────────────────────────────────────────────────────

func (s *Store) NowaSesja(ctx context.Context, userID int64) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	wygasa := time.Now().Add(CzasSesji)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`, skrótTokenu(token), userID, wygasa)
	return token, wygasa, err
}

func (s *Store) Sesja(ctx context.Context, token string) (*Użytkownik, error) {
	var u Użytkownik
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.login, u.created_at FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token = ? AND s.expires_at > CURRENT_TIMESTAMP`, skrótTokenu(token)).
		Scan(&u.ID, &u.Login, &u.Utworzony)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBrakUżytkow
	}
	return &u, err
}

func (s *Store) UsuńSesję(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, skrótTokenu(token))
	return err
}

// UsuńInneSesje kończy wszystkie sesje użytkownika poza wskazaną. Po zmianie
// hasła ktoś, kto miał skradzione ciasteczko, nie może dalej siedzieć w panelu.
func (s *Store) UsuńInneSesje(ctx context.Context, userID int64, zostaw string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND token <> ?`, userID, skrótTokenu(zostaw))
	return err
}

func (s *Store) SprzątnijSesje(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= CURRENT_TIMESTAMP`)
	return err
}

// ── historia sprawdzeń ───────────────────────────────────────────────────────
//
// Tego nie ma żaden lekki panel na rynku: stan sprzed godziny, nie tylko teraz.

func (s *Store) ZapiszWynik(ctx context.Context, w Wynik) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checks (key, state, code, ms, err, at) VALUES (?, ?, ?, ?, ?, ?)`,
		w.Klucz, w.Stan, w.Kod, w.MS, w.Błąd, w.Kiedy.UTC())
	return err
}

// PrzenieśKlucze przepisuje historię sprawdzeń pod nowe klucze (stary → nowy)
// w jednej transakcji: albo przenoszą się wszystkie wiersze, albo żaden.
//
// Klucz usługi to grupa+"/"+nazwa, więc zmiana nazwy i przeniesienie do innej
// grupy zmieniają klucz. Bez tego karta po przeciągnięciu traciła cały 24-godzinny
// pasek. Przyjmuje pary, a nie prefiks: nazwa grupy może zawierać „/", a pary
// z configu są jednoznaczne. Jeśli pod nowym kluczem już coś leży, wiersze się
// łączą — historia nie jest nigdy kasowana.
func (s *Store) PrzenieśKlucze(ctx context.Context, pary map[string]string) error {
	if len(pary) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for stary, nowy := range pary {
		if stary == nowy {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE checks SET key = ? WHERE key = ?`, nowy, stary); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Historia zwraca ostatnie n wyników dla usługi, od najstarszego.
func (s *Store) Historia(ctx context.Context, klucz string, n int) ([]Wynik, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, state, COALESCE(code,0), COALESCE(ms,0), COALESCE(err,''), at
		 FROM checks WHERE key = ? ORDER BY at DESC LIMIT ?`, klucz, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Wynik
	for rows.Next() {
		var w Wynik
		if err := rows.Scan(&w.Klucz, &w.Stan, &w.Kod, &w.MS, &w.Błąd, &w.Kiedy); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	// odwróć: chcemy chronologicznie
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Dostępność liczy procent czasu w stanie "up" w zadanym oknie.
func (s *Store) Dostępność(ctx context.Context, klucz string, okno time.Duration) (float64, int, error) {
	od := time.Now().Add(-okno).UTC()
	var wszystkie, ok int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), 0)
		 FROM checks WHERE key = ? AND at >= ?`, klucz, od).Scan(&wszystkie, &ok)
	if err != nil || wszystkie == 0 {
		return 0, 0, err
	}
	return float64(ok) / float64(wszystkie) * 100, wszystkie, nil
}

// Przytnij usuwa wpisy starsze niż podany okres — panel domowy nie potrzebuje
// historii sprzed pół roku, a plik ma nie puchnąć w nieskończoność.
func (s *Store) Przytnij(ctx context.Context, starszeNiż time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM checks WHERE at < ?`, time.Now().Add(-starszeNiż).UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}


// ── historia konfiguracji ────────────────────────────────────────────────────
//
// Każdy zapis z UI zostawia ślad: pełną treść pliku po zmianie i diff.
// Dzięki temu „cofnij" jest jednym kliknięciem, a nie szukaniem backupu.

type Wersja struct {
	ID     int64
	Kiedy  time.Time
	Autor  string
	Opis   string
	Treść  string
	Diff   string
}

// ZapiszWersję zapisuje stan pliku po zmianie i przycina historię do 50 wpisów.
func (s *Store) ZapiszWersję(ctx context.Context, autor, opis, treść, diff string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO config_versions (author, opis, tresc, diff) VALUES (?, ?, ?, ?)`,
		autor, opis, treść, diff); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM config_versions WHERE id NOT IN (
		   SELECT id FROM config_versions ORDER BY id DESC LIMIT 50)`)
	return err
}

// Wersje zwraca listę zmian bez treści plików — do wyświetlenia w UI.
func (s *Store) Wersje(ctx context.Context, limit int) ([]Wersja, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, author, opis, diff FROM config_versions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Wersja
	for rows.Next() {
		var w Wersja
		if err := rows.Scan(&w.ID, &w.Kiedy, &w.Autor, &w.Opis, &w.Diff); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Wersja zwraca pojedynczy wpis razem z treścią pliku.
func (s *Store) Wersja(ctx context.Context, id int64) (*Wersja, error) {
	var w Wersja
	err := s.db.QueryRowContext(ctx,
		`SELECT id, at, author, opis, tresc, diff FROM config_versions WHERE id = ?`, id).
		Scan(&w.ID, &w.Kiedy, &w.Autor, &w.Opis, &w.Treść, &w.Diff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("version not found")
	}
	return &w, err
}


// ── statystyki usługi ────────────────────────────────────────────────────────

type Statystyki struct {
	Próbek     int
	Dostępność float64 // procent czasu w stanie "up"
	P50        int64   // mediana czasu odpowiedzi w ms
	P95        int64
	Najgorszy  int64
}

// Staty liczy dostępność i czasy odpowiedzi w oknie. Percentyle liczone w SQL,
// bo przy kilku tysiącach wierszy nie ma po co przerzucać ich do Go.
func (s *Store) Staty(ctx context.Context, klucz string, okno time.Duration) (Statystyki, error) {
	od := time.Now().Add(-okno).UTC()
	var st Statystyki
	var udanych int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), 0),
       COALESCE(MAX(ms), 0)
FROM checks WHERE key = ? AND at >= ?`, klucz, od).Scan(&st.Próbek, &udanych, &st.Najgorszy)
	if err != nil || st.Próbek == 0 {
		return Statystyki{}, err
	}
	st.Dostępność = float64(udanych) / float64(st.Próbek) * 100

	// percentyle tylko z udanych sprawdzeń — czas odpowiedzi błędu nic nie mówi
	err = s.db.QueryRowContext(ctx, `
SELECT COALESCE((SELECT ms FROM checks WHERE key = ?1 AND at >= ?2 AND state = 'up'
                 ORDER BY ms LIMIT 1 OFFSET (SELECT COUNT(*)/2 FROM checks
                   WHERE key = ?1 AND at >= ?2 AND state = 'up')), 0),
       COALESCE((SELECT ms FROM checks WHERE key = ?1 AND at >= ?2 AND state = 'up'
                 ORDER BY ms LIMIT 1 OFFSET (SELECT COUNT(*)*95/100 FROM checks
                   WHERE key = ?1 AND at >= ?2 AND state = 'up')), 0)`,
		klucz, od).Scan(&st.P50, &st.P95)
	return st, err
}

type ZmianaStanu struct {
	Kiedy time.Time
	Z     string
	Na    string
	Błąd  string
	// Kod — status HTTP w momencie zmiany. Bez niego lista zmian pokazuje
	// tekst błędu albo nic, a „HTTP 502" mówi człowiekowi więcej.
	Kod int
}

// Zmiany zwraca momenty, w których usługa zmieniła stan — czyli to, co
// człowiek naprawdę chce zobaczyć zamiast tysiąca identycznych pomiarów.
func (s *Store) Zmiany(ctx context.Context, klucz string, limit int) ([]ZmianaStanu, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT at, poprzedni, state, COALESCE(err, ''), COALESCE(code, 0) FROM (
  SELECT at, state, err, code, LAG(state) OVER (ORDER BY at) AS poprzedni
  FROM checks WHERE key = ?
) WHERE poprzedni IS NOT NULL AND poprzedni <> state
ORDER BY at DESC LIMIT ?`, klucz, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ZmianaStanu
	for rows.Next() {
		var z ZmianaStanu
		if err := rows.Scan(&z.Kiedy, &z.Z, &z.Na, &z.Błąd, &z.Kod); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}
