package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedConfigCopiesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.yaml")
	if err := os.WriteFile(seed, []byte("title: Seed\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "data", "config", "shygarlic.yaml")

	seeded, err := seedConfig(cfg, seed)
	if err != nil || !seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	got, err := os.ReadFile(cfg)
	if err != nil || string(got) != "title: Seed\n" {
		t.Fatalf("%q %v", got, err)
	}
	// the seed is read-only (a ConfigMap); the copy must stay writable for the editor
	if st, _ := os.Stat(cfg); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg), ".seed-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func TestSeedConfigNeverTouchesAnExistingConfig(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.yaml")
	cfg := filepath.Join(dir, "shygarlic.yaml")
	if err := os.WriteFile(seed, []byte("title: Seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("title: Mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seeded, err := seedConfig(cfg, seed)
	if err != nil || seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	if got, _ := os.ReadFile(cfg); string(got) != "title: Mine\n" {
		t.Fatalf("existing config changed: %q", got)
	}
}

func TestSeedConfigMissingSeedIsAnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := seedConfig(filepath.Join(dir, "shygarlic.yaml"), filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatal("want an error for a missing seed file")
	}
}
