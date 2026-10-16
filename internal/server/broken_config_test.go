package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/store"
)

// SHYG-45: a broken config must not kill the panel.

const goodConfig = "title: Dobry\ngroups:\n  - name: Media\n    services:\n      - name: Sonarr\n"
const brokenConfig = "title: Zepsuty\ngroups:\n\t- name: Media\n"

// serverOnContent sets the panel up on the given file content WITHOUT loading it.
func serverOnContent(t *testing.T, content string) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "shygarlic.yaml")
	if content != "" {
		if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(dir, "shygarlic.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, st, Options{BackgroundDir: filepath.Join(dir, "bg")})
	if err != nil {
		t.Fatal(err)
	}
	s.ctx = context.Background()
	t.Cleanup(func() { s.Close(); _ = st.Close() })
	return s
}

var testCookies sync.Map // *Server → *http.Cookie

// session creates an account only once per server — loggedInCookie is called once.
func session(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	if c, ok := testCookies.Load(s); ok {
		return c.(*http.Cookie)
	}
	c := loggedInCookie(t, s)
	testCookies.Store(s, c)
	return c
}

func homePage(t *testing.T, s *Server, headers ...string) string {
	t.Helper()
	c := session(t, s)
	w := send(s.Handler(), c, "GET", "/", "", headers...)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / → %d: %.200s", w.Code, w.Body)
	}
	return w.Body.String()
}

func TestStartWithBrokenConfigDoesNotDie(t *testing.T) {
	s := serverOnContent(t, brokenConfig)
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatalf("start with a broken file must succeed (crash loop in Docker): %v", err)
	}
	html := homePage(t, s)
	if !strings.Contains(html, `id="baner-config"`) || !strings.Contains(html, `data-stan="error"`) {
		t.Errorf("no error banner:\n%.600s", html)
	}
	// error line number (a tab on line 3)
	if !strings.Contains(html, "Line 3") {
		t.Errorf("the banner has no line number")
	}
	if strings.Contains(html, "Zepsuty") {
		t.Error("the panel shows the content of the broken file")
	}
	if !strings.Contains(html, "nothing to show") {
		t.Error("the banner does not say there is nothing to show")
	}
}

func TestStartWithBrokenConfigTakesLastGoodFromHistory(t *testing.T) {
	s := serverOnContent(t, brokenConfig)
	ctx := context.Background()
	if err := s.st.SaveVersion(ctx, "x", "stara", "title: Stary\ngroups:\n  - name: Archiwum\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveVersion(ctx, "x", "uszkodzona", brokenConfig, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveVersion(ctx, "x", "dobra", goodConfig, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveVersion(ctx, "x", "też uszkodzona", brokenConfig, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.loadAfterStart(ctx); err != nil {
		t.Fatal(err)
	}
	html := homePage(t, s)
	if !strings.Contains(html, "Dobry") || !strings.Contains(html, "Sonarr") {
		t.Errorf("the newest WORKING version from history was not taken:\n%.800s", html)
	}
	if !strings.Contains(html, "saved in history") {
		t.Error("the banner does not say it is a version from history")
	}
	// the file on disk untouched — we do not overwrite what a human is editing right now
	if b, _ := os.ReadFile(s.path); string(b) != brokenConfig {
		t.Errorf("the panel changed the file on disk: %q", b)
	}
}

func TestStartWithoutFileAlsoDoesNotDie(t *testing.T) {
	s := serverOnContent(t, "")
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatalf("missing file = banner, not exit: %v", err)
	}
	if html := homePage(t, s); !strings.Contains(html, `data-stan="error"`) {
		t.Error("no banner for a missing file")
	}
}

// Breaking the file during operation: the panel stays on the previous configuration,
// logs the error ONCE, does not spin every 2 s and cleans up the banner after the fix.
func TestBrokenFileDuringRunLogsOnceAndClearsBanner(t *testing.T) {
	var log bytes.Buffer
	var mu sync.Mutex
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{&log, &mu}, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	s := serverOnContent(t, goodConfig+"      - name: Radarr\n        chek: 1\n")
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if html := homePage(t, s); strings.Contains(html, `data-stan="error"`) {
		t.Fatal("banner for a good file")
	}
	if b := s.instanceBanner("en"); len(b.Ostrzeżenia) != 1 {
		t.Fatalf("setup: want 1 warning, have %v", b.Ostrzeżenia)
	}

	breakFile := func(content string) {
		if err := os.WriteFile(s.path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	breakFile(brokenConfig)
	for i := 0; i < 5; i++ { // five watch() "ticks" on the same error
		s.checkFileChange(s.ctx)
	}
	mu.Lock()
	n := strings.Count(log.String(), "config invalid")
	mu.Unlock()
	if n != 1 {
		t.Fatalf("error logged %d times, want 1", n)
	}
	html := homePage(t, s)
	if !strings.Contains(html, `data-stan="error"`) || !strings.Contains(html, "Dobry") ||
		!strings.Contains(html, "last working configuration") {
		t.Errorf("after breaking: the panel should stay on the previous configuration with a banner:\n%.800s", html)
	}

	// warnings from the previous version of the file do not hang next to the new error
	if b := s.instanceBanner("en"); len(b.Ostrzeżenia) != 0 {
		t.Errorf("on error the banner shows old warnings: %v", b.Ostrzeżenia)
	}

	// a different error → a new log entry
	breakFile("groups: 5\n")
	s.checkFileChange(s.ctx)
	mu.Lock()
	n = strings.Count(log.String(), "config invalid")
	mu.Unlock()
	if n != 2 {
		t.Errorf("different error: %d entries, want 2", n)
	}

	breakFile("title: Naprawiony\ngroups:\n  - name: Sieć\n")
	s.checkFileChange(s.ctx)
	html = homePage(t, s)
	if strings.Contains(html, `data-stan="error"`) || !strings.Contains(html, "Naprawiony") {
		t.Errorf("after the fix the banner should vanish and the panel load the file:\n%.800s", html)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestUnknownKeyIsWarningInBanner(t *testing.T) {
	s := serverOnContent(t, "groups:\n  - name: G\n    services:\n      - name: S\n        chek: {interval: 5s}\n")
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	html := homePage(t, s)
	if !strings.Contains(html, `data-stan="warnings"`) {
		t.Fatalf("no warnings banner:\n%.600s", html)
	}
	for _, want := range []string{`unknown key`, "chek", "line 5", `did you mean`, "check"} {
		if !strings.Contains(html, want) {
			t.Errorf("banner lacks %q", want)
		}
	}
	// in Polish
	pl := homePage(t, s, "Accept-Language", "pl")
	for _, want := range []string{"nieznany klucz", "linia 5", "chodziło o"} {
		if !strings.Contains(pl, want) {
			t.Errorf("PL version: missing %q", want)
		}
	}
	// the panel itself works normally
	if !strings.Contains(html, "S") {
		t.Error("the service disappeared")
	}
}

func TestGoodFileHasNoBanner(t *testing.T) {
	s := serverOnContent(t, goodConfig)
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if html := homePage(t, s); !strings.Contains(html, `data-stan="ok"`) {
		t.Error("the banner is not in state ok")
	}
}

// The raw YAML editor must work on a broken file — it is the only place in the UI
// from which it can be repaired.
func TestRawEditorRepairsBrokenFile(t *testing.T) {
	s := serverOnContent(t, brokenConfig)
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c := session(t, s)

	// bad YAML still rejected
	w := send(h, c, "PUT", "/api/config", `{"yaml":"groups: [\n"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad YAML accepted: %d", w.Code)
	}
	w = send(h, c, "PUT", "/api/config", `{"yaml":"title: Naprawa\ngroups:\n  - name: Sieć\n"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("repair through the editor: %d %s", w.Code, w.Body)
	}
	if html := homePage(t, s); strings.Contains(html, `data-stan="error"`) || !strings.Contains(html, "Naprawa") {
		t.Errorf("banner stayed after the fix:\n%.600s", html)
	}
	// structural operations on a broken file give a readable error, not a panic
	if err := os.WriteFile(s.path, []byte(brokenConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	w = send(h, c, "POST", "/api/groups", `{"name":"X"}`)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /api/groups on a broken file → %d, want 500 with a message", w.Code)
	}
}

// The banner travels to open tabs in the SSE event "konfig".
func TestBannerOverSSE(t *testing.T) {
	s := serverOnContent(t, goodConfig)
	if err := s.loadAfterStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := session(t, s)

	req, _ := http.NewRequest("GET", srv.URL+"/events", nil)
	req.AddCookie(c)
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var kind string
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				kind = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: ") && kind == "konfig":
				events <- strings.TrimPrefix(l, "data: ")
				kind = ""
			case l == "":
				kind = ""
			}
		}
	}()

	time.Sleep(100 * time.Millisecond) // subscription
	if err := os.WriteFile(s.path, []byte(brokenConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	s.checkFileChange(s.ctx)

	select {
	case d := <-events:
		var b struct {
			State string `json:"state"`
			Error string `json:"error"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal([]byte(d), &b); err != nil {
			t.Fatal(err)
		}
		if b.State != "error" || !strings.Contains(b.Error, "3") || b.Title == "" {
			t.Errorf("zdarzenie konfig = %s", d)
		}
	case <-ctx.Done():
		t.Fatal("no config event in SSE")
	}
}
