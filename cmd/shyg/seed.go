package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// seedConfig copies seedPath to cfgPath when cfgPath does not exist yet, and
// reports whether it did.
//
// It replaces the shell init container the Kubernetes manifests used: the
// default image is distroless and has no shell. An existing config is never
// touched — once the panel has written to it, it is the source of truth. The
// copy goes through a temporary file and os.Link, so a half-written file never
// appears under the real name and a file created in the meantime is not
// overwritten.
func seedConfig(cfgPath, seedPath string) (bool, error) {
	if filepath.Clean(cfgPath) == filepath.Clean(seedPath) {
		return false, nil // SHYG_CONFIG pointed at the seed itself: edit it in place
	}
	if _, err := os.Lstat(cfgPath); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}

	src, err := os.Open(seedPath)
	if err != nil {
		return false, fmt.Errorf("seed config: %w", err)
	}
	defer src.Close()

	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, fmt.Errorf("seed config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".seed-*.yaml")
	if err != nil {
		return false, fmt.Errorf("seed config: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return false, fmt.Errorf("seed config: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return false, fmt.Errorf("seed config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("seed config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("seed config: %w", err)
	}
	if err := os.Link(tmp.Name(), cfgPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil // created in the meantime — leave it alone
		}
		return false, fmt.Errorf("seed config: %w", err)
	}
	return true, nil
}
