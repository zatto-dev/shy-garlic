package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// configFile reads the file from disk, not the in-memory state — the point is to check
// that the write really left the process.
func (s *Server) configFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("czytam config: %v", err)
	}
	return b
}

func TestNotifyGetSourceDB(t *testing.T) {
	s := testServer(t)
	if err := s.st.SetSetting(s.ctx, "notify.webhook", "https://example.test/hooks/abc123"); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.notifier.SetWebhook("https://example.test/hooks/abc123")

	rr := httptest.NewRecorder()
	s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, have %d", rr.Code)
	}
	var reply struct {
		Enabled bool   `json:"enabled"`
		Webhook string `json:"webhook"`
		Source  string `json:"source"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&reply); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	if !reply.Enabled || reply.Source != "database" {
		t.Fatalf("want enabled/db, have %v/%q", reply.Enabled, reply.Source)
	}
	if strings.Contains(reply.Webhook, "abc123") {
		t.Fatalf("the secret must be masked, it went out whole: %q", reply.Webhook)
	}
	if !strings.Contains(reply.Webhook, "example.test") {
		t.Fatalf("the host must stay so the address can be recognized: %q", reply.Webhook)
	}
}

func TestNotifyGetWithoutWebhook(t *testing.T) {
	s := testServer(t)
	rr := httptest.NewRecorder()
	s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
	var reply struct {
		Enabled bool   `json:"enabled"`
		Source  string `json:"source"`
	}
	json.NewDecoder(rr.Body).Decode(&reply)
	if reply.Enabled || reply.Source != "none" {
		t.Fatalf("want disabled/none, have %v/%q", reply.Enabled, reply.Source)
	}
}

// A webhook from the environment belongs to the deploy. The panel must not
// overwrite it — otherwise a container restart would silently undo a change from the UI.
func TestNotifyPutRejectsWhenSourceIsEnv(t *testing.T) {
	s := testServer(t)
	s.webhookFromEnv = true

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"https://inny.test/hook"}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("want 409, have %d", rr.Code)
	}
	v, _ := s.st.Setting(s.ctx, "notify.webhook")
	if v != "" {
		t.Fatalf("a rejected request must not save anything, the DB holds %q", v)
	}
}

func TestNotifyPutSavesToDBAndEnables(t *testing.T) {
	s := testServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"https://example.test/hooks/xyz"}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, have %d — body: %s", rr.Code, rr.Body.String())
	}
	v, _ := s.st.Setting(s.ctx, "notify.webhook")
	if v != "https://example.test/hooks/xyz" {
		t.Fatalf("the address must land in the DB, have %q", v)
	}
	if !s.notifier.Enabled() {
		t.Fatal("after the save notifications must be enabled without a restart")
	}
}

// The secret travels in the address path. Over http it would go in clear text, so
// the panel must not allow that.
func TestNotifyPutRejectsHTTP(t *testing.T) {
	s := testServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"http://example.test/hook"}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for http, have %d", rr.Code)
	}
}

func TestNotifyPutClearsWebhook(t *testing.T) {
	s := testServer(t)
	_ = s.st.SetSetting(s.ctx, "notify.webhook", "https://example.test/hook")
	s.notifier.SetWebhook("https://example.test/hook")

	rr := httptest.NewRecorder()
	s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":""}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, have %d — body: %s", rr.Code, rr.Body.String())
	}
	if s.notifier.Enabled() {
		t.Fatal("after clearing, notifications must be disabled")
	}
	v, _ := s.st.Setting(s.ctx, "notify.webhook")
	if v != "" {
		t.Fatalf("the row must disappear from the DB, it holds %q", v)
	}
}

func TestNotifyPutSavesThreshold(t *testing.T) {
	s := testServer(t)
	rr := httptest.NewRecorder()
	s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"threshold":4}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, have %d — body: %s", rr.Code, rr.Body.String())
	}
	if s.notifier.Threshold() != 4 {
		t.Fatalf("the threshold must take effect without a restart, have %d", s.notifier.Threshold())
	}
	if !strings.Contains(string(s.configFile(t)), "threshold: 4") {
		t.Fatalf("the threshold must land in the YAML:\n%s", s.configFile(t))
	}
}

func TestNotifyPutRejectsBadThreshold(t *testing.T) {
	s := testServer(t)
	for _, body := range []string{`{"threshold":0}`, `{"threshold":11}`} {
		rr := httptest.NewRecorder()
		s.handleNotifyPut(rr, httptest.NewRequest("PUT", "/api/notify", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, have %d", body, rr.Code)
		}
	}
}

func TestInstanceReturnsSourcesWithoutSecrets(t *testing.T) {
	s := testServer(t)
	s.instance = []OptionInfo{
		{Key: "addr", Value: ":8080", Source: "default"},
		{Key: "trusted-proxy", Value: "10.0.0.0/8", Source: "flag"},
	}
	rr := httptest.NewRecorder()
	s.handleInstance(rr, httptest.NewRequest("GET", "/api/instance", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, have %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"addr", ":8080", "default", "trusted-proxy", "flag"} {
		if !strings.Contains(body, want) {
			t.Fatalf("brakuje %q w %s", want, body)
		}
	}
	// The webhook is a secret and must not be here, even masked —
	// that is what /api/notify is for.
	if strings.Contains(body, "webhook") {
		t.Fatal("the webhook does not belong in the instance list")
	}
}

func TestDiscoverDisabledReturns403(t *testing.T) {
	s := testServer(t)
	off := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &off
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleDiscover(rr, httptest.NewRequest("GET", "/api/discover", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403 with suggestions disabled, have %d — body: %s", rr.Code, rr.Body.String())
	}
}

// A missing key is not the same as false — it should behave as before, i.e.
// refuse because of the missing socket (404), not because of being disabled (403).
func TestDiscoverWithoutKeyIsNotDisabled(t *testing.T) {
	s := testServer(t)
	rr := httptest.NewRecorder()
	s.handleDiscover(rr, httptest.NewRequest("GET", "/api/discover", nil))
	if rr.Code == http.StatusForbidden {
		t.Fatal("without the key suggestions must stay enabled")
	}
}

// The panel hides a menu item based on /api/info.docker — disabling it in the file
// must show up there, otherwise the user would click something that answers 403.
func TestInfoRejectsDisabledDocker(t *testing.T) {
	s := testServer(t)
	off := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &off
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleInfo(rr, httptest.NewRequest("GET", "/api/info", nil))
	var reply struct {
		Docker    bool `json:"docker"`
		DockerNew int  `json:"dockerNew"`
	}
	json.NewDecoder(rr.Body).Decode(&reply)
	if reply.Docker {
		t.Fatal("disabled docker must go to the UI as false")
	}
	if reply.DockerNew != 0 {
		t.Fatalf("the badge must be empty for a hidden entry, have %d", reply.DockerNew)
	}
}

// Disabled in the file and a missing socket must reach the UI separately — otherwise
// the panel does not know whether to hide the item or only to fade it.
func TestInfoDistinguishesMissingDockerReasons(t *testing.T) {
	s := testServer(t)
	off := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &off
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleInfo(rr, httptest.NewRequest("GET", "/api/info", nil))
	var reply struct {
		Docker   bool `json:"docker"`
		Disabled bool `json:"dockerDisabled"`
	}
	json.NewDecoder(rr.Body).Decode(&reply)
	if !reply.Disabled {
		t.Fatal("disabling in the file must be distinguishable from a missing socket")
	}

	// without the key: not disabled, even if there is no socket
	s2 := testServer(t)
	rr2 := httptest.NewRecorder()
	s2.handleInfo(rr2, httptest.NewRequest("GET", "/api/info", nil))
	var reply2 struct {
		Disabled bool `json:"dockerDisabled"`
	}
	json.NewDecoder(rr2.Body).Decode(&reply2)
	if reply2.Disabled {
		t.Fatal("a missing key is not a disable")
	}
}

// notify.threshold from the file must take effect at start-up, not only after a save
// from the panel. Without that, setting the threshold in the file and restarting quietly restored
// the default two — and the file is the source of truth in this project.
func TestThresholdFromFileWorksAfterStart(t *testing.T) {
	s := testServer(t)
	if !s.saveToConfig(t, "notify:\n  threshold: 5\n") {
		return
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s.notifier.Threshold(); got != 5 {
		t.Fatalf("the threshold from the file must apply after start, have %d", got)
	}
}

// An explicitly given flag belongs to the deploy and beats the file — the same rule as
// with the webhook, so a container restart does not undo a setting from outside.
func TestThresholdFlagBeatsFile(t *testing.T) {
	s := testServer(t)
	s.thresholdFromFlag = true
	s.notifier.SetThreshold(7)
	if !s.saveToConfig(t, "notify:\n  threshold: 5\n") {
		return
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s.notifier.Threshold(); got != 7 {
		t.Fatalf("the flag must win over the file, have %d", got)
	}
}

// saveToConfig appends a block to the end of the server's config file.
func (s *Server) saveToConfig(t *testing.T, block string) bool {
	t.Helper()
	old, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("czytam config: %v", err)
	}
	if err := os.WriteFile(s.path, append(old, []byte("\n"+block)...), 0o644); err != nil {
		t.Fatalf("saving config: %v", err)
	}
	return true
}
