package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// SHYG-46: saving must survive a file mounted as a single bind-mount (rename
// → EBUSY), a directory without write permission (EACCES) and another file system (EXDEV), and
// a symlink to the config must not be replaced with a regular file.

func openForWrite(t *testing.T, p string) *Document {
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

// replaceRename sets the variable for the duration of the test.
func replaceRename(t *testing.T, f func(string, string) error) {
	t.Helper()
	oldName := replaceFile
	replaceFile = f
	t.Cleanup(func() { replaceFile = oldName })
}

func TestSavePreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dane", "prawdziwy.yaml")
	os.MkdirAll(filepath.Dir(target), 0o755)
	os.WriteFile(target, loadBasic(t), 0o640)
	link := filepath.Join(dir, "shygarlic.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	d := openForWrite(t, link)
	if err := d.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced with a regular file")
	}
	saved, _ := os.ReadFile(target)
	if string(saved) != string(d.Bytes()) {
		t.Error("the symlink target did not get the new content")
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("junk left in the target directory: %v", entries)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 { // data/ + link
		t.Errorf("files appeared in the symlink's directory: %v", entries)
	}
}

func TestSaveFallsBackToInPlaceWriteOnEBUSYAndEXDEV(t *testing.T) {
	for name, errno := range map[string]error{"EBUSY": syscall.EBUSY, "EXDEV": syscall.EXDEV} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "shygarlic.yaml")
			os.WriteFile(p, loadBasic(t), 0o640)
			before, _ := os.Stat(p)
			replaceRename(t, func(string, string) error {
				return &os.LinkError{Op: "rename", Err: errno}
			})

			d := openForWrite(t, p)
			if err := d.Save(); err != nil {
				t.Fatalf("Save should fall back to an in-place write: %v", err)
			}
			saved, _ := os.ReadFile(p)
			if string(saved) != string(d.Bytes()) {
				t.Error("content on disk ≠ document")
			}
			after, _ := os.Stat(p)
			if !os.SameFile(before, after) {
				t.Error("the file was replaced — with a bind-mount that is the only way for it to fail")
			}
			if after.Mode().Perm() != 0o640 {
				t.Errorf("permissions = %v", after.Mode().Perm())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("temp file left behind: %v", entries)
			}
			// another save from the same document: hash updated
			if err := d.SetServiceField("Media", "Sonarr", "url", "http://x:2"); err != nil {
				t.Fatal(err)
			}
			if err := d.Save(); err != nil {
				t.Errorf("second Save: %v", err)
			}
		})
	}
}

func TestSaveFallbackDoesNotBypassChangedOnDiskGuard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := loadBasic(t)
	os.WriteFile(p, src, 0o644)
	replaceRename(t, func(string, string) error {
		// someone edits the file exactly between the rename attempt and the in-place write
		os.WriteFile(p, append(append([]byte{}, src...), []byte("\n# cudza edycja\n")...), 0o644)
		return &os.LinkError{Op: "rename", Err: syscall.EBUSY}
	})

	d := openForWrite(t, p)
	if err := d.Save(); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("Save = %v, want ErrChangedOnDisk", err)
	}
	now, _ := os.ReadFile(p)
	if !contains(string(now), "# cudza edycja") {
		t.Error("fallback overwrote someone else's change")
	}
}

func TestSaveOtherRenameErrorDoesNotTriggerFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	src := loadBasic(t)
	os.WriteFile(p, src, 0o644)
	replaceRename(t, func(string, string) error {
		return &os.LinkError{Op: "rename", Err: syscall.ENOSPC}
	})
	d := openForWrite(t, p)
	if err := d.Save(); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Save = %v, want ENOSPC", err)
	}
	if now, _ := os.ReadFile(p); string(now) != string(src) {
		t.Error("file changed despite an error that is not a reason for a fallback")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestSaveDirectoryWithoutWriteEACCES(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "shygarlic.yaml")
	os.WriteFile(p, loadBasic(t), 0o644) // the file itself is writable
	d := openForWrite(t, p)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := d.Save(); err != nil {
		t.Fatalf("Save in a directory without write permission: %v", err)
	}
	if saved, _ := os.ReadFile(p); string(saved) != string(d.Bytes()) {
		t.Error("content did not reach the file")
	}
}

// An in-place write that fails halfway (ENOSPC/EIO) must not leave
// the user with a truncated file.
func TestInPlaceWriteRestoresOriginalAfterWriteError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	original := loadBasic(t)
	os.WriteFile(p, original, 0o640)
	d := openForWrite(t, p)
	replaceRename(t, func(string, string) error { return syscall.EBUSY }) // force the fallback

	oldValue := writeToFile
	writeToFile = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, syscall.ENOSPC
	}
	t.Cleanup(func() { writeToFile = oldValue })

	err := d.Save()
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err = %v, want ENOSPC", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(original) {
		t.Fatalf("file did not return to the original (%d B instead of %d B)", len(got), len(original))
	}
}

func TestInPlaceWriteOnFsyncErrorRestoresOriginal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	original := loadBasic(t)
	os.WriteFile(p, original, 0o640)
	d := openForWrite(t, p)
	replaceRename(t, func(string, string) error { return syscall.EBUSY })

	oldValue := syncFile
	syncFile = func(*os.File) error { return syscall.EIO }
	t.Cleanup(func() { syncFile = oldValue })

	if err := d.Save(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("err = %v, want EIO", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(original) {
		t.Fatal("file did not return to the original")
	}
}
