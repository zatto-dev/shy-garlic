package server

import (
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
)

// GH #6: PATCH /api/services skipped the "Check" section.

const checkConfig = `groups:
  - name: Infra
    services:
      - name: Proxmox
        url: https://pve.home.arpa:8006
      - name: NAS
        url: https://nas.home.arpa
        check:
          interval: 1m
          insecure: true
`

func checkServer(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s := testServer(t)
	if err := os.WriteFile(s.path, []byte(checkConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), loggedInCookie(t, s)
}

func TestPatchServiceMarksInsecure(t *testing.T) {
	s, h, c := checkServer(t)
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","insecure":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if !serviceFromFile(t, s, "Proxmox").Check.Insecure {
		t.Fatalf("insecure not saved:\n%s", s.configFile(t))
	}
}

func TestPatchServiceDryShowsCheckDiff(t *testing.T) {
	_, h, c := checkServer(t)
	w := send(h, c, "PATCH", "/api/services?dry=1",
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","insecure":true,"expect":[200,401]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "insecure: true") ||
		!strings.Contains(w.Body.String(), "expect: [200, 401]") {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
}

func TestPatchServiceUnchecksAndClears(t *testing.T) {
	s, h, c := checkServer(t)
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","insecure":false,"interval":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if file := string(s.configFile(t)); strings.Contains(file, "check:") {
		t.Fatalf("empty check block remained:\n%s", file)
	}
	if u := serviceFromFile(t, s, "NAS"); u.Check.Insecure || u.Check.Interval != config.Duration(config.DefaultInterval) {
		t.Fatalf("%+v", u.Check)
	}
}

// The form sends back the values it showed — the same as in the file or the defaults.
// They must not land in the file or change it at all.
func TestPatchServiceUnchangedCheckDoesNotTouchFile(t *testing.T) {
	s, h, c := checkServer(t)
	before := string(s.configFile(t))
	for _, body := range []string{
		// size too: the default `md` from the dialog does not add `size: md`
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","size":"md"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"1m0s","timeout":"5s","expect":[200,301,302,401,403],"insecure":true}`,
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","interval":"30s","timeout":"","expect":[],"insecure":false}`,
		// an API client without check fields does not erase the settings
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa"}`,
	} {
		if w := send(h, c, "PATCH", "/api/services", body); w.Code != http.StatusOK {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
	}
	if after := string(s.configFile(t)); after != before {
		t.Fatalf("the file changed:\n%s", after)
	}
}

func TestPatchServiceChangesCodesAndInterval(t *testing.T) {
	s, h, c := checkServer(t)
	w := send(h, c, "PATCH", "/api/services",
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"2m","expect":[200,401]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	u := serviceFromFile(t, s, "NAS")
	if u.Check.Interval != config.Duration(2*time.Minute) || !reflect.DeepEqual(u.Check.Expect, []int{200, 401}) || !u.Check.Insecure {
		t.Fatalf("%+v", u.Check)
	}
}

func TestPatchServiceRejectsBadCheck(t *testing.T) {
	_, h, c := checkServer(t)
	for _, body := range []string{
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"kiedyś"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","timeout":"-5s"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"500ms"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","expect":[200,9999]}`,
	} {
		if w := send(h, c, "PATCH", "/api/services", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d", body, w.Code)
		}
	}
}
