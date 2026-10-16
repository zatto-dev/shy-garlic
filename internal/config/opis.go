package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
)

// Opis i tagi usługi (SHYG-47) — oba pola opcjonalne.
//
// Limity są po stronie zapisu (API), nie odczytu: ręcznie wpisany plik ma się
// wczytać zawsze, a CSS i tak przycina to, co się nie mieści.
const (
	MaksOpisu = 200 // znaków, nie bajtów
	MaksTagów = 12
	MaksTagu  = 32
)

var (
	ErrOpisDługi = errors.New("description is too long")
	ErrOpisZnaki = errors.New("description contains control characters")
	ErrTagiZły   = errors.New("invalid tags")

	// ErrPoleDziedziczone — wartość przychodzi z `<<: *anchor`, a lokalny klucz
	// obok merge'a to dla parsera „duplicate key". Zmiana wymaga ręcznej edycji.
	ErrPoleDziedziczone = errors.New("the value is inherited through a merge key (<<) — edit it in the file")
)

// Tagi to lista etykiet usługi. Osobny typ, bo `tags: media` (jeden tekst
// zamiast listy) jest tak naturalnym zapisem, że nie wolno go karać błędem
// wczytania całego pliku.
type Tagi []string

// UnmarshalYAML przyjmuje listę albo pojedynczy tekst.
func (t *Tagi) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var lista []string
	if err := unmarshal(&lista); err == nil {
		*t = Tagi(lista)
		return nil
	}
	var jeden string
	if err := unmarshal(&jeden); err != nil {
		return err
	}
	*t = Tagi{jeden}
	return nil
}

func znakSterujący(r rune) bool {
	return unicode.IsControl(r) || r == ' ' || r == ' '
}

// NormalizujOpis przycina i sprawdza opis. Pusty wynik jest poprawny i znaczy
// „usuń opis". Opis to jedna linia, bez znaków sterujących.
func NormalizujOpis(v string) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > MaksOpisu {
		return "", ErrOpisDługi
	}
	for _, r := range v {
		if znakSterujący(r) {
			return "", ErrOpisZnaki
		}
	}
	// collapse runs of spaces the same way Load does, so what is written is
	// exactly what reads back
	return strings.Join(strings.Fields(v), " "), nil
}

// czyśćTagi usuwa puste i powtórzone (bez względu na wielkość liter) tagi,
// zachowując kolejność. Nic nie waliduje — patrz NormalizujTagi.
func czyśćTagi(t Tagi) Tagi {
	var out Tagi
	widziane := map[string]bool{}
	for _, x := range t {
		x = strings.TrimSpace(x)
		k := strings.ToLower(x)
		if x == "" || widziane[k] {
			continue
		}
		widziane[k] = true
		out = append(out, x)
	}
	return out
}

// NormalizujTagi czyści i sprawdza tagi przysłane do zapisu: do MaksTagów
// sztuk, każdy do MaksTagu znaków i bez znaków sterujących.
func NormalizujTagi(t []string) ([]string, error) {
	out := czyśćTagi(Tagi(t))
	if len(out) > MaksTagów {
		return nil, fmt.Errorf("%w: at most %d", ErrTagiZły, MaksTagów)
	}
	for _, x := range out {
		if utf8.RuneCountInString(x) > MaksTagu {
			return nil, fmt.Errorf("%w: %q is longer than %d", ErrTagiZły, x, MaksTagu)
		}
		for _, r := range x {
			if znakSterujący(r) {
				return nil, fmt.Errorf("%w: control characters", ErrTagiZły)
			}
		}
	}
	return []string(out), nil
}

// ── zapis ────────────────────────────────────────────────────────────────────

// UstawOpisUsługi ustawia, podmienia albo (pusty opis) usuwa `description`.
// Cytowanie i komentarz w linii zostają, jak przy SetServiceField.
func (d *Document) UstawOpisUsługi(grupa, usługa, opis string) error {
	opis, err := NormalizujOpis(opis)
	if err != nil {
		return err
	}
	maOpis := func(s Service) bool { return s.Description != "" }
	if err := d.odmówGdyDziedziczone(grupa, usługa, "description", maOpis); err != nil {
		return err
	}
	prev := d.src
	if opis != "" {
		err = d.SetServiceField(grupa, usługa, "description", opis)
	} else {
		err = d.usuńPoleUsługi(grupa, usługa, "description")
	}
	if err != nil {
		return err
	}
	return d.verifyService(prev, grupa, usługa, func(s Service) bool { return s.Description == opis })
}

// UstawTagiUsługi ustawia, podmienia albo (pusta lista) usuwa `tags`.
//
// Styl zapisu: istniejący klucz zostaje w swoim stylu (flow `[a, b]` albo
// lista blokowa); nowy dostaje styl, w jakim plik już pisze tagi, a gdy ich
// jeszcze nie ma — flow.
func (d *Document) UstawTagiUsługi(grupa, usługa string, tagi []string) error {
	prev := d.src
	if err := d.setServiceTags(grupa, usługa, tagi); err != nil {
		return err
	}
	want, _ := NormalizujTagi(tagi) // validated by setServiceTags
	return d.verifyService(prev, grupa, usługa, func(s Service) bool {
		return slices.Equal([]string(s.Tags), want)
	})
}

func (d *Document) setServiceTags(grupa, usługa string, tagi []string) error {
	tagi, err := NormalizujTagi(tagi)
	if err != nil {
		return err
	}
	maTagi := func(s Service) bool { return len(s.Tags) > 0 }
	if err := d.odmówGdyDziedziczone(grupa, usługa, "tags", maTagi); err != nil {
		return err
	}
	if len(tagi) == 0 {
		return d.usuńPoleUsługi(grupa, usługa, "tags")
	}
	gi, si, err := d.findService(grupa, usługa)
	if err != nil {
		return err
	}
	ścieżka := []any{"groups", gi, "services", si}
	n, err := d.node(append(append([]any{}, ścieżka...), "tags")...)
	if errors.Is(err, ErrPathNotFound) {
		if d.plikPiszeTagiBlokowo() {
			return d.wstawTagiBlokowo(tagi, ścieżka...)
		}
		return d.wstawKlucz("tags", renderTagiFlow(tagi), ścieżka...)
	}
	if err != nil {
		return err
	}

	if seq, ok := unwrap(n).(*ast.SequenceNode); ok && !seq.IsFlowStyle && len(seq.Values) > 0 {
		return d.podmieńTagiBlokowe(seq, tagi)
	}
	// flow, skalar (`tags: media`) albo alias — wartość mieści się w linii klucza
	start, end, err := d.valueRange(append(append([]any{}, ścieżka...), "tags")...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(renderTagiFlow(tagi)))
}

// odmówGdyDziedziczone zwraca ErrPoleDziedziczone, gdy usługa nie ma własnego
// klucza, a mimo to pole ma wartość — czyli dostaje je z merge-key. Zapis
// lokalnego klucza obok merge'a dałby plik, którego parser nie wczyta.
func (d *Document) odmówGdyDziedziczone(grupa, usługa, klucz string, maWartość func(Service) bool) error {
	gi, si, err := d.findService(grupa, usługa)
	if err != nil {
		return err
	}
	if _, err := d.node("groups", gi, "services", si, klucz); err == nil {
		return nil // klucz lokalny — to jego dotyczy zmiana
	}
	cfg, err := Load(d.src)
	if err != nil {
		return nil
	}
	for _, g := range cfg.Groups {
		if g.Name != grupa {
			continue
		}
		for _, s := range g.Services {
			if s.Name == usługa && maWartość(s) {
				return ErrPoleDziedziczone
			}
		}
	}
	return nil
}

func (d *Document) usuńPoleUsługi(grupa, usługa, klucz string) error {
	gi, si, err := d.findService(grupa, usługa)
	if err != nil {
		return err
	}
	return d.usuńPoleWpisu(klucz, "groups", gi, "services", si)
}

// usuńPoleWpisu to usuńKlucz, który radzi sobie też z kluczem stojącym zaraz po
// myślniku (`- description: x`) — sam usuńKlucz zabrałby wtedy myślnik, a
// z nim cały element listy.
func (d *Document) usuńPoleWpisu(klucz string, ścieżka ...any) error {
	n, err := d.node(ścieżka...)
	if err != nil {
		return nil
	}
	off, err := d.offsetKlucza(n, klucz)
	if err != nil {
		return nil // nie ma lokalnego klucza — nie ma czego kasować
	}
	kolumna, _ := d.zakresWpisu(off)
	if d.lineIndent(off) == kolumna {
		return d.usuńKlucz(klucz, ścieżka...)
	}

	// Pierwszy klucz po myślniku: zdejmujemy go razem z wartością i podciągamy
	// następny klucz pod myślnik.
	koniec := d.blockEnd(off, kolumna)
	nast := koniec
	for nast < len(d.src) && strings.TrimSpace(string(d.src[nast:d.lineEnd(nast)])) == "" {
		nast = d.lineEnd(nast) + 1
	}
	if nast >= len(d.src) || d.lineIndent(nast) != kolumna {
		return fmt.Errorf("cannot remove %q: it is the only key of its entry", klucz)
	}
	return d.replaceRange(off, nast+kolumna, nil)
}

// ── tagi: pomocnicze ─────────────────────────────────────────────────────────

// cytujTagFlow — wewnątrz listy flow przecinek i nawiasy też są składnią.
func cytujTagFlow(v string) string {
	if strings.ContainsAny(v, ",[]{}") || needsQuoting(v) {
		return quoteWith('"', v)
	}
	return v
}

func renderTagiFlow(tagi []string) string {
	części := make([]string, len(tagi))
	for i, t := range tagi {
		części[i] = cytujTagFlow(t)
	}
	return "[" + strings.Join(części, ", ") + "]"
}

func renderTagiBlok(tagi []string, myślnik int) string {
	var b strings.Builder
	for _, t := range tagi {
		b.WriteString(strings.Repeat(" ", myślnik) + "- " + quoteLike(nil, t) + "\n")
	}
	return b.String()
}

// plikPiszeTagiBlokowo: czy pierwsza lista tagów znaleziona w pliku jest
// blokowa. Brak tagów w pliku = flow.
func (d *Document) plikPiszeTagiBlokowo() bool {
	grupy, ok := d.sekwencja("groups")
	if !ok {
		return false
	}
	for gi := range grupy.Values {
		usługi, ok := d.sekwencja("groups", gi, "services")
		if !ok {
			continue
		}
		for si := range usługi.Values {
			n, err := d.node("groups", gi, "services", si, "tags")
			if err != nil {
				continue
			}
			if seq, ok := unwrap(n).(*ast.SequenceNode); ok {
				return !seq.IsFlowStyle
			}
		}
	}
	return false
}

func (d *Document) wstawTagiBlokowo(tagi []string, ścieżka ...any) error {
	kolumna, wstaw, err := d.miejsceWstawienia(ścieżka...)
	if err != nil {
		return err
	}
	blok := strings.Repeat(" ", kolumna) + "tags:\n" + renderTagiBlok(tagi, kolumna+2)
	if wstaw > 0 && wstaw <= len(d.src) && d.src[wstaw-1] != '\n' {
		blok = "\n" + blok
	}
	return d.replaceRange(wstaw, wstaw, []byte(blok))
}

func (d *Document) podmieńTagiBlokowe(seq *ast.SequenceNode, tagi []string) error {
	pierwszy, err := d.offsetOf(seq.Values[0])
	if err != nil {
		return err
	}
	ostatni, err := d.offsetOf(seq.Values[len(seq.Values)-1])
	if err != nil {
		return err
	}
	myślnik := d.dashIndentOf(pierwszy)
	start := d.lineStart(pierwszy)
	koniec := d.blockEnd(ostatni, d.dashIndentOf(ostatni))
	return d.replaceRange(start, koniec, []byte(renderTagiBlok(tagi, myślnik)))
}
