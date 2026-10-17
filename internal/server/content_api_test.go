package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

// The whole path: YAML → Load → monitor → result → /api/service. Checks that the fields
// check.path/contains reached health.Target (apply), and not only that
// each layer works separately.
func TestContentCheckFromConfigToResult(t *testing.T) {
	// the monitor also checks on its own in the background, so writing the path goes under a mutex
	var (
		mu   sync.Mutex
		path string
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		mu.Unlock()
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"starting"}`))
			return
		}
		w.WriteHeader(500)
	}))
	defer target.Close()

	s := testServer(t)
	cfg := "groups:\n  - name: G\n    services:\n      - name: S\n        url: " + target.URL + "/ui\n" +
		"        check:\n          path: /health\n          contains: '\"status\":\"ok\"'\n"
	if err := os.WriteFile(s.path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}

	r, err := s.monitor.CheckNow(s.ctx, "G/S")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := path
	mu.Unlock()
	if last != "/health" {
		t.Errorf("check went to %q, want /health", last)
	}
	if r.State != health.StateDegraded || !strings.HasPrefix(r.Err, health.ContentPrefix) {
		t.Fatalf("%+v", r)
	}

	// service details show the rule and the reason
	c := loggedInCookie(t, s)
	w := send(s.Handler(), c, "GET", "/api/service?key=G%2FS", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	var reply struct {
		Path     string `json:"path"`
		Contains string `json:"contains"`
		Now      struct {
			State string `json:"state"`
			Err   string `json:"err"`
		} `json:"now"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Path != "/health" || reply.Contains != `"status":"ok"` {
		t.Errorf("rule in details: %+v", reply)
	}
	if reply.Now.State != "degraded" || !strings.Contains(reply.Now.Err, "status") {
		t.Errorf("reason in details: %+v", reply.Now)
	}
}

func TestContentCheckTextsHaveBothLanguages(t *testing.T) {
	for _, l := range []i18n.Lang{i18n.PL, i18n.EN} {
		m := i18n.Map(l)
		for _, k := range []string{"chip_tresc", "powod_stanu", "kontrola_get", "kontrola_zawiera", "kontrola_nie_zawiera"} {
			if v := m[k]; v == "" || v == k {
				t.Errorf("%s: missing text %q", l, k)
			}
		}
	}
}
