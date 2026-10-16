package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// SHYG-46: zapis ma przeżyć plik zamontowany jako pojedynczy bind-mount (rename
// → EBUSY), katalog bez prawa zapisu (EACCES) i inny system plików (EXDEV), a
// symlink do configu nie może zostać podmieniony na zwykły plik.

func otwórzDoZapisu(t *testing.T, p string) *Document {
	t.Helper()
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetServiceField("Media", "Sonarr", "url", "http://x:1"); err != nil {
		t.Fatal(err)
	}
	return d
}

// podmień ustawia zmienną na czas testu.
func podmieńRename(t *testing.T, f func(string, string) error) {
	t.Helper()
	stara := zamieńPlik
	zamieńPlik = f
	t.Cleanup(func() { zamieńPlik = stara })
}

func TestSaveZachowujeSymlink(t *testing.T) {
	kat := t.TempDir()
	cel := filepath.Join(kat, "dane", "prawdziwy.yaml")
	os.MkdirAll(filepath.Dir(cel), 0o755)
	os.WriteFile(cel, wczytajBasic(t), 0o640)
	link := filepath.Join(kat, "shygarlic.yaml")
	if err := os.Symlink(cel, link); err != nil {
		t.Fatal(err)
	}

	d := otwórzDoZapisu(t, link)
	if err := d.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink został podmieniony na zwykły plik")
	}
	zapisane, _ := os.ReadFile(cel)
	if string(zapisane) != string(d.Bytes()) {
		t.Error("cel symlinka nie dostał nowej treści")
	}
	ents, _ := os.ReadDir(filepath.Dir(cel))
	if len(ents) != 1 {
		t.Errorf("w katalogu celu został śmieć: %v", ents)
	}
	if ents, _ := os.ReadDir(kat); len(ents) != 2 { // dane/ + link
		t.Errorf("w katalogu symlinka powstały pliki: %v", ents)
	}
}

func TestSaveFallbackNaZapisWMiejscuPrzyEBUSYIEXDEV(t *testing.T) {
	for nazwa, errno := range map[string]error{"EBUSY": syscall.EBUSY, "EXDEV": syscall.EXDEV} {
		t.Run(nazwa, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "shygarlic.yaml")
			os.WriteFile(p, wczytajBasic(t), 0o640)
			przed, _ := os.Stat(p)
			podmieńRename(t, func(string, string) error {
				return &os.LinkError{Op: "rename", Err: errno}
			})

			d := otwórzDoZapisu(t, p)
			if err := d.Save(); err != nil {
				t.Fatalf("Save powinien spaść na zapis w miejscu: %v", err)
			}
			zapisane, _ := os.ReadFile(p)
			if string(zapisane) != string(d.Bytes()) {
				t.Error("treść na dysku ≠ dokument")
			}
			po, _ := os.Stat(p)
			if !os.SameFile(przed, po) {
				t.Error("plik został podmieniony — przy bind-mouncie to jedyny sposób, żeby się nie udało")
			}
			if po.Mode().Perm() != 0o640 {
				t.Errorf("uprawnienia = %v", po.Mode().Perm())
			}
			if ents, _ := os.ReadDir(dir); len(ents) != 1 {
				t.Errorf("został plik tymczasowy: %v", ents)
			}
			// kolejny zapis z tego samego dokumentu: hash zaktualizowany
			if err := d.SetServiceField("Media", "Sonarr", "url", "http://x:2"); err != nil {
				t.Fatal(err)
			}
			if err := d.Save(); err != nil {
				t.Errorf("drugi Save: %v", err)
			}
		})
	}
}

func TestSaveFallbackNieOminaStrażnikaZmianNaDysku(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := wczytajBasic(t)
	os.WriteFile(p, src, 0o644)
	podmieńRename(t, func(string, string) error {
		// ktoś edytuje plik dokładnie między próbą rename a zapisem w miejscu
		os.WriteFile(p, append(append([]byte{}, src...), []byte("\n# cudza edycja\n")...), 0o644)
		return &os.LinkError{Op: "rename", Err: syscall.EBUSY}
	})

	d := otwórzDoZapisu(t, p)
	if err := d.Save(); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save = %v, chcę ErrChangedOnDisk", err)
	}
	teraz, _ := os.ReadFile(p)
	if !contains(string(teraz), "# cudza edycja") {
		t.Error("fallback nadpisał cudzą zmianę")
	}
}

func TestSaveInnyBłądRenameNieUruchamiaFallbacku(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := wczytajBasic(t)
	os.WriteFile(p, src, 0o644)
	podmieńRename(t, func(string, string) error {
		return &os.LinkError{Op: "rename", Err: syscall.ENOSPC}
	})
	d := otwórzDoZapisu(t, p)
	if err := d.Save(); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Save = %v, chcę ENOSPC", err)
	}
	if teraz, _ := os.ReadFile(p); string(teraz) != string(src) {
		t.Error("plik zmieniony mimo błędu, który nie jest powodem do fallbacku")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("został plik tymczasowy: %v", ents)
	}
}

func TestSaveKatalogBezZapisuEACCES(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignoruje uprawnienia katalogu")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	os.WriteFile(p, wczytajBasic(t), 0o644) // sam plik zapisywalny
	d := otwórzDoZapisu(t, p)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := d.Save(); err != nil {
		t.Fatalf("Save w katalogu bez zapisu: %v", err)
	}
	if zapisane, _ := os.ReadFile(p); string(zapisane) != string(d.Bytes()) {
		t.Error("treść nie trafiła do pliku")
	}
}

// Zapis w miejscu, który pada w połowie (ENOSPC/EIO), nie może zostawić
// użytkownikowi okrojonego pliku.
func TestZapisWMiejscuPrzywracaOryginałPoBłędzieZapisu(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	oryginał := wczytajBasic(t)
	os.WriteFile(p, oryginał, 0o640)
	d := otwórzDoZapisu(t, p)
	podmieńRename(t, func(string, string) error { return syscall.EBUSY }) // wymuś fallback

	stary := piszDoPliku
	piszDoPliku = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, syscall.ENOSPC
	}
	t.Cleanup(func() { piszDoPliku = stary })

	err := d.Save()
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err = %v, chcę ENOSPC", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(oryginał) {
		t.Fatalf("plik nie wrócił do oryginału (%d B zamiast %d B)", len(got), len(oryginał))
	}
}

func TestZapisWMiejscuPrzyBłędzieFsyncPrzywracaOryginał(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	oryginał := wczytajBasic(t)
	os.WriteFile(p, oryginał, 0o640)
	d := otwórzDoZapisu(t, p)
	podmieńRename(t, func(string, string) error { return syscall.EBUSY })

	stary := synchronizujPlik
	synchronizujPlik = func(*os.File) error { return syscall.EIO }
	t.Cleanup(func() { synchronizujPlik = stary })

	if err := d.Save(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("err = %v, chcę EIO", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(oryginał) {
		t.Fatal("plik nie wrócił do oryginału")
	}
}
