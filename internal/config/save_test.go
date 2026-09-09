package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveJestAtomowyIZachowujeUprawnienia(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := wczytajBasic(t)
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
		t.Errorf("uprawnienia = %v, chcę 0640", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("został plik tymczasowy: %v", ents)
	}
	zapisane, _ := os.ReadFile(p)
	if string(zapisane) != string(d.Bytes()) {
		t.Error("na dysku jest co innego niż w dokumencie")
	}
}

func TestSaveOdmawiaGdyPlikZmienionyPodSpodem(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := wczytajBasic(t)
	os.WriteFile(p, src, 0o644)

	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// ktoś edytuje plik ręcznie w międzyczasie
	os.WriteFile(p, append(src, []byte("\n# ręczna edycja\n")...), 0o644)

	_ = d.SetServiceField("Media", "Sonarr", "url", "http://x:1")
	if err := d.Save(); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save = %v, chcę ErrChangedOnDisk", err)
	}
	// i nic nie nadpisał
	teraz, _ := os.ReadFile(p)
	if !contains(string(teraz), "# ręczna edycja") {
		t.Error("cudza zmiana została nadpisana")
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
