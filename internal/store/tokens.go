package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// API tokens (SHYG-49): read-only, for Prometheus, Home Assistant and
// scripts. A token is `shyg_` + 32 random bytes in hex (256 bits), shown
// once on creation; the database holds the sha256 — exactly like session tokens, for the
// same reason (a leak of the .db file yields no working credentials) and for the
// same reason a fast hash is enough (bcrypt would make no sense here).

const (
	TokenPrefix  = "shyg_"
	MaxAPITokens = 50

	randomPartLength   = 64 // 32 bytes in hex
	maxTokenNameLength = 64
)

var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrTokenNotFound = errors.New("token not found")
	ErrTooManyTokens = errors.New("too many tokens")
	ErrTokenName     = errors.New("invalid token name")
)

// APIToken — an entry visible in the UI. The token itself is never available here.
type APIToken struct {
	ID         int64
	Name       string
	Hint       string // `shyg_` + the first 4 characters — to recognize the token in the list
	Author     string
	CreatedAt  time.Time
	LastUsedAt time.Time // zero = never
}

func validTokenShape(t string) bool {
	if !strings.HasPrefix(t, TokenPrefix) || len(t) != len(TokenPrefix)+randomPartLength {
		return false
	}
	for _, c := range t[len(TokenPrefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NewAPIToken creates a token and returns it ONCE (the second result is the entry without the secret).
func (s *Store) NewAPIToken(ctx context.Context, name, author string) (string, APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxTokenNameLength {
		return "", APIToken{}, ErrTokenName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' {
			return "", APIToken{}, ErrTokenName
		}
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens`).Scan(&count); err != nil {
		return "", APIToken{}, err
	}
	if count >= MaxAPITokens {
		return "", APIToken{}, ErrTooManyTokens
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", APIToken{}, err
	}
	token := TokenPrefix + hex.EncodeToString(b)
	hint := token[:len(TokenPrefix)+4]
	now := time.Now().UTC()
	// owner by the author's login; no account (e.g. "?") = token without an owner
	var owner sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE login = ?`, author).Scan(&owner); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return "", APIToken{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (name, hash, hint, author, user_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, tokenHash(token), hint, author, owner, now)
	if err != nil {
		return "", APIToken{}, err
	}
	id, _ := res.LastInsertId()
	return token, APIToken{ID: id, Name: name, Hint: hint, Author: author, CreatedAt: now}, nil
}

// VerifyAPIToken returns the token entry or ErrInvalidToken. The lookup goes by the
// hash of the given token, so the response time does not depend on how many
// leading characters match the real one (an attacker does not choose the hash).
func (s *Store) VerifyAPIToken(ctx context.Context, token string) (*APIToken, error) {
	if !validTokenShape(token) {
		return nil, ErrInvalidToken
	}
	hashed := tokenHash(token)
	var t APIToken
	var hash string
	var used sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, hint, author, created_at, last_used_at, hash FROM api_tokens WHERE hash = ?`, hashed).
		Scan(&t.ID, &t.Name, &t.Hint, &t.Author, &t.CreatedAt, &used, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	// belt and braces: equality already follows from the query, but the comparison should be constant-time
	if subtle.ConstantTimeCompare([]byte(hash), []byte(hashed)) != 1 {
		return nil, ErrInvalidToken
	}
	if used.Valid {
		t.LastUsedAt = used.Time
	}
	return &t, nil
}

// APITokens returns the list from the oldest.
func (s *Store) APITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hint, author, created_at, last_used_at FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var used sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Hint, &t.Author, &t.CreatedAt, &used); err != nil {
			return nil, err
		}
		if used.Valid {
			t.LastUsedAt = used.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RemoveAPIToken revokes a token — from this moment it gets 401.
func (s *Store) RemoveAPIToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// MarkTokenUsed saves last_used_at, but at most once a minute.
func (s *Store) MarkTokenUsed(ctx context.Context, id int64, now time.Time) error {
	var last sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT last_used_at FROM api_tokens WHERE id = ?`, id).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // revoked in the meantime
	}
	if err != nil {
		return err
	}
	if last.Valid && now.Sub(last.Time) < time.Minute {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.UTC(), id)
	return err
}

// ServiceAvailability — samples within a window; the ratio is computed by the caller.
type ServiceAvailability struct {
	Samples   int
	Successes int
}

// Availabilities computes the samples of all services in one query — for /metrics,
// so a scrape does not make N separate queries. Services with no samples in the window
// do not end up in the map.
func (s *Store) Availabilities(ctx context.Context, window time.Duration) (map[string]ServiceAvailability, error) {
	since := time.Now().Add(-window).UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, COUNT(*), COALESCE(SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), 0)
		 FROM checks WHERE at >= ? GROUP BY key`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ServiceAvailability{}
	for rows.Next() {
		var k string
		var d ServiceAvailability
		if err := rows.Scan(&k, &d.Samples, &d.Successes); err != nil {
			return nil, err
		}
		out[k] = d
	}
	return out, rows.Err()
}
