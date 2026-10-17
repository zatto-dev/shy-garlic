package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// leakMarkers must never show up in a message that reaches the client: they point
// at the database, the runtime or the file system instead of at what the user did.
var leakMarkers = []string{"constraint", "sql", "goroutine", "unique", "users.login", "/home/", "/tmp/", "/var/", ".go:", "panic"}

func errorText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not a JSON error: %q", w.Body)
	}
	return body.Error
}

// Every failure of the API answers with the right code and a translated message
// that says nothing about the inside of the program.
func TestAPIErrorsAreTranslatedAndClean(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		code                     int
	}{
		{"duplicate service", "POST", "/api/services", `{"group":"Media","name":"Sonarr","url":"http://x"}`, 409},
		{"duplicate group", "POST", "/api/groups", `{"name":"Media"}`, 409},
		{"duplicate service in bulk", "POST", "/api/services/bulk", `{"group":"Media","services":[{"name":"Sonarr","url":"http://x"}]}`, 409},
		{"bad JSON on group create", "POST", "/api/groups", `{`, 400},
		{"bad JSON on service create", "POST", "/api/services", `nope`, 400},
		{"group create without a name", "POST", "/api/groups", `{}`, 400},
		{"group rename without names", "PATCH", "/api/groups", `{"name":"Media"}`, 400},
		{"service delete without params", "DELETE", "/api/services", ``, 400},
		{"service delete in a missing group", "DELETE", "/api/services?group=Nope&name=x", ``, 404},
		{"group delete of a missing group", "DELETE", "/api/groups?name=Nope", ``, 404},
		{"service update of a missing service", "PATCH", "/api/services", `{"group":"Media","name":"Nope","newName":"X","url":"http://x"}`, 404},
		{"service create in a missing group", "POST", "/api/services", `{"group":"Nope","name":"X","url":"http://x"}`, 404},
		{"service move out of range", "POST", "/api/services/move", `{"group":"Media","name":"Sonarr","index":99}`, 400},
		{"empty bulk list", "POST", "/api/services/bulk", `{"services":[]}`, 400},
		{"invalid YAML", "PUT", "/api/config", `{"yaml":"groups: [unclosed"}`, 400},
		{"version not found", "GET", "/api/versions/9999", ``, 404},
		{"version with a bad id", "GET", "/api/versions/abc", ``, 400},
		{"restore of a missing version", "POST", "/api/versions/9999/restore", ``, 404},
		{"duplicate user", "POST", "/api/users", `{"login":"admin","password":"haslo12345"}`, 409},
		{"duplicate user, other case", "POST", "/api/users", `{"login":"ADMIN","password":"haslo12345"}`, 409},
		{"login too short", "POST", "/api/users", `{"login":"a","password":"haslo12345"}`, 400},
		{"password too short", "POST", "/api/users", `{"login":"second","password":"x"}`, 400},
		{"last account", "DELETE", "/api/users/9999", ``, 400},
		{"wrong current password", "POST", "/api/password", `{"old":"wrong","new":"newpassword1"}`, 403},
		{"new password too short", "POST", "/api/password", `{"old":"haslo12345","new":"x"}`, 400},
		{"history without a key", "GET", "/api/history", ``, 400},
		{"details of a missing service", "GET", "/api/service?key=nope", ``, 404},
		{"check of a missing service", "POST", "/api/check?key=nope", ``, 404},
		{"token not found", "DELETE", "/api/tokens/9999", ``, 404},
		{"token with a bad id", "DELETE", "/api/tokens/abc", ``, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, h, c := orderServer(t)
			en := send(h, c, tc.method, tc.path, tc.body, "Accept-Language", "en")
			pl := send(h, c, tc.method, tc.path, tc.body, "Accept-Language", "pl")
			if en.Code != tc.code || pl.Code != tc.code {
				t.Fatalf("code EN %d / PL %d, want %d: %s", en.Code, pl.Code, tc.code, en.Body)
			}
			textEN, textPL := errorText(t, en), errorText(t, pl)
			if textEN == "" || textPL == "" {
				t.Fatalf("empty message: EN %q PL %q", textEN, textPL)
			}
			if textEN == textPL {
				t.Errorf("EN and PL are the same, so one of them is not translated: %q", textEN)
			}
			for _, text := range []string{textEN, textPL} {
				lower := strings.ToLower(text)
				for _, bad := range leakMarkers {
					if strings.Contains(lower, bad) {
						t.Errorf("message %q contains %q", text, bad)
					}
				}
			}
		})
	}
}

// Deleting your own account is refused with a translated message too.
func TestAPIErrorDeleteOwnAccount(t *testing.T) {
	s, h, c := orderServer(t)
	users, err := s.st.Users(s.ctx)
	if err != nil || len(users) == 0 {
		t.Fatalf("users: %v %v", users, err)
	}
	path := "/api/users/" + strconv.FormatInt(users[0].ID, 10)
	en := send(h, c, "DELETE", path, "", "Accept-Language", "en")
	pl := send(h, c, "DELETE", path, "", "Accept-Language", "pl")
	if en.Code != 400 || errorText(t, en) == errorText(t, pl) || errorText(t, pl) == "" {
		t.Errorf("EN %d %q / PL %d %q", en.Code, errorText(t, en), pl.Code, errorText(t, pl))
	}
}

// A failure inside (here: the config file vanished, and the error text names its
// path) is a generic 500; the detail goes to the log and never to the client.
func TestAPIInternalErrorHidesTheDetail(t *testing.T) {
	s, h, c := orderServer(t)
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "pl"} {
		w := send(h, c, "POST", "/api/groups", `{"name":"New"}`, "Accept-Language", lang)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("%s: code %d, want 500: %s", lang, w.Code, w.Body)
		}
		text := errorText(t, w)
		lower := strings.ToLower(text)
		for _, bad := range append(leakMarkers, filepath.Base(s.path), "no such file", "open ") {
			if strings.Contains(lower, strings.ToLower(bad)) {
				t.Errorf("%s: message %q contains %q", lang, text, bad)
			}
		}
	}
}

// The duplicate message is the translated one, in both languages.
func TestDuplicateMessagesAreTranslated(t *testing.T) {
	_, h, c := orderServer(t)
	for lang, want := range map[string]string{"en": "taken", "pl": "zajęta"} {
		for _, p := range []struct{ path, body string }{
			{"/api/services", `{"group":"Media","name":"Sonarr","url":"http://x"}`},
			{"/api/groups", `{"name":"Media"}`},
		} {
			w := send(h, c, "POST", p.path, p.body, "Accept-Language", lang)
			if w.Code != 409 || !strings.Contains(errorText(t, w), want) {
				t.Errorf("%s %s: %d %s", lang, p.path, w.Code, w.Body)
			}
		}
	}
}
