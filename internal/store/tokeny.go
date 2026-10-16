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

// Tokeny API (SHYG-49): tylko do odczytu, dla Prometheusa, Home Assistanta i
// skryptów. Token to `shyg_` + 32 losowe bajty w hex (256 bitów), pokazywany
// raz przy tworzeniu; w bazie leży sha256 — dokładnie jak tokeny sesji, z tego
// samego powodu (wyciek pliku .db nie daje działających poświadczeń) i z tego
// samego powodu szybki skrót wystarcza (bcrypt byłby tu bez sensu).

const (
	PrefiksTokenu  = "shyg_"
	MaksTokenówAPI = 50

	długośćLosowejCzęści = 64 // 32 bajty w hex
	maksNazwyTokenu      = 64
)

var (
	ErrZłyToken      = errors.New("invalid token")
	ErrBrakTokenu    = errors.New("token not found")
	ErrZaDużoTokenów = errors.New("too many tokens")
	ErrNazwaTokenu   = errors.New("invalid token name")
)

// TokenAPI — wpis widoczny w UI. Sam token nie jest tu nigdy dostępny.
type TokenAPI struct {
	ID             int64
	Nazwa          string
	Wskazówka      string // `shyg_` + 4 pierwsze znaki — do rozpoznania tokenu na liście
	Autor          string
	Utworzony      time.Time
	OstatnieUżycie time.Time // zero = nigdy
}

func poprawnyKształtTokenu(t string) bool {
	if !strings.HasPrefix(t, PrefiksTokenu) || len(t) != len(PrefiksTokenu)+długośćLosowejCzęści {
		return false
	}
	for _, c := range t[len(PrefiksTokenu):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NowyTokenAPI tworzy token i zwraca go JEDEN raz (drugi wynik to wpis bez sekretu).
func (s *Store) NowyTokenAPI(ctx context.Context, nazwa, autor string) (string, TokenAPI, error) {
	nazwa = strings.TrimSpace(nazwa)
	if nazwa == "" || len([]rune(nazwa)) > maksNazwyTokenu {
		return "", TokenAPI{}, ErrNazwaTokenu
	}
	for _, r := range nazwa {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' {
			return "", TokenAPI{}, ErrNazwaTokenu
		}
	}
	var ile int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens`).Scan(&ile); err != nil {
		return "", TokenAPI{}, err
	}
	if ile >= MaksTokenówAPI {
		return "", TokenAPI{}, ErrZaDużoTokenów
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", TokenAPI{}, err
	}
	token := PrefiksTokenu + hex.EncodeToString(b)
	wskazówka := token[:len(PrefiksTokenu)+4]
	teraz := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (name, hash, hint, author, created_at) VALUES (?, ?, ?, ?, ?)`,
		nazwa, skrótTokenu(token), wskazówka, autor, teraz)
	if err != nil {
		return "", TokenAPI{}, err
	}
	id, _ := res.LastInsertId()
	return token, TokenAPI{ID: id, Nazwa: nazwa, Wskazówka: wskazówka, Autor: autor, Utworzony: teraz}, nil
}

// SprawdźTokenAPI zwraca wpis tokenu albo ErrZłyToken. Wyszukiwanie idzie po
// skrócie podanego tokenu, więc czas odpowiedzi nie zależy od tego, ile
// początkowych znaków zgadza się z prawdziwym (atakujący nie wybierze skrótu).
func (s *Store) SprawdźTokenAPI(ctx context.Context, token string) (*TokenAPI, error) {
	if !poprawnyKształtTokenu(token) {
		return nil, ErrZłyToken
	}
	skrót := skrótTokenu(token)
	var t TokenAPI
	var hash string
	var użyty sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, hint, author, created_at, last_used_at, hash FROM api_tokens WHERE hash = ?`, skrót).
		Scan(&t.ID, &t.Nazwa, &t.Wskazówka, &t.Autor, &t.Utworzony, &użyty, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrZłyToken
	}
	if err != nil {
		return nil, err
	}
	// pas i szelki: równość już wynika z zapytania, ale porównanie ma być stałoczasowe
	if subtle.ConstantTimeCompare([]byte(hash), []byte(skrót)) != 1 {
		return nil, ErrZłyToken
	}
	if użyty.Valid {
		t.OstatnieUżycie = użyty.Time
	}
	return &t, nil
}

// TokenyAPI zwraca listę od najstarszego.
func (s *Store) TokenyAPI(ctx context.Context) ([]TokenAPI, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hint, author, created_at, last_used_at FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenAPI
	for rows.Next() {
		var t TokenAPI
		var użyty sql.NullTime
		if err := rows.Scan(&t.ID, &t.Nazwa, &t.Wskazówka, &t.Autor, &t.Utworzony, &użyty); err != nil {
			return nil, err
		}
		if użyty.Valid {
			t.OstatnieUżycie = użyty.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UsuńTokenAPI odwołuje token — od tej chwili dostaje 401.
func (s *Store) UsuńTokenAPI(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBrakTokenu
	}
	return nil
}

// OznaczUżycieTokenu zapisuje last_used_at, ale najwyżej raz na minutę.
func (s *Store) OznaczUżycieTokenu(ctx context.Context, id int64, teraz time.Time) error {
	var ostatnie sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT last_used_at FROM api_tokens WHERE id = ?`, id).Scan(&ostatnie)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // odwołany w międzyczasie
	}
	if err != nil {
		return err
	}
	if ostatnie.Valid && teraz.Sub(ostatnie.Time) < time.Minute {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, teraz.UTC(), id)
	return err
}

// DostępnośćUsługi — próbki w oknie; stosunek liczy wołający.
type DostępnośćUsługi struct {
	Próbek  int
	Udanych int
}

// Dostępności liczy próbki wszystkich usług jednym zapytaniem — do /metrics,
// żeby scrape nie robił N osobnych zapytań. Usługi bez próbek w oknie nie
// trafiają do mapy.
func (s *Store) Dostępności(ctx context.Context, okno time.Duration) (map[string]DostępnośćUsługi, error) {
	od := time.Now().Add(-okno).UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, COUNT(*), COALESCE(SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), 0)
		 FROM checks WHERE at >= ? GROUP BY key`, od)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DostępnośćUsługi{}
	for rows.Next() {
		var k string
		var d DostępnośćUsługi
		if err := rows.Scan(&k, &d.Próbek, &d.Udanych); err != nil {
			return nil, err
		}
		out[k] = d
	}
	return out, rows.Err()
}
