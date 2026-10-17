package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/store"
)

// SHYG-41: a service key is group/name. A move or a rename must not lose
// history — the card should have its 24-hour bar right away.

func seedHistory(t *testing.T, s *Server, key string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := s.st.SaveResult(context.Background(), store.Result{
			Key: key, State: "up", Code: 200, MS: 3,
			At: time.Now().Add(-time.Duration(n-i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// servicePoints calls /api/service for the key and returns the number of history points.
func servicePoints(t *testing.T, h http.Handler, c *http.Cookie, key string) int {
	t.Helper()
	w := send(h, c, "GET", "/api/service?key="+url.QueryEscape(key), "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/service %q: %d %s", key, w.Code, w.Body)
	}
	var reply struct {
		Points []json.RawMessage `json:"points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return len(reply.Points)
}

func TestMoveBetweenGroupsKeepsHistory(t *testing.T) {
	s, h, c := orderServer(t)
	seedHistory(t, s, "Media/Sonarr", 4)

	w := send(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","toGroup":"Sieć","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if n := servicePoints(t, h, c, "Sieć/Sonarr"); n != 4 {
		t.Errorf("%d points under the new key, want 4", n)
	}
	if hist, _ := s.st.History(context.Background(), "Media/Sonarr", 10); len(hist) != 0 {
		t.Errorf("the old key kept %d rows", len(hist))
	}
}

func TestRenameAndMoveInPatchKeepsHistory(t *testing.T) {
	s, h, c := orderServer(t)
	seedHistory(t, s, "Media/Sonarr", 3)

	w := send(h, c, "PATCH", "/api/services", `{"group":"Media","name":"Sonarr","newName":"Sonarr 4K"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	if n := servicePoints(t, h, c, "Media/Sonarr 4K"); n != 3 {
		t.Errorf("%d points after rename, want 3", n)
	}

	// move + rename in one request
	w = send(h, c, "PATCH", "/api/services",
		`{"group":"Media","name":"Sonarr 4K","newGroup":"Sieć","newName":"Sonarr"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move+rename: %d %s", w.Code, w.Body)
	}
	if n := servicePoints(t, h, c, "Sieć/Sonarr"); n != 3 {
		t.Errorf("%d points after move+rename, want 3", n)
	}
}

func TestGroupRenameKeepsHistoryOfAllServices(t *testing.T) {
	s, h, c := orderServer(t)
	seedHistory(t, s, "Media/Jellyfin", 2)
	seedHistory(t, s, "Media/Sonarr", 5)
	seedHistory(t, s, "Sieć/Pi-hole", 1)

	w := send(h, c, "PATCH", "/api/groups", `{"name":"Media","newName":"Filmy"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("group rename: %d %s", w.Code, w.Body)
	}
	for key, want := range map[string]int{"Filmy/Jellyfin": 2, "Filmy/Sonarr": 5, "Sieć/Pi-hole": 1} {
		if n := servicePoints(t, h, c, key); n != want {
			t.Errorf("%s: %d points, want %d", key, n, want)
		}
	}
}

// ?dry=1 saves nothing, so it moves nothing.
func TestDryRunDoesNotTouchHistory(t *testing.T) {
	s, h, c := orderServer(t)
	seedHistory(t, s, "Media/Sonarr", 2)
	w := send(h, c, "POST", "/api/services/move?dry=1", `{"group":"Media","name":"Sonarr","toGroup":"Sieć","index":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("dry: %d %s", w.Code, w.Body)
	}
	if hist, _ := s.st.History(context.Background(), "Media/Sonarr", 10); len(hist) != 2 {
		t.Errorf("dry-run moved the history: %d rows", len(hist))
	}
}

// The notifier state and "last saved" travel together with the key — otherwise after
// a drag the same result would reach the DB a second time.
func TestMigrationRewiresNotifierAndSaveState(t *testing.T) {
	s, _, _ := orderServer(t)
	at := time.Now()
	s.lastMu.Lock()
	s.last["Media/Sonarr"] = at
	s.lastMu.Unlock()

	s.migrateKeys(context.Background(), map[string]string{"Media/Sonarr": "Sieć/Sonarr"})

	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	if !s.last["Sieć/Sonarr"].Equal(at) {
		t.Errorf("last[new] = %v, want %v", s.last["Sieć/Sonarr"], at)
	}
	if _, has := s.last["Media/Sonarr"]; has {
		t.Error("the old key stayed in last")
	}
}

// ── SHYG-44: serializing reloads ─────────────────────────────────────────────

// watch() must not reload the config in the middle of a save from the panel: the
// stamp is set only in reload(), and between Save and reload there are still the history and git.
func TestWatchWaitsForPanelSave(t *testing.T) {
	s, _, _ := orderServer(t)
	s.edit.Lock() // "a save from the panel is in progress"
	if err := os.WriteFile(s.path, []byte("groups: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.checkFileChange(s.ctx)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("watch reloaded the config although a panel save is in progress")
	case <-time.After(150 * time.Millisecond):
	}
	s.edit.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not resume after the save finished")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cfg.Groups) != 0 {
		t.Errorf("the change from disk was not loaded: %d groups", len(s.cfg.Groups))
	}
}

// An external change and saves from the panel at once; under -race nothing may be
// detected, and the final state must be consistent with the file.
func TestReloadConcurrentWithEdit(t *testing.T) {
	s, h, c := orderServer(t)
	var by sync.WaitGroup
	by.Add(2)
	go func() {
		defer by.Done()
		for i := 0; i < 15; i++ {
			s.checkFileChange(s.ctx)
			_ = s.reload(s.ctx)
		}
	}()
	go func() {
		defer by.Done()
		for i := 0; i < 15; i++ {
			send(h, c, "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","toGroup":"Sieć","index":0}`)
			send(h, c, "POST", "/api/services/move", `{"group":"Sieć","name":"Sonarr","toGroup":"Media","index":0}`)
		}
	}()
	by.Wait()
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if got := s.stamp; got != s.stampOf() {
		t.Errorf("stamp %q ≠ file %q", got, s.stampOf())
	}
}
