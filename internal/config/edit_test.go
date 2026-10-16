package config

import (
	"errors"
	"strings"
	"testing"
)

// diffLines returns the indexes of lines that differ. Requires an equal number of lines.
func diffLines(t *testing.T, before, after string) []int {
	t.Helper()
	bLines, aLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(bLines) != len(aLines) {
		t.Fatalf("number of lines changed: %d → %d", len(bLines), len(aLines))
	}
	var out []int
	for i := range bLines {
		if bLines[i] != aLines[i] {
			out = append(out, i)
		}
	}
	return out
}

func TestSetServiceFieldChangesOnlyOneLine(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://10.0.0.5:8990"); err != nil {
		t.Fatal(err)
	}

	changed := diffLines(t, string(src), string(d.Bytes()))
	if len(changed) != 1 {
		t.Fatalf("changed lines = %v, want exactly 1", changed)
	}
	after := strings.Split(string(d.Bytes()), "\n")
	if !strings.Contains(after[changed[0]], "8990") {
		t.Fatalf("changed line = %q", after[changed[0]])
	}
}

func TestSetServiceFieldDoesNotTouchQuotesOrComment(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "name", "Sonarr v4"); err != nil {
		t.Fatal(err)
	}
	if got := string(d.Bytes()); !strings.Contains(got, `name: "Sonarr v4"          # cudzysłowy celowo, nie zdejmuj`) {
		t.Fatalf("quoting style or comment lost:\n%s", got)
	}
}

func TestSetServiceFieldQuotesWhenNeeded(t *testing.T) {
	cases := []struct{ value, want string }{
		{"Jellyfin 2", "Jellyfin 2"}, // unchanged, quoting unnecessary
		{"true", `"true"`},           // otherwise it would become a bool
		{"123", `"123"`},             // otherwise a number
		{"a: b", `"a: b"`},           // would break the mapping
		{"# hash", `"# hash"`},       // would become a comment
		{"", `""`},                   // empty value
	}
	for _, c := range cases {
		d, err := Parse(loadBasic(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetServiceField("Media", "Jellyfin", "name", c.value); err != nil {
			t.Fatalf("%q: %v", c.value, err)
		}
		if got := string(d.Bytes()); !strings.Contains(got, "name: "+c.want+"\n") {
			t.Errorf("for %q want `name: %s`, got a different line:\n%s", c.value, c.want, cutLineFrom(got, "Jellyfin"))
		}
		// the result must still parse
		if _, err := Load(d.Bytes()); err != nil {
			t.Errorf("after setting %q the file does not parse: %v", c.value, err)
		}
	}
}

func cutLineFrom(s, needle string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return "(nie znaleziono)"
}

func TestSetServiceFieldUnknownService(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	err = d.SetServiceField("Media", "Nie Ma Takiej", "url", "http://x")
	if !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("err = %v, want ErrServiceNotFound", err)
	}
	err = d.SetServiceField("Nie Ma Grupy", "Jellyfin", "url", "http://x")
	if !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}
}

// assertOriginalLinesUntouched checks that the result contains all lines
// of the original, in the same order — additions are allowed, changes are not.
func assertOriginalLinesUntouched(t *testing.T, before, after string) {
	t.Helper()
	bLines, aLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	j := 0
	for _, line := range bLines {
		for j < len(aLines) && aLines[j] != line {
			j++
		}
		if j == len(aLines) {
			t.Fatalf("a line vanished or changed: %q", line)
		}
		j++
	}
}

func TestAddServiceAppendsToGroupEnd(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	err = d.AddService("Sieć", Service{Name: "AdGuard", URL: "http://10.0.0.2:3000", Icon: "adguard-home"})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, d.Bytes())
	}
	if n := len(cfg.Groups[1].Services); n != 2 {
		t.Fatalf("services in group Sieć = %d, want 2\n%s", n, d.Bytes())
	}
	if cfg.Groups[1].Services[1].Name != "AdGuard" {
		t.Errorf("wrong name: %+v", cfg.Groups[1].Services[1])
	}
	assertOriginalLinesUntouched(t, string(src), string(d.Bytes()))
}

func TestAddServiceUsesNeighborIndent(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Media", Service{Name: "Radarr", URL: "http://x:7878"}); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "\n      - name: Radarr\n        url: http://x:7878\n") {
		t.Fatalf("bad indent:\n%s", out)
	}
	// the Media group separates services with a blank line — the new one should follow the same style
	if !strings.Contains(out, "<<: *defaults\n\n      - name: Radarr") {
		t.Errorf("blank line between services not preserved:\n%s", out)
	}
	// the comment at the end of the file must stay at the end
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "# Stopka: nic tu nie ma, ale komentarz ma zostać.") {
		t.Errorf("footer stopped being a footer:\n%s", out)
	}
}

func TestAddServiceRejectsDuplicate(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Media", Service{Name: "Jellyfin", URL: "http://x"}); err == nil {
		t.Fatal("want an error on duplicate name")
	}
}

func TestAddServiceToGroupWithoutServices(t *testing.T) {
	src := []byte("groups:\n  - name: Puste\n")
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Puste", Service{Name: "Nowa", URL: "http://n"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
	if len(cfg.Groups[0].Services) != 1 || cfg.Groups[0].Services[0].Name != "Nowa" {
		t.Fatalf("added wrongly:\n%s", d.Bytes())
	}
}

func TestRemoveServiceKeepsGroupComment(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveService("Media", "Jellyfin"); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if len(cfg.Groups[0].Services) != 1 || cfg.Groups[0].Services[0].Name != "Sonarr" {
		t.Fatalf("removed wrongly:\n%s", out)
	}
	if !strings.Contains(out, "# Grupa mediów — najczęściej używana, ma być pierwsza") {
		t.Errorf("group comment lost:\n%s", out)
	}
	if !strings.Contains(out, "# Stopka: nic tu nie ma, ale komentarz ma zostać.") {
		t.Errorf("footer lost:\n%s", out)
	}
	if strings.Contains(out, "Jellyfin") {
		t.Errorf("service did not disappear:\n%s", out)
	}
}

func TestRemoveLastServiceInGroup(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveService("Sieć", "Pi-hole"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
	if len(cfg.Groups) != 2 {
		t.Fatalf("group vanished:\n%s", d.Bytes())
	}
	if len(cfg.Groups[1].Services) != 0 {
		t.Fatalf("want an empty service list, got %d:\n%s", len(cfg.Groups[1].Services), d.Bytes())
	}
}

func TestAddAndRemoveGroup(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddGroup("Testy"); err != nil {
		t.Fatal(err)
	}
	if err := d.AddService("Testy", Service{Name: "Coś", URL: "http://cos"}); err != nil {
		t.Fatalf("adding to a new group: %v\n%s", err, d.Bytes())
	}
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, d.Bytes())
	}
	if len(cfg.Groups) != 3 || cfg.Groups[2].Name != "Testy" || len(cfg.Groups[2].Services) != 1 {
		t.Fatalf("group added wrongly:\n%s", d.Bytes())
	}
	if err := d.RemoveGroup("Testy"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(d.Bytes())
	if len(cfg.Groups) != 2 {
		t.Fatalf("group did not disappear:\n%s", d.Bytes())
	}
}

func TestRenameGroup(t *testing.T) {
	src := loadBasic(t)
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RenameGroup("Sieć", "Network"); err != nil {
		t.Fatal(err)
	}
	changed := diffLines(t, string(src), string(d.Bytes()))
	if len(changed) != 1 {
		t.Fatalf("changed lines = %v, want 1", changed)
	}
	cfg, _ := Load(d.Bytes())
	if cfg.Groups[1].Name != "Network" || len(cfg.Groups[1].Services) != 1 {
		t.Fatalf("wrong:\n%s", d.Bytes())
	}
}

func TestMoveServicePreservesCommentAndFields(t *testing.T) {
	d, err := Parse(loadBasic(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MoveService("Media", "Sonarr", "Sieć"); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	cfg, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if len(cfg.Groups[0].Services) != 1 || len(cfg.Groups[1].Services) != 2 {
		t.Fatalf("wrong service counts: %d / %d\n%s",
			len(cfg.Groups[0].Services), len(cfg.Groups[1].Services), out)
	}
	if cfg.Groups[1].Services[1].Name != "Sonarr" {
		t.Fatalf("not that service:\n%s", out)
	}
	// the comment from the entry's line travels with the service
	if !strings.Contains(out, "cudzysłowy celowo, nie zdejmuj") {
		t.Errorf("service comment lost:\n%s", out)
	}
	// merge-key too
	if strings.Count(out, "<<: *defaults") != 2 {
		t.Errorf("merge-key lost during the move:\n%s", out)
	}
	// indent in the new group matches the neighbour
	if !strings.Contains(out, "\n      - name: \"Sonarr\"") {
		t.Errorf("bad indent after the move:\n%s", out)
	}
}

func TestSetAppearanceDim(t *testing.T) {
	src := "appearance:\n  background:\n    preset: photo\n    image: t.jpg\n    blur: 10\ngroups: []\n"
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := d.SetAppearance(map[string]string{"dim": "70"}); err != nil {
		t.Fatalf("SetAppearance: %v", err)
	}
	c, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("Load after edit: %v", err)
	}
	if c.Appearance.Background.Dim != 70 {
		t.Fatalf("want dim 70, got %d", c.Appearance.Background.Dim)
	}
	// The splice must touch one line, not rewrite the block.
	if !strings.Contains(string(d.Bytes()), "blur: 10") {
		t.Fatalf("neighbouring keys must stay untouched:\n%s", d.Bytes())
	}
}

// An appearance block created from scratch must carry dim, otherwise the first save
// of the appearance would lose the dimming set in the same click.
func TestAddAppearanceCarriesDim(t *testing.T) {
	d, err := Parse([]byte("title: t\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := d.SetAppearance(map[string]string{"preset": "photo", "image": "t.jpg", "dim": "20"}); err != nil {
		t.Fatalf("SetAppearance: %v", err)
	}
	c, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("Load after edit: %v", err)
	}
	if c.Appearance.Background.Dim != 20 {
		t.Fatalf("want dim 20, got %d — file:\n%s", c.Appearance.Background.Dim, d.Bytes())
	}
}

func TestSetServiceFieldNameRejectsDuplicate(t *testing.T) {
	d, err := Parse([]byte(moveSrc))
	if err != nil {
		t.Fatal(err)
	}
	before := string(d.Bytes())
	if err := d.SetServiceField("Media", "A", "name", "B"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
	if string(d.Bytes()) != before {
		t.Error("a rejected rename changed the file")
	}
	// the same name in ANOTHER group is allowed, and a rename to its own name is a no-op
	if err := d.SetServiceField("Media", "A", "name", "D"); err != nil {
		t.Errorf("name from another group: %v", err)
	}
	if err := d.SetServiceField("Media", "B", "name", "B"); err != nil {
		t.Errorf("rename to itself: %v", err)
	}
}

func TestRenameGroupDuplicateIsErrDuplicate(t *testing.T) {
	d, _ := Parse([]byte(moveSrc))
	if err := d.RenameGroup("Media", "Sieć"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}
