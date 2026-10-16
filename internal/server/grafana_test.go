package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dashboard do Grafany leży w repo jako zwykły plik; ten test pilnuje, że jest
// poprawnym JSON-em, że źródło danych to zmienna (nikt nie ma „naszego" uid-a)
// i że każde zapytanie używa metryk, które panel naprawdę wystawia.
func TestDashboardGrafanyJestPoprawnyIUżywaZmiennejŹródła(t *testing.T) {
	surowy, err := os.ReadFile(filepath.Join(korzeńRepo(t), "deploy", "grafana", "shy-garlic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Inputs []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"__inputs"`
		Panels []struct {
			Type       string `json:"type"`
			Title      string `json:"title"`
			Datasource struct {
				UID string `json:"uid"`
			} `json:"datasource"`
			Targets []struct {
				Expr       string `json:"expr"`
				Datasource struct {
					UID string `json:"uid"`
				} `json:"datasource"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(surowy, &d); err != nil {
		t.Fatalf("dashboard nie jest poprawnym JSON-em: %v", err)
	}
	if len(d.Inputs) != 1 || d.Inputs[0].Name != "DS_PROMETHEUS" || d.Inputs[0].Type != "datasource" {
		t.Errorf("__inputs: %+v", d.Inputs)
	}
	typy := map[string]bool{}
	for _, p := range d.Panels {
		typy[p.Type] = true
		if p.Datasource.UID != "${DS_PROMETHEUS}" {
			t.Errorf("panel %q: datasource uid %q, chcę ${DS_PROMETHEUS}", p.Title, p.Datasource.UID)
		}
		for _, tg := range p.Targets {
			if tg.Datasource.UID != "${DS_PROMETHEUS}" {
				t.Errorf("panel %q: target z datasource %q", p.Title, tg.Datasource.UID)
			}
			if !strings.Contains(tg.Expr, "shyg_service_") {
				t.Errorf("panel %q: zapytanie %q nie używa metryk shyg_service_*", p.Title, tg.Expr)
			}
		}
	}
	for _, chcę := range []string{"stat", "table", "timeseries", "state-timeline"} {
		if !typy[chcę] {
			t.Errorf("brak panelu typu %s", chcę)
		}
	}
	if strings.Contains(string(surowy), `"uid": "`) {
		for _, linia := range strings.Split(string(surowy), "\n") {
			if strings.Contains(linia, `"uid": "`) && !strings.Contains(linia, "${DS_PROMETHEUS}") && !strings.Contains(linia, `"shy-garlic"`) {
				t.Errorf("wpisany na sztywno uid: %s", strings.TrimSpace(linia))
			}
		}
	}

	// każda metryka z dashboardu istnieje w kodzie /metrics
	kod, err := os.ReadFile(filepath.Join(korzeńRepo(t), "internal", "server", "metryki.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"shyg_service_up", "shyg_service_degraded", "shyg_service_response_seconds", "shyg_service_availability_ratio"} {
		if !strings.Contains(string(kod), `"`+m+`"`) {
			t.Errorf("metryka %s nie jest wystawiana", m)
		}
	}
}
