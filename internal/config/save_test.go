package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveIsAtomicAndPreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := loadBasic(t)
	if err := os.WriteFile(p, src, 0o640); err != nil {
		t.Fatal(err)
	}

	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://x:1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}

	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("permissions = %v, want 0640", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
	saved, _ := os.ReadFile(p)
	if string(saved) != string(d.Bytes()) {
		t.Error("what is on disk differs from the document")
	}
}

func TestSaveRefusesWhenFileChangedUnderneath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := loadBasic(t)
	os.WriteFile(p, src, 0o644)

	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// someone edits the file by hand in the meantime
	os.WriteFile(p, append(src, []byte("\n# ręczna edycja\n")...), 0o644)

	_ = d.SetServiceField("Media", "Sonarr", "url", "http://x:1")
	if err := d.Save(); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save = %v, want ErrChangedOnDisk", err)
	}
	// and it overwrote nothing
	now, _ := os.ReadFile(p)
	if !contains(string(now), "# ręczna edycja") {
		t.Error("someone else's change was overwritten")
	}
}

func contains(h, n string) bool { return len(h) >= len(n) && (len(n) == 0 || indexOf(h, n) >= 0) }

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
