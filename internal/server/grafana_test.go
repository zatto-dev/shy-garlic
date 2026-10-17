package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Grafana dashboard lives in the repo as a plain file; this test makes sure it is
// valid JSON, that the data source is a variable (nobody has "our" uid)
// and that every query uses metrics the panel really exposes.
func TestGrafanaDashboardIsValidAndUsesSourceVariable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "grafana", "shy-garlic.json"))
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
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("the dashboard is not valid JSON: %v", err)
	}
	// import inputs: data source + Prometheus job name (constant)
	inputs := map[string]string{}
	for _, in := range d.Inputs {
		inputs[in.Name] = in.Type
	}
	if len(d.Inputs) != 2 || inputs["DS_PROMETHEUS"] != "datasource" || inputs["VAR_JOB"] != "constant" {
		t.Errorf("__inputs: %+v", d.Inputs)
	}
	kinds := map[string]bool{}
	for _, p := range d.Panels {
		kinds[p.Type] = true
		if len(p.Targets) == 0 {
			continue // text panel — does not query any source
		}
		if p.Datasource.UID != "${DS_PROMETHEUS}" {
			t.Errorf("panel %q: datasource uid %q, want ${DS_PROMETHEUS}", p.Title, p.Datasource.UID)
		}
		for _, tg := range p.Targets {
			if tg.Datasource.UID != "${DS_PROMETHEUS}" {
				t.Errorf("panel %q: target z datasource %q", p.Title, tg.Datasource.UID)
			}
			if !strings.Contains(tg.Expr, "shyg_service_") && !strings.Contains(tg.Expr, "shyg_build_info") {
				t.Errorf("panel %q: query %q does not use shyg_* metrics", p.Title, tg.Expr)
			}
		}
	}
	for _, want := range []string{"stat", "table", "timeseries", "state-timeline"} {
		if !kinds[want] {
			t.Errorf("no panel of type %s", want)
		}
	}
	if strings.Contains(string(raw), `"uid": "`) {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, `"uid": "`) && !strings.Contains(line, "${DS_PROMETHEUS}") && !strings.Contains(line, `"shy-garlic"`) {
				t.Errorf("wpisany na sztywno uid: %s", strings.TrimSpace(line))
			}
		}
	}

	// every metric from the dashboard exists in the /metrics code
	code, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "server", "metrics.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"shyg_service_up", "shyg_service_degraded", "shyg_service_response_seconds", "shyg_service_availability_ratio", "shyg_service_last_check_timestamp_seconds", "shyg_build_info"} {
		if !strings.Contains(string(code), `"`+m+`"`) {
			t.Errorf("metric %s is not exposed", m)
		}
	}
}
