package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrChangedOnDisk — ktoś (człowiek, Ansible, git pull) zmienił plik między
// naszym odczytem a zapisem. Nadpisanie skasowałoby cudzą pracę, więc odmawiamy.
var ErrChangedOnDisk = errors.New("config changed on disk since it was read")

// Open wczytuje plik do edycji i zapamiętuje jego odcisk oraz uprawnienia.
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

// Save zapisuje zmiany atomowo: nowy plik powstaje obok, jest zsynchronizowany
// na dysk i dopiero wtedy podmieniany. Przerwany zapis zostawia stary plik cały.
func (d *Document) Save() error {
	if d.path == "" {
		return errors.New("document has no path (use Open)")
	}

	obecne, err := os.ReadFile(d.path)
	if err != nil {
		return err
	}
	if sha256.Sum256(obecne) != d.hash {
		return ErrChangedOnDisk
	}

	dir := filepath.Dir(d.path)
	tmp, err := os.CreateTemp(dir, ".shygarlic-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	sprzątaj := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(d.src); err != nil {
		sprzątaj()
		return err
	}
	if err := tmp.Sync(); err != nil {
		sprzątaj()
		return err
	}
	if err := tmp.Chmod(d.mode); err != nil {
		sprzątaj()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, d.path); err != nil {
		os.Remove(tmpName)
		return err
	}

	// fsync katalogu — bez tego rename może nie przetrwać nagłego resetu
	if fd, err := os.Open(dir); err == nil {
		_ = fd.Sync()
		_ = fd.Close()
	}

	d.hash = sha256.Sum256(d.src)
	return nil
}

// Path zwraca ścieżkę pliku, jeśli dokument pochodzi z Open.
func (d *Document) Path() string { return d.path }

func (d *Document) String() string { return fmt.Sprintf("Document(%s, %d B)", d.path, len(d.src)) }
