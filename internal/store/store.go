// Package store holds what cannot be kept in the YAML file:
// users, sessions and the check history.
//
// SQLite in pure Go (modernc.org/sqlite) — no cgo, so the binary stays
// static and the image is still a single file.
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserNotFound       = errors.New("user not found")
	ErrUserExists         = errors.New("user already exists")
	// ErrSetupDone: the first account already exists, so setup is closed.
	ErrSetupDone = errors.New("setup already done")
)

// SessionDuration — how long the login cookie lives.
const SessionDuration = 30 * 24 * time.Hour

type Store struct {
	db  *sql.DB
	now func() time.Time // replaced in tests
}

type User struct {
	ID        int64
	Login     string
	CreatedAt time.Time
}

type Result struct {
	Key   string
	State string
	Code  int
	MS    int64
	Err   string
	At    time.Time
}

func Open(path string) (*Store, error) {
	prepareFiles(path)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite dislikes concurrent writes; a single writer is entirely enough
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	tightenFiles(path)
	return s, nil
}

// inMemory says whether path is not a plain file.
func inMemory(path string) bool {
	return path == ":memory:" || path == "" || strings.HasPrefix(path, "file:")
}

// prepareFiles creates the data directory (0700) and the database file (0600)
// when they do not exist. SQLite gives the -wal and -shm files the mode of the
// main file, so creating it private makes the whole set private. The database
// holds password hashes, session hashes and the webhook address.
func prepareFiles(path string) {
	if inMemory(path) {
		return
	}
	if dir := filepath.Dir(path); dir != "." {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			_ = os.MkdirAll(dir, 0o700)
		}
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	}
}

// tightenFiles narrows files left over from older versions (created 0644) to
// 0600. Errors are ignored on purpose: a read-only mount or a file owned by
// someone else must not stop the panel.
func tightenFiles(path string) {
	if inMemory(path) {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if fi, err := os.Stat(path + suffix); err == nil && fi.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(path+suffix, 0o600)
		}
	}
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
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
CREATE TABLE IF NOT EXISTS api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT NOT NULL,
  hash         TEXT NOT NULL UNIQUE,
  hint         TEXT NOT NULL DEFAULT '',
  author       TEXT NOT NULL DEFAULT '',
  user_id      INTEGER,
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at DATETIME
);
`)
	if err != nil {
		return err
	}
	if err := s.backfillTokenUserIDs(); err != nil {
		return err
	}
	if err := s.hashSessions(); err != nil {
		return err
	}
	return s.normalizeSessionExpiry()
}

// backfillTokenUserIDs: API tokens remembered only the author's text login, so
// removing an account did not touch them. user_id ties a token to an account (without a
// foreign key — ALTER TABLE ADD COLUMN with REFERENCES is finicky in SQLite, and
// the removal is done by RemoveUser). Old rows get user_id by the author's login;
// those whose author is not in users stay without an owner.
// Idempotent.
func (s *Store) backfillTokenUserIDs() error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('api_tokens') WHERE name = 'user_id'`)
	if err != nil {
		return err
	}
	exists := rows.Next()
	rows.Close()
	if !exists {
		if _, err := s.db.Exec(`ALTER TABLE api_tokens ADD COLUMN user_id INTEGER`); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`UPDATE api_tokens SET user_id = (SELECT id FROM users WHERE login = api_tokens.author)
		WHERE user_id IS NULL`)
	return err
}

// tokenHash — the database holds the token's sha256, not the token itself: a leak of the
// .db file (backup, snapshot) then yields no working cookies. The token has 256 bits
// of randomness, so a fast hash is enough (bcrypt would make no sense here).
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// hashSessions rewrites plain tokens from old databases into hashes, so that after
// an upgrade nobody gets logged out. Idempotent: a hash is 64 characters
// (hex), a plain token is 43 (base64 of 32 bytes).
func (s *Store) hashSessions() error {
	rows, err := s.db.Query(`SELECT token FROM sessions WHERE length(token) <> 64`)
	if err != nil {
		return err
	}
	var plain []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		plain = append(plain, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range plain {
		if _, err := s.db.Exec(`UPDATE sessions SET token = ? WHERE token = ?`, tokenHash(t), t); err != nil {
			return err
		}
	}
	return nil
}

// sessionTimeFormat — how expires_at is stored: UTC, in the exact shape of SQLite's
// own CURRENT_TIMESTAMP ("2026-10-17 12:34:56"), so comparing the two as text is
// correct. The driver used to write time.Time with the local offset and zone name
// ("… +0200 CEST"); as text that sorts by local wall-clock digits, so in any zone
// east of UTC a session stayed valid for hours past its expiry.
const sessionTimeFormat = "2006-01-02 15:04:05"

var legacyTimeFormats = []string{
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999 -0700 -0700",
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05.999999999-07:00",
}

// parseLegacyTime reads an expires_at written by an older version.
func parseLegacyTime(v string) (time.Time, bool) {
	if i := strings.Index(v, " m="); i >= 0 { // monotonic clock reading from time.Time.String()
		v = v[:i]
	}
	for _, layout := range legacyTimeFormats {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// normalizeSessionExpiry rewrites expires_at values from older databases into
// sessionTimeFormat (UTC). Idempotent: rows already in the new shape are 19
// characters long and are left alone. A value that cannot be read is dropped
// together with its session — the user signs in again, which is safer than
// guessing.
func (s *Store) normalizeSessionExpiry() error {
	rows, err := s.db.Query(`SELECT token, CAST(expires_at AS TEXT) FROM sessions WHERE length(CAST(expires_at AS TEXT)) <> ?`, len(sessionTimeFormat))
	if err != nil {
		return err
	}
	type legacy struct{ token, expires string }
	var old []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.token, &l.expires); err != nil {
			rows.Close()
			return err
		}
		old = append(old, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range old {
		if t, ok := parseLegacyTime(l.expires); ok {
			_, err = s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`, t.UTC().Format(sessionTimeFormat), l.token)
		} else {
			_, err = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, l.token)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ── instance settings ───────────────────────────────────────────────────────
//
// Key-value for things that must NOT end up in the configuration file,
// because that one is shown in the UI and committed to git. Today it holds one thing:
// the notification webhook address, i.e. a credential.

// Setting returns the value, or an empty string when the key does not exist. A missing key
// is not an error — the caller would have to tell it apart from an empty value
// anyway, and there is no point.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetSetting saves the value. An empty one deletes the row.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value, at) VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, at = CURRENT_TIMESTAMP`, key, value)
	return err
}

// ── users ───────────────────────────────────────────────────────────────────

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) AddUser(ctx context.Context, login, password string) (*User, error) {
	if len(login) < 2 {
		return nil, errors.New("login too short")
	}
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (login, hash) VALUES (?, ?)`, login, string(h))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUserExists, err)
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Login: login, CreatedAt: time.Now()}, nil
}

// AddFirstUser creates the first account, and only if there is none: the check
// and the insert are ONE statement, so concurrent setup requests cannot all
// succeed (eight parallel POSTs used to create eight administrators).
// Returns ErrSetupDone when an account already exists.
func (s *Store) AddFirstUser(ctx context.Context, login, password string) (*User, error) {
	if len(login) < 2 {
		return nil, errors.New("login too short")
	}
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (login, hash) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, login, string(h))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUserExists, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrSetupDone
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Login: login, CreatedAt: s.now()}, nil
}

func (s *Store) ChangePassword(ctx context.Context, userID int64, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET hash = ? WHERE id = ?`, string(h), userID)
	return err
}

// Authenticate verifies login and password. On a wrong login it still computes bcrypt,
// so the response time does not reveal which accounts exist.
func (s *Store) Authenticate(ctx context.Context, login, password string) (*User, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, login, hash, created_at FROM users WHERE login = ?`, login).
		Scan(&u.ID, &u.Login, &hash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, login, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Login, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// RemoveUser deletes the account together with its sessions (cascade) and API tokens —
// a token issued by a removed user has no right to outlive their account.
func (s *Store) RemoveUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ── sessions ────────────────────────────────────────────────────────────────

func (s *Store) NewSession(ctx context.Context, userID int64) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expiresAt := s.now().Add(SessionDuration)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash(token), userID, expiresAt.UTC().Format(sessionTimeFormat))
	return token, expiresAt, err
}

func (s *Store) Session(ctx context.Context, token string) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.login, u.created_at FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token = ? AND s.expires_at > CURRENT_TIMESTAMP`, tokenHash(token)).
		Scan(&u.ID, &u.Login, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return &u, err
}

func (s *Store) RemoveSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, tokenHash(token))
	return err
}

// RemoveOtherSessions ends all of a user's sessions except the given one. After a password
// change, someone who had a stolen cookie cannot keep sitting in the panel.
func (s *Store) RemoveOtherSessions(ctx context.Context, userID int64, keep string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND token <> ?`, userID, tokenHash(keep))
	return err
}

func (s *Store) CleanupSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= CURRENT_TIMESTAMP`)
	return err
}

// ── check history ───────────────────────────────────────────────────────────
//
// No lightweight panel on the market has this: the state from an hour ago, not just now.

func (s *Store) SaveResult(ctx context.Context, w Result) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checks (key, state, code, ms, err, at) VALUES (?, ?, ?, ?, ?, ?)`,
		w.Key, w.State, w.Code, w.MS, w.Err, w.At.UTC())
	return err
}

// MoveKeys rewrites the check history under new keys (old → new)
// in a single transaction: either all rows move, or none.
//
// A service key is group+"/"+name, so renaming and moving to another
// group change the key. Without this, a card lost its whole 24-hour bar after being
// dragged. It takes pairs, not a prefix: a group name may contain "/", and the pairs
// from the config are unambiguous. If something already lies under the new key, the rows
// merge — history is never deleted.
func (s *Store) MoveKeys(ctx context.Context, pairs map[string]string) error {
	if len(pairs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for oldKey, newKey := range pairs {
		if oldKey == newKey {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE checks SET key = ? WHERE key = ?`, newKey, oldKey); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// History returns the last n results for a service, from the oldest.
func (s *Store) History(ctx context.Context, key string, n int) ([]Result, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, state, COALESCE(code,0), COALESCE(ms,0), COALESCE(err,''), at
		 FROM checks WHERE key = ? ORDER BY at DESC LIMIT ?`, key, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var w Result
		if err := rows.Scan(&w.Key, &w.State, &w.Code, &w.MS, &w.Err, &w.At); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	// reverse: we want chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Availability computes the percentage of time in the "up" state within the given window.
func (s *Store) Availability(ctx context.Context, key string, window time.Duration) (float64, int, error) {
	since := time.Now().Add(-window).UTC()
	var all, ok int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), 0)
		 FROM checks WHERE key = ? AND at >= ?`, key, since).Scan(&all, &ok)
	if err != nil || all == 0 {
		return 0, 0, err
	}
	return float64(ok) / float64(all) * 100, all, nil
}

// Prune removes entries older than the given period — a home panel does not need
// history from half a year ago, and the file should not grow forever.
func (s *Store) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM checks WHERE at < ?`, time.Now().Add(-olderThan).UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── config history ──────────────────────────────────────────────────────────
//
// Every save from the UI leaves a trace: the full file content after the change, and a diff.
// Thanks to that "undo" is one click, not a hunt for a backup.

type Version struct {
	ID          int64
	At          time.Time
	Author      string
	Description string
	Content     string
	Diff        string
}

// SaveVersion saves the file state after a change and trims the history to 50 entries.
func (s *Store) SaveVersion(ctx context.Context, author, description, content, diff string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO config_versions (author, opis, tresc, diff) VALUES (?, ?, ?, ?)`,
		author, description, content, diff); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM config_versions WHERE id NOT IN (
		   SELECT id FROM config_versions ORDER BY id DESC LIMIT 50)`)
	return err
}

// Versions returns the list of changes without file contents — for display in the UI.
func (s *Store) Versions(ctx context.Context, limit int) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, author, opis, diff FROM config_versions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var w Version
		if err := rows.Scan(&w.ID, &w.At, &w.Author, &w.Description, &w.Diff); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Version returns a single entry together with the file content.
func (s *Store) Version(ctx context.Context, id int64) (*Version, error) {
	var w Version
	err := s.db.QueryRowContext(ctx,
		`SELECT id, at, author, opis, tresc, diff FROM config_versions WHERE id = ?`, id).
		Scan(&w.ID, &w.At, &w.Author, &w.Description, &w.Content, &w.Diff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("version not found")
	}
	return &w, err
}

// ── service statistics ──────────────────────────────────────────────────────

type Statistics struct {
	Samples      int
	Availability float64 // percentage of time in the "up" state
	P50          int64   // median response time in ms
	P95          int64
	Worst        int64
}

// Stats computes availability and response times within a window. Percentiles are computed in SQL,
// because with a few thousand rows there is no point shipping them to Go.
func (s *Store) Stats(ctx context.Context, key string, window time.Duration) (Statistics, error) {
	since := time.Now().Add(-window).UTC()
	var st Statistics
	var successes int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), 0),
       COALESCE(MAX(ms), 0)
FROM checks WHERE key = ? AND at >= ?`, key, since).Scan(&st.Samples, &successes, &st.Worst)
	if err != nil || st.Samples == 0 {
		return Statistics{}, err
	}
	st.Availability = float64(successes) / float64(st.Samples) * 100

	// percentiles only from successful checks — an error's response time says nothing
	err = s.db.QueryRowContext(ctx, `
SELECT COALESCE((SELECT ms FROM checks WHERE key = ?1 AND at >= ?2 AND state = 'up'
                 ORDER BY ms LIMIT 1 OFFSET (SELECT COUNT(*)/2 FROM checks
                   WHERE key = ?1 AND at >= ?2 AND state = 'up')), 0),
       COALESCE((SELECT ms FROM checks WHERE key = ?1 AND at >= ?2 AND state = 'up'
                 ORDER BY ms LIMIT 1 OFFSET (SELECT COUNT(*)*95/100 FROM checks
                   WHERE key = ?1 AND at >= ?2 AND state = 'up')), 0)`,
		key, since).Scan(&st.P50, &st.P95)
	return st, err
}

type StateChange struct {
	At   time.Time
	From string
	To   string
	Err  string
	// Code — the HTTP status at the moment of the change. Without it the change list shows
	// the error text or nothing, and "HTTP 502" tells a human more.
	Code int
}

// Changes returns the moments when a service changed state — which is what a
// human actually wants to see instead of a thousand identical measurements.
func (s *Store) Changes(ctx context.Context, key string, limit int) ([]StateChange, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT at, poprzedni, state, COALESCE(err, ''), COALESCE(code, 0) FROM (
  SELECT at, state, err, code, LAG(state) OVER (ORDER BY at) AS poprzedni
  FROM checks WHERE key = ?
) WHERE poprzedni IS NOT NULL AND poprzedni <> state
ORDER BY at DESC LIMIT ?`, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateChange
	for rows.Next() {
		var z StateChange
		if err := rows.Scan(&z.At, &z.From, &z.To, &z.Err, &z.Code); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}
