// Package gitrepo commits config changes if the file lives inside a repository.
//
// Why: the file is the source of truth, and git is the best history a file can
// have. If the user keeps the config in a repo, every change made in the UI
// should leave a commit — so `git log` shows who clicked what in the browser,
// and when.
//
// We deliberately use the system git instead of a library: go-git costs ~6 MB
// of binary (measured 2026-08-14) against a 25 MB budget, and the scenario
// concerns a minority of installations. A missing git or a missing repo is not
// an error — we simply do nothing.
package gitrepo

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrNoRepo = errors.New("file is not inside a git repository")

// Available tells whether we have anything to commit with.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// InRepo checks whether the file lies in a repository's working tree.
func InRepo(ctx context.Context, file string) bool {
	if !Available() {
		return false
	}
	dir := filepath.Dir(file)
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Commit adds the file and creates a commit. Nothing to commit — silence.
func Commit(ctx context.Context, file, message, author string) error {
	if !InRepo(ctx, file) {
		return ErrNoRepo
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	dir := filepath.Dir(file)
	name := filepath.Base(file)

	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "add", "--", name).CombinedOutput(); err != nil {
		return errors.New("git add: " + strings.TrimSpace(string(out)))
	}

	// nothing changed relative to the index → no commit and no error
	if err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--cached", "--quiet", "--", name).Run(); err == nil {
		return nil
	}

	if author == "" {
		author = "shyg"
	}
	// arguments passed separately, not through a shell — the file name and message
	// may contain spaces and quotes
	args := []string{
		"-C", dir,
		"-c", "user.name=Shy Garlic",
		"-c", "user.email=shyg@localhost",
		"commit",
		"-m", message,
		"--author", author + " <shyg@localhost>",
		"--only", "--", name,
	}
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return errors.New("git commit: " + strings.TrimSpace(string(out)))
	}
	return nil
}
