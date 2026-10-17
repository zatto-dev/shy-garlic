package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
)

// SHYG-52: POST and bulk validate check fields like PATCH instead of
// silently dropping or accepting bad values.

func TestCreateServiceRejectsInvalidCheck(t *testing.T) {
	s, h, c := checkServer(t)
	before := string(s.configFile(t))
	for _, body := range []string{
		`{"group":"Infra","name":"X","url":"https://x.home.arpa","interval":"someday"}`,
		`{"group":"Infra","name":"X","url":"https://x.home.arpa","interval":"1ns"}`,
		`{"group":"Infra","name":"X","url":"https://x.home.arpa","timeout":"-5s"}`,
		`{"group":"Infra","name":"X","url":"https://x.home.arpa","expect":[200,999]}`,
	} {
		if w := send(h, c, "POST", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d", body, w.Code)
		}
	}
	if after := string(s.configFile(t)); after != before {
		t.Fatalf("file changed:\n%s", after)
	}
}

func TestCreateServiceSavesValidCheck(t *testing.T) {
	s, h, c := checkServer(t)
	w := send(h, c, "POST", "/api/services",
		`{"group":"Infra","name":"X","url":"https://x.home.arpa","interval":" 2m ","timeout":"","expect":[200,401],"insecure":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	u := serviceFromFile(t, s, "X")
	if u.Check.Interval != config.Duration(2*time.Minute) || u.Check.Timeout != config.Duration(config.DefaultTimeout) ||
		!u.Check.Insecure || len(u.Check.Expect) != 2 {
		t.Fatalf("%+v", u.Check)
	}
}

func TestBulkCreateRejectsInvalidCheck(t *testing.T) {
	s, h, c := checkServer(t)
	before := string(s.configFile(t))
	w := send(h, c, "POST", "/api/services/bulk",
		`{"group":"Infra","services":[{"name":"A","url":"https://a.home.arpa"},{"name":"B","url":"https://b.home.arpa","interval":"500ms"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if after := string(s.configFile(t)); after != before || strings.Contains(after, "name: A") {
		t.Fatalf("partial bulk write:\n%s", after)
	}
}
