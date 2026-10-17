package config

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func serviceWithCheck(check string) []byte {
	return []byte("groups:\n  - name: G\n    services:\n      - name: S\n        url: http://s\n        check:\n" + check)
}

// Hand-written intervals that used to reach time.NewTicker unchanged: a
// non-positive one panicked the process on every start.
func TestLoadReplacesOutOfRangeDurations(t *testing.T) {
	bad := []string{
		"-5s", "-1", "0.5s", "500ms", "1ns", "25h", "1e9", "1e999", "-1e999",
		".nan", ".inf", "-.inf", "NaN", "99999999999h", "-99999999999h", "9223372036854775807", "30d",
	}
	for _, v := range bad {
		for _, field := range []string{"interval", "timeout"} {
			t.Run(field+"="+v, func(t *testing.T) {
				cfg, err := Load(serviceWithCheck("          " + field + ": " + v + "\n"))
				if err != nil {
					// an unparseable spelling (like "30d") may still be a file error,
					// but never a value that slips through
					if v == "30d" {
						return
					}
					t.Fatalf("Load: %v", err)
				}
				c := cfg.Groups[0].Services[0].Check
				got, def := c.Interval.Duration(), DefaultInterval
				if field == "timeout" {
					got, def = c.Timeout.Duration(), DefaultTimeout
				}
				if got != def {
					t.Errorf("%s = %v, want the default %v", field, got, def)
				}
				if len(cfg.LoadWarnings) != 1 || cfg.LoadWarnings[0].Kind != KindCheck || cfg.LoadWarnings[0].Key != field {
					t.Errorf("warnings = %+v, want one KindCheck for %s", cfg.LoadWarnings, field)
				}
			})
		}
	}
}

func TestLoadKeepsDurationsInRange(t *testing.T) {
	for v, want := range map[string]time.Duration{"1s": time.Second, "90": 90 * time.Second, "1m": time.Minute, "24h": 24 * time.Hour, "1.5s": 1500 * time.Millisecond} {
		cfg, err := Load(serviceWithCheck("          interval: " + v + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if got := cfg.Groups[0].Services[0].Check.Interval.Duration(); got != want {
			t.Errorf("interval %s = %v, want %v", v, got, want)
		}
		if len(cfg.LoadWarnings) != 0 {
			t.Errorf("interval %s: unexpected warnings %+v", v, cfg.LoadWarnings)
		}
	}
}

func TestLoadSanitizesInheritedDurations(t *testing.T) {
	src := "defaults: &d\n  check: {interval: -3s}\ngroups:\n  - name: G\n    services:\n      - <<: *d\n        name: A\n        url: http://a\n      - <<: *d\n        name: B\n        url: http://b\n"
	cfg, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cfg.Groups[0].Services {
		if s.Check.Interval.Duration() != DefaultInterval {
			t.Errorf("%s: interval = %v", s.Name, s.Check.Interval)
		}
	}
	if len(cfg.LoadWarnings) != 2 {
		t.Errorf("warnings = %d, want one per service", len(cfg.LoadWarnings))
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"http://admin:hunter2@nas.lan:5000/x?y=1": "http://nas.lan:5000/x?y=1",
		"https://user@host/":                      "https://host/",
		"tcp://root:pw@10.0.0.1:22":               "tcp://10.0.0.1:22",
		"http://host/a@b":                         "http://host/a@b",
		"http://nas.lan":                          "http://nas.lan",
		"":                                        "",
		"http://us er:pw@host/":                   "http://host/", // unparseable: regexp fallback
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddressWarningsFlagCredentialsWithoutLeakingThem(t *testing.T) {
	cfg, err := Load([]byte("groups:\n  - name: G\n    services:\n      - name: S\n        url: http://admin:hunter2@nas.lan\n"))
	if err != nil {
		t.Fatal(err)
	}
	w := AddressWarnings(cfg)
	if len(w) != 1 || w[0].Kind != KindCredentials {
		t.Fatalf("warnings = %+v, want one KindCredentials", w)
	}
	if strings.Contains(w[0].Address, "hunter2") || strings.Contains(w[0].Address, "admin") {
		t.Errorf("warning carries the credentials: %q", w[0].Address)
	}
	// the invalid-address warning must not leak either
	cfg2, _ := Load([]byte("groups:\n  - name: G\n    services:\n      - name: S\n        url: ftp://admin:hunter2@nas.lan\n"))
	for _, x := range AddressWarnings(cfg2) {
		if strings.Contains(x.Address, "hunter2") {
			t.Errorf("address warning leaks: %q", x.Address)
		}
	}
}

// A group name with control characters broke the file: \r was written raw.
func TestGroupNamesWithControlCharactersRoundTrip(t *testing.T) {
	names := []string{"a\rb", "a\x01b", "a\x7fb", "a b", "a\u0085b", "tab\there", "q\"uote", "back\\slash", "a\x1b[31mred"}
	for _, name := range names {
		for _, style := range []string{"", `"`, `'`} {
			doc, err := Parse([]byte("groups:\n  - name: " + style + "First" + style + "\n    services: []\n"))
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.AddGroup(name); err != nil {
				t.Fatalf("AddGroup(%q): %v", name, err)
			}
			cfg, err := Load(doc.Bytes())
			if err != nil {
				t.Fatalf("AddGroup(%q) left an unloadable file: %v\n%q", name, err, doc.Bytes())
			}
			if len(cfg.Groups) != 2 || cfg.Groups[1].Name != name {
				t.Fatalf("AddGroup(%q) loaded as %+v", name, cfg.Groups)
			}
			if err := doc.RenameGroup(name, name+"!"); err != nil {
				t.Fatalf("RenameGroup(%q): %v", name, err)
			}
			cfg, _ = Load(doc.Bytes())
			if cfg.Groups[1].Name != name+"!" {
				t.Fatalf("rename of %q loaded as %q", name, cfg.Groups[1].Name)
			}
		}
	}
}

func TestQuoteWithEscapesEveryControlCharacter(t *testing.T) {
	for _, q := range []rune{'"', '\''} {
		got := quoteWith(q, "a\rb\x00c d")
		for _, r := range got {
			if isControl(r) {
				t.Errorf("quoteWith(%q) left a raw control character in %q", q, got)
				break
			}
		}
	}
}

func TestNestingLimit(t *testing.T) {
	for _, src := range []string{
		strings.Repeat("[", 10000),
		"groups: " + strings.Repeat("[", 10000) + strings.Repeat("]", 10000),
		strings.Repeat("{a: ", 10000),
		"- " + strings.Repeat("- ", 10000) + "x",
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, err := Load([]byte(src))
		_, err2 := Parse([]byte(src))
		runtime.ReadMemStats(&after)
		if err == nil || err2 == nil {
			t.Fatalf("nesting of 10000 levels was accepted (%.20q)", src)
		}
		if !strings.Contains(err.Error(), "nesting") {
			t.Errorf("error = %v, want a message about nesting", err)
		}
		if fe := ParseFileError(err); fe.Line != 1 {
			t.Errorf("error line = %d, want 1", fe.Line)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("rejecting took %v", d)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 20<<20 {
			t.Errorf("rejecting allocated %d MiB", alloc>>20)
		}
	}
}

func TestNestingLimitSparesRealFiles(t *testing.T) {
	var b strings.Builder
	b.WriteString("groups:\n")
	for i := 0; i < 50; i++ {
		b.WriteString("  - name: \"[[[[[[[[[[ not a list ]]]]]]]]]]\" # [[[[[[[[[[[[\n    services:\n      - name: 'it''s [[[['\n        url: http://x\n        description: |\n          [[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[\n")
	}
	if _, err := Load([]byte(b.String())); err != nil {
		t.Fatalf("real-looking file rejected: %v", err)
	}
	// 40 levels of flow nesting are still fine
	if err := checkNesting([]byte(strings.Repeat("[", 40) + strings.Repeat("]", 40))); err != nil {
		t.Fatalf("40 levels rejected: %v", err)
	}
	if err := checkNesting([]byte(strings.Repeat("[", 65))); err == nil {
		t.Fatal("65 levels accepted")
	}
}
