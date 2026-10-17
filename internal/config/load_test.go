package config

import (
	"os"
	"slices"
	"testing"
	"time"
)

func TestLoadReadsGroupsAndServices(t *testing.T) {
	src, err := os.ReadFile("testdata/basic.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Title != "Homelab Demo" {
		t.Errorf("Title = %q, want %q", cfg.Title, "Homelab Demo")
	}
	if len(cfg.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(cfg.Groups))
	}
	if cfg.Groups[0].Name != "Media" || len(cfg.Groups[0].Services) != 2 {
		t.Errorf("group 0 = %+v", cfg.Groups[0])
	}

	// merge-key must be expanded on read
	sonarr := cfg.Groups[0].Services[1]
	if sonarr.Name != "Sonarr" {
		t.Errorf("name = %q", sonarr.Name)
	}
	if sonarr.Check.Interval.Duration() != 30*time.Second {
		t.Errorf("interval from <<: *defaults = %v, want 30s", sonarr.Check.Interval)
	}
	if !slices.Equal(sonarr.Check.Expect, []int{200, 301, 302, 401, 403}) {
		t.Errorf("expect = %v", sonarr.Check.Expect)
	}

	// an explicit check overrides defaults
	pihole := cfg.Groups[1].Services[0]
	if pihole.Check.Interval.Duration() != 10*time.Second {
		t.Errorf("pihole interval = %v, want 10s", pihole.Check.Interval)
	}
	if !slices.Equal(pihole.Check.Expect, []int{200}) {
		t.Errorf("pihole expect = %v, want [200]", pihole.Check.Expect)
	}
}

func TestLoadFillsDefaults(t *testing.T) {
	cfg, err := Load([]byte("groups:\n  - name: X\n    services:\n      - name: Y\n        url: http://y\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Groups[0].Services[0].Check
	if c.Interval.Duration() != 30*time.Second {
		t.Errorf("default interval = %v, want 30s", c.Interval)
	}
	if c.Timeout.Duration() != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", c.Timeout)
	}
	// ! This is the whole product thesis: 401/403 means "alive, requires login",
	// not "down". Competitors confuse the two.
	if !slices.Equal(c.Expect, []int{200, 301, 302, 401, 403}) {
		t.Errorf("default expect = %v", c.Expect)
	}
}

func TestDefaultThresholdAndDim(t *testing.T) {
	c, err := Load([]byte("title: t\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Notify.Threshold != DefaultThreshold {
		t.Fatalf("want threshold %d, got %d", DefaultThreshold, c.Notify.Threshold)
	}
	if c.Appearance.Background.Dim != DefaultDim {
		t.Fatalf("want dim %d, got %d", DefaultDim, c.Appearance.Background.Dim)
	}
}

// dim: 0 is a deliberate "no dimming", not a missing key. Exactly the same
// trap as with blur — zero is the type's zero value.
func TestDimZeroIsAChoice(t *testing.T) {
	c, err := Load([]byte("appearance:\n  background:\n    preset: photo\n    image: t.jpg\n    dim: 0\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Appearance.Background.Dim != 0 {
		t.Fatalf("dim: 0 must stay zero, got %d", c.Appearance.Background.Dim)
	}
}

// A comment is not a setting. If "key given" were checked with a regex over
// the file text, this case would yield dim: 0 instead of the default.
func TestDimInCommentIsNotGiven(t *testing.T) {
	c, _ := Load([]byte("appearance:\n  background:\n    preset: aurora\n    # dim: 0\ngroups: []\n"))
	if c.Appearance.Background.Dim != DefaultDim {
		t.Fatalf("commented-out dim must yield default %d, got %d", DefaultDim, c.Appearance.Background.Dim)
	}
}

func TestDimOutOfRangeIsClamped(t *testing.T) {
	c, _ := Load([]byte("appearance:\n  background:\n    dim: 900\ngroups: []\n"))
	if c.Appearance.Background.Dim != DefaultDim {
		t.Fatalf("out-of-range dim must return to the default, got %d", c.Appearance.Background.Dim)
	}
}

func TestThresholdBelowOneIsClamped(t *testing.T) {
	c, _ := Load([]byte("notify:\n  threshold: 0\ngroups: []\n"))
	if c.Notify.Threshold != DefaultThreshold {
		t.Fatalf("threshold 0 must return to the default, got %d", c.Notify.Threshold)
	}
}

// A missing discovery.docker key means "behave as before", i.e.
// suggest if the socket is available. Hence a pointer, not a bool.
func TestDiscoveryUnsetByDefault(t *testing.T) {
	c, _ := Load([]byte("groups: []\n"))
	if c.Discovery.Docker != nil {
		t.Fatalf("without the key it must be nil, got %v", *c.Discovery.Docker)
	}
	c2, _ := Load([]byte("discovery:\n  docker: false\ngroups: []\n"))
	if c2.Discovery.Docker == nil || *c2.Discovery.Docker {
		t.Fatal("docker: false must yield a pointer to false")
	}
}
