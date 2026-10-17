package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

func setupForm(token, login string) url.Values {
	return url.Values{"login": {login}, "password": {"correct-horse-9"}, "setup_token": {token}}
}

// ── first-run setup ──────────────────────────────────────────────────────────

func TestSetupNeedsTheTokenFromTheLog(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	ctx := context.Background()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/setup", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="setup_token"`) {
		t.Fatalf("GET /setup: %d, the form has no token field", w.Code)
	}
	if strings.Contains(w.Body.String(), s.SetupToken()) {
		t.Fatal("the setup token is printed on the page")
	}

	for _, bad := range []string{"", "nope", "0000-0000-0000-0000"} {
		if w := postForm(h, "/setup", setupForm(bad, "owner")); w.Code != http.StatusForbidden {
			t.Errorf("token %q: code %d, want 403", bad, w.Code)
		}
	}
	if n, _ := s.st.UserCount(ctx); n != 0 {
		t.Fatalf("an account was created without the token (%d)", n)
	}

	// the token is accepted regardless of case and separators
	token := strings.ToUpper(strings.ReplaceAll(s.SetupToken(), "-", " "))
	w = postForm(h, "/setup", setupForm(token, "owner"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("setup with the right token: %d %s", w.Code, w.Body)
	}
	if n, _ := s.st.UserCount(ctx); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	if s.setup.token != "" {
		t.Error("the token outlived the setup")
	}
	if w := postForm(h, "/setup", setupForm("anything", "second")); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("setup after setup: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestSetupParallelRequestsCreateOneAccount(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	token := s.SetupToken()
	var wg sync.WaitGroup
	codes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := postForm(h, "/setup", setupForm(token, fmt.Sprintf("admin%d", i)))
			codes <- fmt.Sprintf("%d %s", w.Code, w.Header().Get("Location"))
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		if c == "303 /" {
			created++
		}
	}
	if n, _ := s.st.UserCount(context.Background()); n != 1 || created != 1 {
		t.Fatalf("accounts = %d, requests that got in = %d; want 1 and 1", n, created)
	}
}

func TestSetupTokenGuessesAreRateLimited(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	for i := 0; i < 5; i++ {
		if w := postForm(h, "/setup", setupForm("wrong", "owner")); w.Code != http.StatusForbidden {
			t.Fatalf("guess %d: %d", i, w.Code)
		}
	}
	if w := postForm(h, "/setup", setupForm(s.SetupToken(), "owner")); w.Code != http.StatusTooManyRequests {
		t.Errorf("the sixth attempt, even with the right token: %d, want 429", w.Code)
	}
}

func TestEnvAdminSkipsSetup(t *testing.T) {
	s := testServer(t)
	if err := BootstrapUser(context.Background(), s.st, "envadmin", "env-password-1"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/setup", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("GET /setup with an ENV admin: %d %s", w.Code, w.Header().Get("Location"))
	}
	// and a second call (restart) is harmless
	if err := BootstrapUser(context.Background(), s.st, "envadmin", "env-password-1"); err != nil {
		t.Fatal(err)
	}
}

func TestSetupTokenFormat(t *testing.T) {
	a, b := newSetupToken(), newSetupToken()
	if a == b || len(a) != 35 || strings.Count(a, "-") != 3 {
		t.Fatalf("tokens %q %q", a, b)
	}
}

// ── allowed Host names ───────────────────────────────────────────────────────

func TestAllowedHostsRefuseOtherNames(t *testing.T) {
	s := testServer(t)
	s.allowedHosts = parseAllowedHosts([]string{"panel.example.org", " Dash.lan ", ""})
	h := s.Handler()
	for host, want := range map[string]int{
		"panel.example.org":      http.StatusSeeOther, // allowed: /login redirects to /setup on a fresh instance
		"PANEL.example.org:8443": http.StatusSeeOther, // allowed: /login redirects to /setup on a fresh instance
		"dash.lan":               http.StatusSeeOther, // allowed: /login redirects to /setup on a fresh instance
		"localhost:8080":         http.StatusSeeOther, // allowed: /login redirects to /setup on a fresh instance
		"127.0.0.1:8080":         http.StatusSeeOther, // allowed: /login redirects to /setup on a fresh instance
		"[::1]:8080":             http.StatusSeeOther, // allowed: /login redirects to /setup on a fresh instance
		"evil.example":           http.StatusMisdirectedRequest,
		"panel.example.org.evil": http.StatusMisdirectedRequest,
		"192.168.1.5:8080":       http.StatusMisdirectedRequest,
		"":                       http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest("GET", "/login", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: %d, want %d", host, w.Code, want)
		}
	}
	// the setup form is refused too, so a rebinding page cannot reach it
	r := httptest.NewRequest("GET", "/setup", nil)
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("/setup for a foreign Host: %d", w.Code)
	}
}

func TestHealthzIsExemptFromHostCheck(t *testing.T) {
	s := testServer(t)
	s.allowedHosts = parseAllowedHosts([]string{"panel.example.org"})
	h := s.Handler()
	for path, want := range map[string]int{
		"/healthz": http.StatusOK,
		"/metrics": http.StatusMisdirectedRequest, // stays under the check
		"/":        http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "10.42.0.17:8080" // a pod IP, as a kubelet probe sends it
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s with a pod-IP Host: %d, want %d", path, w.Code, want)
		}
	}
}

func TestNoAllowedHostsMeansAnyHost(t *testing.T) {
	s := testServer(t)
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.Host = "whatever.example"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("default behaviour changed: %d", w.Code)
	}
}

// ── webhook test endpoint ────────────────────────────────────────────────────

func TestNotifyTestDoesNotRevealNetworkDetails(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s)
	h := s.Handler()
	for _, hook := range []string{
		"http://169.254.169.254/latest/meta-data/", // blocked by the dialer
		"http://127.0.0.1:1/hook",                  // connection refused
		"http://no-such-host.invalid/hook",         // DNS failure
	} {
		s.notifier.SetWebhook(hook)
		w := send(h, cookie, "POST", "/api/notify/test", "")
		if w.Code != http.StatusBadGateway {
			t.Fatalf("%s: code %d", hook, w.Code)
		}
		body := w.Body.String()
		for _, leak := range []string{"refused", "169.254", "blocked", "no such host", "dial", "127.0.0.1", "lookup", "invalid"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the response reveals %q: %s", hook, leak, body)
			}
		}
		if !strings.Contains(body, i18n.T(i18n.EN, "webhook_test_failed")[:20]) {
			t.Errorf("%s: unexpected message %s", hook, body)
		}
	}
}

// ── credentials in service addresses ─────────────────────────────────────────

const credentialConfig = `title: T
groups:
  - name: NAS
    services:
      - name: Files
        url: http://admin:hunter2@127.0.0.1:1/share
      - name: Slow
        url: http://slow.lan
        check:
          interval: -5s
          timeout: 99999999999h
`

func serverWithCredentials(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	s := testServer(t)
	if err := os.WriteFile(s.path, []byte(credentialConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, loggedInCookie(t, s)
}

func TestCredentialsInServiceURLDoNotLeak(t *testing.T) {
	s, cookie := serverWithCredentials(t)
	h := s.Handler()

	// page: data-address, href, host
	w := send(h, cookie, "GET", "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Error("the page carries the password")
	}
	if !strings.Contains(w.Body.String(), `data-address="http://127.0.0.1:1/share"`) {
		t.Error("data-address should hold the address without the login")
	}
	// /api/service
	w = send(h, cookie, "GET", "/api/service?key="+url.QueryEscape("NAS/Files"), "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "hunter2") || strings.Contains(w.Body.String(), "admin:") {
		t.Errorf("/api/service: %d %s", w.Code, w.Body)
	}
	// the banner names the service but does not repeat the secret
	b := s.instanceBanner(i18n.EN)
	joined := strings.Join(b.Warnings, "\n")
	if strings.Contains(joined, "hunter2") {
		t.Error("the banner leaks the password")
	}
	if !strings.Contains(joined, "contains a login and password") {
		t.Errorf("no credentials warning in the banner: %q", joined)
	}
	// out-of-range check values: replaced and reported, process alive
	if !strings.Contains(joined, "check.interval") || !strings.Contains(joined, "check.timeout") {
		t.Errorf("no duration warnings in the banner: %q", joined)
	}
	if got := s.monitorTarget("NAS/Slow"); got.Interval != 30*time.Second || got.Timeout != 5*time.Second {
		t.Errorf("target %+v should have the default interval and timeout", got)
	}
	// the check itself still uses the full address
	if got := s.monitorTarget("NAS/Files"); got.URL != "http://admin:hunter2@127.0.0.1:1/share" {
		t.Errorf("the health check lost its credentials: %q", got.URL)
	}
	// PL banner too
	if strings.Contains(strings.Join(s.instanceBanner(i18n.PL).Warnings, "\n"), "hunter2") {
		t.Error("the PL banner leaks the password")
	}
}

func (s *Server) monitorTarget(key string) health.Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.cfg.Groups {
		for _, svc := range g.Services {
			if g.Name+"/"+svc.Name == key {
				return health.Target{URL: svc.URL, Interval: svc.Check.Interval.Duration(), Timeout: svc.Check.Timeout.Duration()}
			}
		}
	}
	return health.Target{}
}

func TestNotificationsDoNotCarryCredentials(t *testing.T) {
	s, _ := serverWithCredentials(t)
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b)+" "+r.Header.Get("Click"))
		mu.Unlock()
	}))
	defer hook.Close()
	s.notifier.SetWebhook(hook.URL)
	s.notifier.SetThreshold(1)
	s.notify(context.Background(), "NAS/Files", health.Result{State: health.StateDown, Err: "connection refused", At: time.Now()})
	s.notifier.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no notification was sent")
	}
	for _, m := range got {
		if strings.Contains(m, "hunter2") || strings.Contains(m, "admin:") {
			t.Errorf("notification carries the credentials: %s", m)
		}
		if !strings.Contains(m, "127.0.0.1:1/share") {
			t.Errorf("notification lost the address: %s", m)
		}
	}
}

// The dialog shows the address without its login and sends it back on save; that
// must not strip the credentials from the file.
func TestEditingDoesNotStripCredentials(t *testing.T) {
	s, cookie := serverWithCredentials(t)
	h := s.Handler()
	body := `{"group":"NAS","name":"Files","url":"http://127.0.0.1:1/share","icon":"","size":"md"}`
	if w := send(h, cookie, "PATCH", "/api/services", body); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(string(s.configFile(t)), "http://admin:hunter2@127.0.0.1:1/share") {
		t.Fatalf("the credentials were stripped from the file:\n%s", s.configFile(t))
	}
	// a real change still goes through
	body = `{"group":"NAS","name":"Files","url":"http://127.0.0.1:2/other"}`
	if w := send(h, cookie, "PATCH", "/api/services", body); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(string(s.configFile(t)), "http://127.0.0.1:2/other") {
		t.Fatalf("the address was not changed:\n%s", s.configFile(t))
	}
}

func TestStatusErrorsDoNotCarryCredentials(t *testing.T) {
	// health.Check redacts; this guards the wiring end to end through /api/status
	s, cookie := serverWithCredentials(t)
	s.monitor.SetTargets(context.Background(), []health.Target{{
		Key: "NAS/Files", URL: "http://admin:hunter2@127.0.0.1:1/", Timeout: time.Second}})
	if _, err := s.monitor.CheckNow(context.Background(), "NAS/Files"); err != nil {
		t.Fatal(err)
	}
	w := send(s.Handler(), cookie, "GET", "/api/status", "")
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Errorf("/api/status: %s", w.Body)
	}
}

// ── names ────────────────────────────────────────────────────────────────────

func TestAPIRejectsControlCharactersInNames(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s)
	h := s.Handler()
	post := func(method, path string, v map[string]any) int {
		b, _ := json.Marshal(v)
		return send(h, cookie, method, path, string(b)).Code
	}
	for _, name := range []string{"a\rb", "a\nb", "a\x00b", "a\x1bb", "a\x7fb", strings.Repeat("x", 201)} {
		if c := post("POST", "/api/groups", map[string]any{"name": name}); c != http.StatusBadRequest {
			t.Errorf("group %q: %d, want 400", name, c)
		}
		if c := post("PATCH", "/api/groups", map[string]any{"name": "Media", "newName": name}); c != http.StatusBadRequest {
			t.Errorf("rename to %q: %d, want 400", name, c)
		}
		if c := post("POST", "/api/services", map[string]any{"group": "Media", "name": name, "url": "http://x"}); c != http.StatusBadRequest {
			t.Errorf("service %q: %d, want 400", name, c)
		}
	}
	if c := post("POST", "/api/groups", map[string]any{"name": "Fine name"}); c != http.StatusCreated {
		t.Errorf("a normal group: %d", c)
	}
	if bytes.ContainsAny(s.configFile(t), "\r\x00\x1b\x7f") {
		t.Errorf("a control character reached the file")
	}
}

func TestAPIRejectsOutOfRangeDurations(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s)
	for _, v := range []string{"25h", "99999999999h", "-5s", "0s", "500ms"} {
		body := fmt.Sprintf(`{"group":"Media","name":"New","url":"http://x","interval":%q}`, v)
		if w := send(s.Handler(), cookie, "POST", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("interval %s: %d, want 400", v, w.Code)
		}
	}
}

// ── password endpoint ────────────────────────────────────────────────────────

func TestPasswordChangeIsRateLimited(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s) // admin / haslo12345
	h := s.Handler()
	for i := 0; i < 5; i++ {
		if w := send(h, cookie, "POST", "/api/password", `{"old":"wrong-guess","new":"newpassword1"}`); w.Code != http.StatusForbidden {
			t.Fatalf("guess %d: %d", i, w.Code)
		}
	}
	if w := send(h, cookie, "POST", "/api/password", `{"old":"haslo12345","new":"newpassword1"}`); w.Code != http.StatusTooManyRequests {
		t.Errorf("after five wrong guesses even the right password: %d, want 429", w.Code)
	}
}

// ── outbound address filter ──────────────────────────────────────────────────

func TestPublicAddress(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"93.184.216.34", true},
		// the old list
		{"127.0.0.1", false}, {"::1", false}, {"10.1.2.3", false}, {"172.16.0.1", false},
		{"192.168.1.1", false}, {"169.254.169.254", false}, {"fe80::1", false}, {"fd00::1", false},
		{"100.64.0.1", false}, {"100.127.255.255", false}, {"0.0.0.0", false}, {"224.0.0.1", false},
		{"::ffff:10.0.0.1", false},
		// ranges that slipped through
		{"64:ff9b::a00:1", false},                       // NAT64 → 10.0.0.1
		{"64:ff9b::7f00:1", false},                      // NAT64 → 127.0.0.1
		{"64:ff9b:1::1", false},                         // local-use NAT64
		{"2002:a00:1::1", false},                        // 6to4 → 10.0.0.1
		{"2002:7f00:1::", false},                        // 6to4 → 127.0.0.1
		{"2001:0:4136:e378:8000:63bf:3fff:fdd2", false}, // Teredo
		{"198.18.0.1", false}, {"198.19.255.255", false},
		{"192.0.0.1", false}, {"192.0.0.170", false},
		{"240.0.0.1", false}, {"255.255.255.255", false},
		{"0.1.2.3", false}, {"0.255.255.255", false},
		// neighbours that must stay reachable
		{"198.17.255.255", true}, {"198.20.0.0", true},
		{"192.0.1.1", true}, {"239.255.255.255", false}, {"223.255.255.255", true},
		{"1.0.0.1", true}, {"2001:4860:4860::8888", true}, {"2001:1::1", true},
	}
	for _, c := range cases {
		if got := publicAddress(netip.MustParseAddr(c.ip)); got != c.want {
			t.Errorf("publicAddress(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// ── headers and static files ─────────────────────────────────────────────────

func TestSecurityHeaders(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	get := func(proto string, remote string) http.Header {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Header()
	}
	if err := SetTrustedProxy([]string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetTrustedProxy(nil) })

	if v := get("https", "192.0.2.10:1234").Get("Strict-Transport-Security"); v != "max-age=31536000" {
		t.Errorf("HSTS behind a trusted proxy on https = %q", v)
	}
	if v := get("", "192.0.2.10:1234").Get("Strict-Transport-Security"); v != "" {
		t.Errorf("HSTS over plain http = %q", v)
	}
	// an untrusted peer cannot claim https
	if v := get("https", "198.51.100.7:1234").Get("Strict-Transport-Security"); v != "" {
		t.Errorf("HSTS from an untrusted peer = %q", v)
	}
	if v := get("", "192.0.2.10:1234").Get("Permissions-Policy"); !strings.Contains(v, "camera=()") {
		t.Errorf("Permissions-Policy = %q", v)
	}
}

func TestAssetsDirectoryIsNotListed(t *testing.T) {
	h := testServer(t).Handler()
	for _, path := range []string{"/assets/", "/assets/fonts/", "/assets"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "style.css") {
			t.Errorf("GET %s: %d, lists the directory", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/style.css", nil))
	if w.Code != http.StatusOK {
		t.Errorf("style.css: %d", w.Code)
	}
}

func TestFailedLoginLogDoesNotKeepTheTypedLogin(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	s := testServer(t)
	_ = loggedInCookie(t, s)
	postForm(s.Handler(), "/login", url.Values{"login": {"P4ssw0rd-typed-as-login"}, "password": {"x"}})
	out := buf.String()
	if !strings.Contains(out, "login failed") {
		t.Fatalf("no log line: %s", out)
	}
	if strings.Contains(out, "P4ssw0rd") {
		t.Errorf("the typed login is in the log: %s", out)
	}
	if !strings.Contains(out, "sha256:") {
		t.Errorf("no fingerprint of the login in the log: %s", out)
	}
	if loginForLog("Admin") != loginForLog("admin") {
		t.Error("the fingerprint should not depend on case")
	}
}

// ── server limits ────────────────────────────────────────────────────────────

func TestHTTPServerHasLimits(t *testing.T) {
	srv := NewHTTPServer(":0", http.NewServeMux())
	if srv.ReadTimeout != 30*time.Second || srv.ReadHeaderTimeout == 0 || srv.WriteTimeout == 0 ||
		srv.IdleTimeout == 0 || srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("limits: read %v, header %v, write %v, idle %v, max header %d",
			srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
}

func sseServer(t *testing.T, s *Server, write time.Duration) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.WriteTimeout = write
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func openStream(t *testing.T, ts *httptest.Server, cookie *http.Cookie) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return resp, cancel
}

func TestEventStreamsPerUserAreLimited(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s)
	ts := sseServer(t, s, time.Minute)
	var cancels []context.CancelFunc
	for i := 0; i < maxStreamsPerUser; i++ {
		resp, cancel := openStream(t, ts, cookie)
		cancels = append(cancels, cancel)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: %d", i+1, resp.StatusCode)
		}
	}
	resp, cancel := openStream(t, ts, cookie)
	defer cancel()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream %d: %d, want 429", maxStreamsPerUser+1, resp.StatusCode)
	}
	// closing one frees a slot
	cancels[0]()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, c := openStream(t, ts, cookie)
		code := resp.StatusCode
		defer c()
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a slot was not released after a stream closed (last code %d)", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, c := range cancels {
		c()
	}
}

// The server has a WriteTimeout; an event stream must outlive it.
func TestEventStreamOutlivesWriteTimeout(t *testing.T) {
	s := testServer(t)
	cookie := loggedInCookie(t, s)
	ts := sseServer(t, s, 300*time.Millisecond)
	resp, cancel := openStream(t, ts, cookie)
	defer cancel()
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil { // the initial snapshot
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond) // well past the write timeout
	s.broadcastAppearance()
	done := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			if strings.HasPrefix(line, "event: ") {
				done <- nil
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the stream was cut after the write timeout: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event arrived")
	}
}

// A client that stops reading must not keep its slot: every write has a deadline
// and a failed one ends the stream.
func TestEventStreamEndsWhenClientStopsReading(t *testing.T) {
	old := streamWriteTimeout
	streamWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { streamWriteTimeout = old })

	s := testServer(t)
	cookie := loggedInCookie(t, s)
	ts := sseServer(t, s, time.Minute)

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(1024)
	fmt.Fprintf(conn, "GET /events HTTP/1.1\r\nHost: x\r\nCookie: %s=%s\r\n\r\n", cookie.Name, cookie.Value)

	open := func() int {
		s.sseMu.Lock()
		defer s.sseMu.Unlock()
		n := 0
		for _, c := range s.sseStreams {
			n += c
		}
		return n
	}
	deadline := time.Now().Add(5 * time.Second)
	for open() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if open() != 1 {
		t.Fatal("stream did not open")
	}
	// never read from conn; flood the stream with events until the buffers fill
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				s.broadcastAppearance()
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()
	deadline = time.Now().Add(40 * time.Second)
	for open() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slot of a client that stopped reading was never released")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestBootstrapUserDoesNotClaimSuccessOnFailure(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	s := testServer(t)
	// too short a password: no account, so no "created" line
	if err := BootstrapUser(context.Background(), s.st, "envadmin", "short"); err == nil {
		t.Fatal("a rejected account reported success")
	}
	if strings.Contains(buf.String(), "created") {
		t.Errorf("logged a creation that did not happen: %s", buf.String())
	}
	if n, _ := s.st.UserCount(context.Background()); n != 0 {
		t.Fatalf("users = %d", n)
	}
	if err := BootstrapUser(context.Background(), s.st, "envadmin", "long-enough-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "admin account created from ENV") {
		t.Errorf("no creation line: %s", buf.String())
	}
}
