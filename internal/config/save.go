package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrChangedOnDisk — someone (a human, Ansible, git pull) changed the file between
// our read and write. Overwriting would erase someone else's work, so we refuse.
var ErrChangedOnDisk = errors.New("config changed on disk since it was read")

// Open loads the file for editing and remembers its fingerprint and permissions.
func Open(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	d.path = path
	d.hash = sha256.Sum256(raw)
	d.mode = fi.Mode().Perm()
	return d, nil
}

// replaceFile is os.Rename — a variable only so a test can simulate
// EBUSY / EXDEV, which cannot be triggered on an ordinary test disk.
var replaceFile = os.Rename

// createTemp is os.CreateTemp, for the same reason (EACCES on the directory).
var createTemp = os.CreateTemp

// OpenRaw loads the file WITHOUT parsing YAML. It serves only for repair:
// Open refuses a broken file, and the raw YAML editor (ReplaceAll) must be able to
// replace it with correct content. Every structural operation (AddService etc.)
// on such a document is disallowed until the first successful ReplaceAll.
func OpenRaw(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	src, crlf := normalizeLineEndings(raw)
	d := &Document{src: src, crlf: crlf, path: path, hash: sha256.Sum256(raw), mode: fi.Mode().Perm()}
	_ = d.reparse() // a valid file gets an AST; a broken one stays without
	return d, nil
}

// Save writes the changes atomically: the new file is created alongside, synced
// to disk and only then swapped in. An interrupted write leaves the old file intact.
//
// When an atomic swap is impossible — config mounted as a single
// file (rename → EBUSY), directory without write permission (EACCES), another file system
// (EXDEV) — we write in place: truncate, write, fsync. That is not atomic,
// so the ErrChangedOnDisk guard is checked once more right before.
//
// A symlink to the config is resolved: we write to the target, and the link stays a link.
func (d *Document) Save() error {
	if d.path == "" {
		return errors.New("document has no path (use Open)")
	}
	target := d.path
	if r, err := filepath.EvalSymlinks(d.path); err == nil {
		target = r
	}

	if err := d.verifyFingerprint(target); err != nil {
		return err
	}

	content := d.Bytes()
	err := writeAtomically(target, content, d.mode)
	if err != nil && shouldWriteInPlace(err) {
		// ! second check: between the first and second attempt someone may have touched the file
		if err := d.verifyFingerprint(target); err != nil {
			return err
		}
		err = writeInPlace(target, content)
	}
	if err != nil {
		return err
	}
	d.hash = sha256.Sum256(content)
	return nil
}

func (d *Document) verifyFingerprint(path string) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(current) != d.hash {
		return ErrChangedOnDisk
	}
	return nil
}

// shouldWriteInPlace — only errors that say "the file cannot be replaced
// here". ENOSPC, EIO or EROFS are not a reason for the fallback: writing in place
// would fail too, and could leave the file truncated halfway.
func shouldWriteInPlace(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func writeAtomically(target string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := createTemp(dir, ".shygarlic-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := replaceFile(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}

	// fsync of the directory — without it the rename may not survive a sudden reset
	if fd, err := os.Open(dir); err == nil {
		_ = fd.Sync()
		_ = fd.Close()
	}
	return nil
}

// writeToFile is f.Write — a variable only so a test can simulate
// ENOSPC / EIO in the middle of a write.
var writeToFile = func(f *os.File, content []byte) (int, error) { return f.Write(content) }

// syncFile is f.Sync, for the same reason.
var syncFile = func(f *os.File) error { return f.Sync() }

// writeInPlace: truncate, write, fsync. An error after truncation would leave the
// user's file cut short, so we read the original up front and on failure try to
// restore it.
func writeInPlace(target string, content []byte) error {
	original, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := writeToFile(f, content); err != nil {
		return restoreFile(f, original, err)
	}
	if err := syncFile(f); err != nil {
		return restoreFile(f, original, err)
	}
	return f.Close()
}

// restoreFile restores the original after a failed in-place write and closes the file.
// It returns the write error, supplemented with whether the file returned to its state
// before the change. The restoring write goes directly (not through writeToFile).
func restoreFile(f *os.File, original []byte, cause error) error {
	defer f.Close()
	if err := f.Truncate(0); err == nil {
		if _, err := f.WriteAt(original, 0); err == nil {
			if err := f.Sync(); err == nil {
				return fmt.Errorf("write in place failed (%w); the original file was restored", cause)
			}
		}
	}
	return fmt.Errorf("write in place failed (%w) and the file could not be restored; "+
		"the last good version is in the panel history", cause)
}

// Path returns the file path, if the document comes from Open.
func (d *Document) Path() string { return d.path }

func (d *Document) String() string { return fmt.Sprintf("Document(%s, %d B)", d.path, len(d.src)) }
