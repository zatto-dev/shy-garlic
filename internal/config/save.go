package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

// zamieńPlik to os.Rename — zmienna tylko po to, żeby test mógł zasymulować
// EBUSY / EXDEV, których na zwykłym dysku testowym nie da się wywołać.
var zamieńPlik = os.Rename

// utwórzTymczasowy to os.CreateTemp, z tego samego powodu (EACCES na katalogu).
var utwórzTymczasowy = os.CreateTemp

// Save zapisuje zmiany atomowo: nowy plik powstaje obok, jest zsynchronizowany
// na dysk i dopiero wtedy podmieniany. Przerwany zapis zostawia stary plik cały.
//
// Gdy atomowa podmiana jest niemożliwa — config zamontowany jako pojedynczy
// plik (rename → EBUSY), katalog bez prawa zapisu (EACCES), inny system plików
// (EXDEV) — zapisujemy w miejscu: obcięcie, zapis, fsync. To nie jest atomowe,
// więc strażnik ErrChangedOnDisk jest sprawdzany jeszcze raz tuż przed.
//
// Symlink do configu jest rozwiązywany: piszemy do celu, a link zostaje linkiem.
func (d *Document) Save() error {
	if d.path == "" {
		return errors.New("document has no path (use Open)")
	}
	cel := d.path
	if r, err := filepath.EvalSymlinks(d.path); err == nil {
		cel = r
	}

	if err := d.sprawdźOdcisk(cel); err != nil {
		return err
	}

	err := zapiszAtomowo(cel, d.src, d.mode)
	if err != nil && wartoZapisaćWMiejscu(err) {
		// ! ponowna kontrola: między pierwszą a drugą próbą ktoś mógł dotknąć pliku
		if err := d.sprawdźOdcisk(cel); err != nil {
			return err
		}
		err = zapiszWMiejscu(cel, d.src)
	}
	if err != nil {
		return err
	}
	d.hash = sha256.Sum256(d.src)
	return nil
}

func (d *Document) sprawdźOdcisk(ścieżka string) error {
	obecne, err := os.ReadFile(ścieżka)
	if err != nil {
		return err
	}
	if sha256.Sum256(obecne) != d.hash {
		return ErrChangedOnDisk
	}
	return nil
}

// wartoZapisaćWMiejscu — tylko błędy, które mówią „tu się nie da podmienić
// pliku". ENOSPC, EIO czy EROFS nie są powodem do fallbacku: zapis w miejscu
// też by się nie udał, a mógłby zostawić plik obcięty do połowy.
func wartoZapisaćWMiejscu(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func zapiszAtomowo(cel string, treść []byte, tryb os.FileMode) error {
	dir := filepath.Dir(cel)
	tmp, err := utwórzTymczasowy(dir, ".shygarlic-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	sprzątaj := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(treść); err != nil {
		sprzątaj()
		return err
	}
	if err := tmp.Sync(); err != nil {
		sprzątaj()
		return err
	}
	if err := tmp.Chmod(tryb); err != nil {
		sprzątaj()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := zamieńPlik(tmpName, cel); err != nil {
		os.Remove(tmpName)
		return err
	}

	// fsync katalogu — bez tego rename może nie przetrwać nagłego resetu
	if fd, err := os.Open(dir); err == nil {
		_ = fd.Sync()
		_ = fd.Close()
	}
	return nil
}

func zapiszWMiejscu(cel string, treść []byte) error {
	f, err := os.OpenFile(cel, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(treść); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Path zwraca ścieżkę pliku, jeśli dokument pochodzi z Open.
func (d *Document) Path() string { return d.path }

func (d *Document) String() string { return fmt.Sprintf("Document(%s, %d B)", d.path, len(d.src)) }
