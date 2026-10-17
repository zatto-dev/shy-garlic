package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the zone tests must not depend on the host's zoneinfo
)

// Eight parallel setup requests used to create eight administrators: the handler
// counted users and then inserted, in two statements.
func TestAddFirstUserIsAtomic(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.AddFirstUser(ctx, fmt.Sprintf("admin%d", i), "password-123")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, closed := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrSetupDone), errors.Is(err, ErrUserExists):
			closed++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || closed != 7 {
		t.Fatalf("created %d accounts, refused %d; want 1 and 7", ok, closed)
	}
	if n, _ := st.UserCount(ctx); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	if _, err := st.AddFirstUser(ctx, "late", "password-123"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("late setup = %v, want ErrSetupDone", err)
	}
}

func inZone(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
}

// expires_at was compared as text against CURRENT_TIMESTAMP (UTC) while the
// driver stored local wall-clock time with an offset suffix. East of UTC a session
// that expired an hour ago still passed.
func TestExpiredSessionIsRejectedInAnyTimezone(t *testing.T) {
	for _, zone := range []string{"Europe/Warsaw", "Asia/Tokyo", "America/Los_Angeles", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			inZone(t, zone)
			st := openTest(t)
			ctx := context.Background()
			u, err := st.AddUser(ctx, "owner", "password-123")
			if err != nil {
				t.Fatal(err)
			}
			st.now = func() time.Time { return time.Now().Add(-SessionDuration - time.Hour) } // expired 1 h ago
			expired, _, err := st.NewSession(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			st.now = func() time.Time { return time.Now().Add(-SessionDuration + time.Hour) } // 1 h left
			alive, _, err := st.NewSession(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Session(ctx, expired); err == nil {
				t.Error("a session that expired an hour ago was accepted")
			}
			if _, err := st.Session(ctx, alive); err != nil {
				t.Errorf("a session with an hour left was rejected: %v", err)
			}
			if err := st.CleanupSessions(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Session(ctx, alive); err != nil {
				t.Errorf("cleanup removed a live session: %v", err)
			}
			var n int
			_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
			if n != 1 {
				t.Errorf("sessions after cleanup = %d, want 1", n)
			}
		})
	}
}

// Rows written by the old code ("… +0200 CEST") are rewritten on open, so the
// comparison is right for them too.
func TestLegacySessionExpiryIsMigrated(t *testing.T) {
	inZone(t, "Europe/Warsaw")
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, _ := st.AddUser(ctx, "owner", "password-123")
	legacy := func(d time.Duration) string {
		return time.Now().Add(d).In(time.Local).Format("2006-01-02 15:04:05.999999999 -0700 MST") + " m=+12.3"
	}
	put := func(plain string, expires string) {
		if _, err := st.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash(plain), u.ID, expires); err != nil {
			t.Fatal(err)
		}
	}
	put("expired-token", legacy(-time.Hour))
	put("alive-token", legacy(time.Hour))
	put("garbage-token", "not a time")
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Session(ctx, "expired-token"); err == nil {
		t.Error("legacy session that expired an hour ago was accepted")
	}
	if _, err := st.Session(ctx, "alive-token"); err != nil {
		t.Errorf("legacy session with an hour left was rejected: %v", err)
	}
	if _, err := st.Session(ctx, "garbage-token"); err == nil {
		t.Error("session with an unreadable expiry was accepted")
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE length(CAST(expires_at AS TEXT)) <> 19`).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions left in a non-canonical format", n)
	}
}

func TestDatabaseFilesArePrivate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data", "shyg.db") // the directory does not exist yet
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(context.Background(), "owner", "password-123"); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	check := func(p string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", p, got, want)
		}
	}
	check(filepath.Dir(path), 0o700)
	check(path, 0o600)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			check(path+suffix, 0o600)
		}
	}
}

func TestOpenTightensExistingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			_ = os.Chmod(p, 0o644)
		}
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", p, fi.Mode().Perm())
		}
	}
}

// Only a real "already there" may be reported as ErrUserExists / ErrSetupDone.
func TestAddFirstUserReportsDatabaseErrors(t *testing.T) {
	st := openTest(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := st.AddFirstUser(context.Background(), "owner", "password-123")
	if err == nil {
		t.Fatal("a closed database reported success")
	}
	if errors.Is(err, ErrUserExists) || errors.Is(err, ErrSetupDone) {
		t.Fatalf("a database failure was reported as %v", err)
	}

	// the genuine duplicate still is ErrUserExists / ErrSetupDone
	st2 := openTest(t)
	ctx := context.Background()
	if _, err := st2.AddFirstUser(ctx, "owner", "password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.AddFirstUser(ctx, "owner", "password-123"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("second setup = %v, want ErrSetupDone", err)
	}
}
