package config

import (
	"os"
	"strings"
	"testing"
)

func loadAppearance(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/appearance.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// defaultAppearance — what AddAppearance writes when nobody asked for anything specific.
func defaultAppearance() Appearance {
	return Appearance{
		Background: Background{Preset: DefaultPreset, Blur: DefaultBlur, Tone: DefaultTone},
		View:       DefaultView,
	}
}

func TestAddAppearanceInsertsBeforeGroups(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddAppearance(defaultAppearance()); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())

	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "# Stopka: nic tu nie ma, ale komentarz ma zostać.") {
		t.Fatalf("block landed under the footer:\n%s", out)
	}
	if strings.Index(out, "appearance:") > strings.Index(out, "\ngroups:") {
		t.Fatalf("want the block BEFORE groups:\n%s", out)
	}
	assertOriginalLinesUntouched(t, string(src), out)

	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Preset != DefaultPreset || cfg.Appearance.View != DefaultView {
		t.Fatalf("loaded appearance = %+v", cfg.Appearance)
	}
}

func TestAddAppearanceDoesNotDuplicate(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := d.AddAppearance(defaultAppearance()); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if n := strings.Count(string(d.Bytes()), "appearance:"); n != 1 {
		t.Fatalf("appearance keys = %d, want exactly 1:\n%s", n, d.Bytes())
	}
}

func TestAddAppearanceLeavesCommentAboveGroups(t *testing.T) {
	src := []byte("title: X\n\n# Tu zaczynają się grupy.\ngroups:\n  - name: A\n    services: []\n")
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddAppearance(defaultAppearance()); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "# Tu zaczynają się grupy.\ngroups:") {
		t.Fatalf("comment detached from groups:\n%s", out)
	}
	if strings.Contains(out, "# Tu zaczynają się grupy.\nappearance:") {
		t.Fatalf("comment glued to appearance:\n%s", out)
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
}

func TestAddAppearanceAtEndWhenNoGroups(t *testing.T) {
	for _, src := range []string{"title: X\n", "title: X"} {
		d, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if err := d.AddAppearance(defaultAppearance()); err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		out := string(d.Bytes())
		if strings.Contains(out, "Xappearance") || strings.Contains(out, "X\nappearance") {
			t.Fatalf("block glued to the last line:\n%s", out)
		}
		cfg, err := Load(d.Bytes())
		if err != nil {
			t.Fatalf("%q does not parse: %v\n%s", src, err, out)
		}
		if cfg.Title != "X" || cfg.Appearance.Background.Preset != DefaultPreset {
			t.Fatalf("%q gave %+v\n%s", src, cfg.Appearance, out)
		}
	}
}

func TestSetAppearanceChangesOnlyOneLine(t *testing.T) {
	src := loadAppearance(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetAppearance(map[string]string{"preset": "fog"}); err != nil {
		t.Fatal(err)
	}
	changed := diffLines(t, string(src), string(d.Bytes()))
	if len(changed) != 1 {
		t.Fatalf("changed lines = %v, want exactly 1:\n%s", changed, d.Bytes())
	}
	line := strings.Split(string(d.Bytes()), "\n")[changed[0]]
	if !strings.Contains(line, "fog") || !strings.Contains(line, "# komentarz końca linii") {
		t.Fatalf("changed line = %q", line)
	}
}

func TestSetAppearanceWritesBlurWithoutQuotes(t *testing.T) {
	d, err := Parse(loadAppearance(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetAppearance(map[string]string{"blur": "24"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "blur: 24") || strings.Contains(out, `blur: "24"`) {
		t.Fatalf("want a bare number in `blur:`:\n%s", out)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Blur != 24 {
		t.Fatalf("Blur = %d, want 24", cfg.Appearance.Background.Blur)
	}
}

func TestSetPathRawRejectsValuesOutsideAllowlist(t *testing.T) {
	cases := []struct {
		value  string
		wantOK bool
	}{
		{"18", true}, {"true", true}, {"false", true}, {"0", true}, {"-3", true},
		{"1e9", false}, {"18 # x", false}, {"}", false}, {"", false}, {"a", false},
		{"1234567", false}, {"0x10", false},
	}
	for _, c := range cases {
		src := loadAppearance(t)
		d, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		err = d.SetPathRaw(c.value, "appearance", "background", "blur")
		if c.wantOK && err != nil {
			t.Errorf("%q: %v, want a write", c.value, err)
			continue
		}
		if !c.wantOK {
			if err == nil {
				t.Errorf("%q: want a refusal", c.value)
			}
			if string(d.Bytes()) != string(src) {
				t.Errorf("%q: rejected value touched the file", c.value)
			}
		}
	}
}

func TestSetAppearanceQuotesWhenNeeded(t *testing.T) {
	d, err := Parse(loadAppearance(t))
	if err != nil {
		t.Fatal(err)
	}
	// without quotes YAML would turn this into a comment, i.e. a null value
	if err := d.SetAppearance(map[string]string{"preset": "#101014"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), `preset: "#101014"`) {
		t.Fatalf("want a quoted value:\n%s", d.Bytes())
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
	// a second edit of the same line sees the quote in the original and keeps it
	if err := d.SetAppearance(map[string]string{"preset": "fog"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), `preset: "fog"`) {
		t.Fatalf("quoting style flickered:\n%s", d.Bytes())
	}
}

func TestAppearanceRoundTripPreservesAnchorsAndMergeKeys(t *testing.T) {
	src := loadAppearance(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetAppearance(map[string]string{"preset": "graphite", "blur": "0", "view": "list"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	for _, want := range []string{
		"# Nagłówek — musi przetrwać.",
		`title: "Homelab"        # cudzysłów celowo`,
		"defaults: &defaults",
		"<<: *defaults",
		"  - name: Sieć",
		"# Stopka: nic tu nie ma, ale komentarz ma zostać.",
		"# komentarz końca linii",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Preset != "graphite" || cfg.Appearance.View != "list" {
		t.Fatalf("appearance = %+v\n%s", cfg.Appearance, out)
	}
	// `blur: 0` is a deliberate choice (sharp wallpaper), not a missing value
	if cfg.Appearance.Background.Blur != 0 {
		t.Fatalf("Blur = %d, want 0 — zero must not be treated as missing", cfg.Appearance.Background.Blur)
	}
	if len(cfg.Groups) != 1 || len(cfg.Groups[0].Services) != 1 {
		t.Fatalf("groups fell apart: %+v", cfg.Groups)
	}
}

func TestSetAppearanceAppendsMissingKey(t *testing.T) {
	src := loadAppearance(t) // it has no `image:`
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetAppearance(map[string]string{"preset": "photo", "image": "moje-tlo.jpg"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if cfg.Appearance.Background.Image != "moje-tlo.jpg" || cfg.Appearance.Background.Preset != "photo" {
		t.Fatalf("appearance = %+v\n%s", cfg.Appearance, out)
	}
	assertOriginalLinesUntouched(t, strings.ReplaceAll(string(src), "preset: nebula", "preset: photo"), out)

	// going back to a preset deletes the line with the file name
	if err := d.SetAppearance(map[string]string{"preset": "fog", "image": ""}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Bytes()), "image:") {
		t.Fatalf("`image:` line remained:\n%s", d.Bytes())
	}
}

func TestSetAppearanceToleratesMergeKey(t *testing.T) {
	src := []byte("wspolne: &w\n  preset: fog\n\nappearance:\n  <<: *w\n\ngroups: []\n")
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	// a merge-key is invisible to key navigation — a missing path is not
	// a write error, it must only not bring the server down
	if err := d.SetAppearance(map[string]string{"preset": "dawn"}); err != nil {
		t.Fatalf("want tolerance, got: %v", err)
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
}

func TestRemoveAppearanceDeletesWholeBlock(t *testing.T) {
	d, err := Parse(loadAppearance(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAppearance(); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	for _, wantNot := range []string{"appearance:", "background:", "view: bento"} {
		if strings.Contains(out, wantNot) {
			t.Errorf("%q remained after removal:\n%s", wantNot, out)
		}
	}
	for _, want := range []string{"# Nagłówek — musi przetrwać.", "defaults: &defaults", "groups:", "# Stopka"} {
		if !strings.Contains(out, want) {
			t.Errorf("removal took too much — lost %q:\n%s", want, out)
		}
	}
	if _, err := Load(d.Bytes()); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
}

func TestLoadFillsAndClampsAppearance(t *testing.T) {
	cfg, err := Load([]byte("title: X\n"))
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Appearance
	if w.Background.Preset != "aurora" || w.Background.Blur != 10 ||
		w.Background.Tone != "dark" || w.View != "grid" || w.Background.Image != "" {
		t.Fatalf("default appearance = %+v", w)
	}

	junk := []byte("appearance:\n  background:\n    preset: <script>\n    image: ../../etc/passwd\n" +
		"    blur: 999\n    tone: xx\n  view: hocus\n")
	cfg, err = Load(junk)
	if err != nil {
		t.Fatalf("Load must not blow up on nonsense in the file: %v", err)
	}
	w = cfg.Appearance
	if !Allowed(w.Background.Preset, BackgroundPresets) || !Allowed(w.Background.Tone, BackgroundTones) ||
		!Allowed(w.View, Views) {
		t.Fatalf("value outside the allowlist came out of Load: %+v", w)
	}
	if w.Background.Blur < 0 || w.Background.Blur > MaxBlur {
		t.Errorf("Blur = %d, want range 0..%d", w.Background.Blur, MaxBlur)
	}
	if w.Background.Image != "" {
		t.Errorf("Image = %q, want empty — the name did not pass the regex", w.Background.Image)
	}
}

func TestRenderServiceWritesSizeOnlyWhenGiven(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Sieć", Service{Name: "Bez", URL: "http://bez"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(d.Bytes()), "size:") {
		t.Fatalf("empty size must not reach the file:\n%s", d.Bytes())
	}
	if err := d.AddService("Sieć", Service{Name: "Duży", URL: "http://duzy", Size: "lg"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(d.Bytes()), "size:"); n != 1 {
		t.Fatalf("`size:` lines = %d, want 1:\n%s", n, d.Bytes())
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
	services := cfg.Groups[1].Services
	if services[1].Size != DefaultSize {
		t.Errorf("without size want %q, got %q", DefaultSize, services[1].Size)
	}
	if services[2].Size != "lg" {
		t.Errorf("size = %q, want lg", services[2].Size)
	}
}

func TestSetServiceFieldAppendsMissingField(t *testing.T) {
	src := loadAppearance(t) // Router has neither an icon nor a size
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Sieć", "Router", "size", "sm"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
	if cfg.Groups[0].Services[0].Size != "sm" {
		t.Fatalf("size = %q, want sm:\n%s", cfg.Groups[0].Services[0].Size, d.Bytes())
	}
	assertOriginalLinesUntouched(t, string(src), string(d.Bytes()))
}

// Regression: a light preset with a missing or nonsensical tone must get a light
// tone. Before, it fell back to the constant "dark" — white text on a cream background.
func TestLoadTakesToneFromPreset(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		wantTone string
		wantPre  string
	}{
		{"dawn without a tone", "appearance:\n  background:\n    preset: dawn\n", "light", "dawn"},
		{"fog without a tone", "appearance:\n  background:\n    preset: fog\n", "light", "fog"},
		{"aurora without a tone", "appearance:\n  background:\n    preset: aurora\n", "dark", "aurora"},
		{"dawn with nonsense", "appearance:\n  background:\n    preset: dawn\n    tone: różowy\n", "light", "dawn"},
		{"explicit tone loses to the preset", "appearance:\n  background:\n    preset: dawn\n    tone: dark\n", "light", "dawn"},
		{"photo without a file returns to the preset and its tone", "appearance:\n  background:\n    preset: photo\n    tone: light\n", "dark", DefaultPreset},
		{"empty block", "appearance:\n  background: {}\n", "dark", DefaultPreset},
	}
	for _, p := range cases {
		t.Run(p.name, func(t *testing.T) {
			cfg, err := Load([]byte(p.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Appearance.Background.Tone; got != p.wantTone {
				t.Errorf("tone = %q, want %q", got, p.wantTone)
			}
			if got := cfg.Appearance.Background.Preset; got != p.wantPre {
				t.Errorf("preset = %q, want %q", got, p.wantPre)
			}
		})
	}
}

// Every preset except `photo` must have a tone — otherwise ToneFor will silently return
// the default and the regression will return when a new background is added.
func TestEveryPresetHasTone(t *testing.T) {
	for _, p := range BackgroundPresets {
		if p == PresetPhoto {
			continue
		}
		if _, ok := PresetTone[p]; !ok {
			t.Errorf("preset %q has no tone in PresetTone", p)
		}
	}
	for p := range PresetTone {
		if !Allowed(p, BackgroundPresets) {
			t.Errorf("PresetTone knows preset %q that is not in BackgroundPresets", p)
		}
	}
}
