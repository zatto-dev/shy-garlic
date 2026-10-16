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

// GH #6: PATCH /api/services pomijał sekcję „Sprawdzanie".

const configSprawdzanie = `groups:
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

func serwerSprawdzanie(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s := serwerTestowy(t)
	if err := os.WriteFile(s.path, []byte(configSprawdzanie), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(s.ctx); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(), ciasteczkoZalogowanego(t, s)
}

func TestPatchUsługiZaznaczaInsecure(t *testing.T) {
	s, h, c := serwerSprawdzanie(t)
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","insecure":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if !usługaZPliku(t, s, "Proxmox").Check.Insecure {
		t.Fatalf("insecure nie zapisany:\n%s", s.plikConfigu(t))
	}
}

func TestPatchUsługiDryPokazujeDiffSprawdzania(t *testing.T) {
	_, h, c := serwerSprawdzanie(t)
	w := wyślij(h, c, "PATCH", "/api/services?dry=1",
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","insecure":true,"expect":[200,401]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "insecure: true") ||
		!strings.Contains(w.Body.String(), "expect: [200, 401]") {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
}

func TestPatchUsługiOdznaczaICzyści(t *testing.T) {
	s, h, c := serwerSprawdzanie(t)
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","insecure":false,"interval":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	if plik := string(s.plikConfigu(t)); strings.Contains(plik, "check:") {
		t.Fatalf("pusty blok check został:\n%s", plik)
	}
	if u := usługaZPliku(t, s, "NAS"); u.Check.Insecure || u.Check.Interval != config.Duration(config.DefaultInterval) {
		t.Fatalf("%+v", u.Check)
	}
}

// Formularz odsyła wartości, które pokazał — te same co w pliku albo domyślne.
// Nie mogą wylądować w pliku ani zmieniać go w ogóle.
func TestPatchUsługiNiezmienioneSprawdzanieNieRuszaPliku(t *testing.T) {
	s, h, c := serwerSprawdzanie(t)
	przed := string(s.plikConfigu(t))
	for _, ciało := range []string{
		// rozmiar też: domyślny `md` z dialogu nie dopisuje `size: md`
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","size":"md"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"1m0s","timeout":"5s","expect":[200,301,302,401,403],"insecure":true}`,
		`{"group":"Infra","name":"Proxmox","url":"https://pve.home.arpa:8006","interval":"30s","timeout":"","expect":[],"insecure":false}`,
		// klient API bez pól sprawdzania nie kasuje ustawień
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa"}`,
	} {
		if w := wyślij(h, c, "PATCH", "/api/services", ciało); w.Code != http.StatusOK {
			t.Fatalf("kod %d: %s", w.Code, w.Body)
		}
	}
	if po := string(s.plikConfigu(t)); po != przed {
		t.Fatalf("plik się zmienił:\n%s", po)
	}
}

func TestPatchUsługiZmieniaKodyIInterwał(t *testing.T) {
	s, h, c := serwerSprawdzanie(t)
	w := wyślij(h, c, "PATCH", "/api/services",
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"2m","expect":[200,401]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("kod %d: %s", w.Code, w.Body)
	}
	u := usługaZPliku(t, s, "NAS")
	if u.Check.Interval != config.Duration(2*time.Minute) || !reflect.DeepEqual(u.Check.Expect, []int{200, 401}) || !u.Check.Insecure {
		t.Fatalf("%+v", u.Check)
	}
}

func TestPatchUsługiOdrzucaZłeSprawdzanie(t *testing.T) {
	_, h, c := serwerSprawdzanie(t)
	for _, ciało := range []string{
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"kiedyś"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","timeout":"-5s"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","interval":"500ms"}`,
		`{"group":"Infra","name":"NAS","url":"https://nas.home.arpa","expect":[200,9999]}`,
	} {
		if w := wyślij(h, c, "PATCH", "/api/services", ciało); w.Code != http.StatusBadRequest {
			t.Errorf("%s: kod %d", ciało, w.Code)
		}
	}
}
