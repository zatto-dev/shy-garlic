// Package gitrepo commituje zmiany configu, jeśli plik leży w repozytorium.
//
// Po co: plik jest źródłem prawdy, a najlepszą historią pliku jest git. Jeśli
// użytkownik trzyma config w repo, każda zmiana z UI ma zostawić commit — dzięki
// temu `git log` pokazuje, kto i kiedy klikał w przeglądarce.
//
// Celowo używamy systemowego gita zamiast biblioteki: go-git kosztuje ~6 MB
// binarki (zmierzone 2026-08-14) przy budżecie 25 MB, a scenariusz dotyczy
// mniejszości instalacji. Brak gita albo brak repo nie jest błędem — po prostu
// nic nie robimy.
package gitrepo

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrBrakRepo = errors.New("file is not inside a git repository")

// Dostępny mówi, czy w ogóle mamy czym commitować.
func Dostępny() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// WRepo sprawdza, czy plik leży w drzewie roboczym repozytorium.
func WRepo(ctx context.Context, plik string) bool {
	if !Dostępny() {
		return false
	}
	kat := filepath.Dir(plik)
	cmd := exec.CommandContext(ctx, "git", "-C", kat, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Commit dodaje plik i tworzy commit. Gdy nie ma czego commitować — cisza.
func Commit(ctx context.Context, plik, wiadomość, autor string) error {
	if !WRepo(ctx, plik) {
		return ErrBrakRepo
	}
	ctx, anuluj := context.WithTimeout(ctx, 15*time.Second)
	defer anuluj()

	kat := filepath.Dir(plik)
	nazwa := filepath.Base(plik)

	if out, err := exec.CommandContext(ctx, "git", "-C", kat, "add", "--", nazwa).CombinedOutput(); err != nil {
		return errors.New("git add: " + strings.TrimSpace(string(out)))
	}

	// nic się nie zmieniło względem indeksu → nie ma commita i nie ma błędu
	if err := exec.CommandContext(ctx, "git", "-C", kat, "diff", "--cached", "--quiet", "--", nazwa).Run(); err == nil {
		return nil
	}

	if autor == "" {
		autor = "shyg"
	}
	// argumenty osobno, nie przez powłokę — nazwa pliku i wiadomość mogą
	// zawierać spacje i cudzysłowy
	args := []string{
		"-C", kat,
		"-c", "user.name=Shy Garlic",
		"-c", "user.email=shyg@localhost",
		"commit",
		"-m", wiadomość,
		"--author", autor + " <shyg@localhost>",
		"--only", "--", nazwa,
	}
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return errors.New("git commit: " + strings.TrimSpace(string(out)))
	}
	return nil
}
