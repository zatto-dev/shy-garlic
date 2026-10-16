package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Sekcja „Sprawdzanie" w edytorze usługi (GH #6, SHYG-51).

// ZmianaSprawdzania — pola bloku `check` przysłane z edytora usługi.
//
// nil znaczy „nie ruszaj". Wartość zerowa (0, pusta lista, false) znaczy „wróć
// do domyślnej", czyli usuń klucz z pliku — domyślne wartości nie są wpisywane.
type ZmianaSprawdzania struct {
	Interval *Duration
	Timeout  *Duration
	Expect   *[]int
	Insecure *bool
}

// UstawSprawdzanie zapisuje zmienione pola bloku `check` usługi.
//
// Porównuje z tym, co jest w pliku (bez domyślnych z Load), i rusza tylko
// klucze, które faktycznie się zmieniają: podmienia wartość w miejscu,
// dopisuje brakujący klucz albo go kasuje. Komentarze i reszta bloku zostają.
// Gdy bloku nie ma — powstaje; gdy po zmianie zostaje pusty — znika.
//
// Blok `check` dziedziczony przez `<<: *anchor` kończy się ErrPoleDziedziczone:
// lokalny `check` obok merge'a zastąpiłby cały dziedziczony blok (merge jest
// płytki), a nie tylko zmienione pole.
func (d *Document) UstawSprawdzanie(grupa, usługa string, z ZmianaSprawdzania) error {
	gi, si, err := d.findService(grupa, usługa)
	if err != nil {
		return err
	}
	stary, err := d.surowySprawdzania(gi, si)
	if err != nil {
		return err
	}

	nowy := stary
	var zmiany []string // klucze w kolejności zapisu
	if z.Interval != nil && *z.Interval != stary.Interval {
		nowy.Interval = *z.Interval
		zmiany = append(zmiany, "interval")
	}
	if z.Timeout != nil && *z.Timeout != stary.Timeout {
		nowy.Timeout = *z.Timeout
		zmiany = append(zmiany, "timeout")
	}
	if z.Expect != nil && !slices.Equal(*z.Expect, stary.Expect) {
		nowy.Expect = slices.Clone(*z.Expect)
		zmiany = append(zmiany, "expect")
	}
	if z.Insecure != nil && *z.Insecure != stary.Insecure {
		nowy.Insecure = *z.Insecure
		zmiany = append(zmiany, "insecure")
	}
	if len(zmiany) == 0 {
		return nil
	}

	usł := []any{"groups", gi, "services", si}
	ścieżka := append(slices.Clone(usł), "check")
	n, err := d.node(ścieżka...)
	switch {
	case errors.Is(err, ErrPathNotFound):
		if !pustySprawdzania(stary) {
			return ErrPoleDziedziczone
		}
		return d.wstawBlokUsługi(renderCheck(nowy, 0), usł...)
	case err != nil:
		return err
	}

	if !d.blokowaMapa(n, "check", usł...) {
		// `check: {interval: 1m}` albo puste `check:` — przepisujemy cały blok
		if err := d.usuńPoleWpisu("check", usł...); err != nil {
			return err
		}
		if pustySprawdzania(nowy) {
			return nil
		}
		return d.wstawBlokUsługi(renderCheck(nowy, 0), usł...)
	}

	// najpierw zapisy, potem kasowanie — blok nie może po drodze zostać pusty
	for _, klucz := range zmiany {
		if wartość := wartośćSprawdzania(nowy, klucz); wartość != "" {
			if err := d.ustawKluczSprawdzania(klucz, wartość, ścieżka...); err != nil {
				return err
			}
		}
	}
	for _, klucz := range zmiany {
		if wartośćSprawdzania(nowy, klucz) == "" {
			if err := d.usuńPoleWpisu(klucz, ścieżka...); err != nil {
				return err
			}
		}
	}
	if n, err := d.node(ścieżka...); err == nil {
		if m := asMapping(n); m == nil || len(m.Values) == 0 {
			return d.usuńPoleWpisu("check", usł...)
		}
	}
	return nil
}

// surowySprawdzania zwraca blok `check` usługi tak, jak stoi w pliku: z
// rozwiniętymi merge-keyami, ale BEZ domyślnych, które dokłada Load.
func (d *Document) surowySprawdzania(gi, si int) (Check, error) {
	var cfg Config
	if err := yaml.Unmarshal(d.src, &cfg); err != nil {
		return Check{}, fmt.Errorf("parse config: %w", err)
	}
	if gi >= len(cfg.Groups) || si >= len(cfg.Groups[gi].Services) {
		return Check{}, ErrServiceNotFound
	}
	return cfg.Groups[gi].Services[si].Check, nil
}

func pustySprawdzania(c Check) bool {
	return c.Interval == 0 && c.Timeout == 0 && len(c.Expect) == 0 && !c.Insecure &&
		c.Path == "" && c.Contains == "" && c.NotContains == ""
}

// wartośćSprawdzania — wartość klucza gotowa do wpisania; "" = klucza ma nie być.
func wartośćSprawdzania(c Check, klucz string) string {
	switch klucz {
	case "interval":
		if c.Interval != 0 {
			return c.Interval.String()
		}
	case "timeout":
		if c.Timeout != 0 {
			return c.Timeout.String()
		}
	case "expect":
		if len(c.Expect) > 0 {
			return renderKody(c.Expect)
		}
	case "insecure":
		if c.Insecure {
			return "true"
		}
	}
	return ""
}

func renderKody(kody []int) string {
	s := make([]string, len(kody))
	for i, k := range kody {
		s[i] = strconv.Itoa(k)
	}
	return "[" + strings.Join(s, ", ") + "]"
}

// renderCheck — blok `check:` z niezerowymi polami, klucz wcięty o kolumna.
func renderCheck(c Check, kolumna int) string {
	if pustySprawdzania(c) {
		return ""
	}
	key := strings.Repeat(" ", kolumna)
	var b strings.Builder
	fmt.Fprintf(&b, "%scheck:\n", key)
	for _, klucz := range []string{"interval", "timeout", "expect", "insecure"} {
		if v := wartośćSprawdzania(c, klucz); v != "" {
			fmt.Fprintf(&b, "%s  %s: %s\n", key, klucz, v)
		}
	}
	if c.Path != "" {
		fmt.Fprintf(&b, "%s  path: %s\n", key, quoteLike(nil, c.Path))
	}
	if c.Contains != "" {
		fmt.Fprintf(&b, "%s  contains: %s\n", key, quoteLike(nil, c.Contains))
	}
	if c.NotContains != "" {
		fmt.Fprintf(&b, "%s  not_contains: %s\n", key, quoteLike(nil, c.NotContains))
	}
	return b.String()
}

// blokowaMapa mówi, czy wartość klucza to mapa blokowa z wpisami (a nie flow
// `{...}` ani pusta wartość). Patrzymy w źródło: tuż za `klucz:` w linii klucza
// ma nie stać nic poza komentarzem.
func (d *Document) blokowaMapa(n ast.Node, klucz string, ścieżka ...any) bool {
	if m := asMapping(n); m == nil || len(m.Values) == 0 {
		return false
	}
	rodzic, err := d.node(ścieżka...)
	if err != nil {
		return false
	}
	off, err := d.offsetKlucza(rodzic, klucz)
	if err != nil {
		return false
	}
	reszta := string(d.src[off+len(klucz) : d.lineEnd(off)])
	reszta = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reszta), ":"))
	return reszta == "" || strings.HasPrefix(reszta, "#")
}

// ustawKluczSprawdzania podmienia wartość istniejącego klucza w bloku check
// albo dopisuje go na końcu bloku. Lista kodów zapisana blokowo (`- 200`)
// zamienia się w jedną linię `[200, 301]`.
func (d *Document) ustawKluczSprawdzania(klucz, wartość string, ścieżka ...any) error {
	n, err := d.node(append(slices.Clone(ścieżka), klucz)...)
	if errors.Is(err, ErrPathNotFound) {
		return d.wstawKlucz(klucz, wartość, ścieżka...)
	}
	if err != nil {
		return err
	}
	if seq, ok := unwrap(n).(*ast.SequenceNode); ok && !seq.IsFlowStyle {
		if err := d.usuńPoleWpisu(klucz, ścieżka...); err != nil {
			return err
		}
		return d.wstawKlucz(klucz, wartość, ścieżka...)
	}
	start, end, err := d.valueRange(append(slices.Clone(ścieżka), klucz)...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(wartość))
}

// wstawBlokUsługi dopisuje wielolinijkowy blok (wcięty od kolumny 0) na koniec
// wpisu usługi, z wcięciem jej kluczy.
func (d *Document) wstawBlokUsługi(blok string, ścieżka ...any) error {
	kolumna, wstaw, err := d.miejsceWstawienia(ścieżka...)
	if err != nil {
		return err
	}
	ind := strings.Repeat(" ", kolumna)
	var b strings.Builder
	for _, linia := range strings.SplitAfter(blok, "\n") {
		if linia != "" {
			b.WriteString(ind + linia)
		}
	}
	tekst := b.String()
	if wstaw > 0 && wstaw <= len(d.src) && d.src[wstaw-1] != '\n' {
		tekst = "\n" + tekst
	}
	return d.replaceRange(wstaw, wstaw, []byte(tekst))
}
