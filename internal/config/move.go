package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// Zmiana kolejności: usługi w grupie, usługi między grupami, same grupy.
//
// Każdy ruch to dwa splice'y na oryginalnym tekście — wycięcie bloku i wstawienie
// go w nowym miejscu. Blok niesie komentarze przyklejone nad wpisem i pola,
// których panel nie zna; nic nie jest składane od nowa z modelu.

// elementListy to zakres bajtów jednego elementu listy (`- name: ...`) razem z
// komentarzem przyklejonym nad nim, bez pustych linii po nim.
type elementListy struct {
	początek, koniec, dash int
}

// początekZKomentarzem cofa początek linii o komentarze stojące bezpośrednio
// nad wpisem — te same, które zabiera RemoveService.
func (d *Document) początekZKomentarzem(off int) int {
	start := d.lineStart(off)
	for start > 0 {
		pk := start - 1
		ps := d.lineStart(pk)
		if strings.HasPrefix(strings.TrimSpace(string(d.src[ps:pk])), "#") {
			start = ps
			continue
		}
		break
	}
	return start
}

func (d *Document) elementyListy(seq *ast.SequenceNode) ([]elementListy, error) {
	out := make([]elementListy, 0, len(seq.Values))
	for _, v := range seq.Values {
		off, err := d.offsetOf(v)
		if err != nil {
			return nil, err
		}
		dash := d.dashIndentOf(off)
		out = append(out, elementListy{d.początekZKomentarzem(off), d.blockEnd(off, dash), dash})
	}
	return out, nil
}

// listaRozdzielona mówi, czy użytkownik rozdziela elementy pustą linią —
// patrzymy na drugi i kolejne, bo pierwszy zwykle stoi pod kluczem.
func (d *Document) listaRozdzielona(el []elementListy) bool {
	for _, e := range el[min(1, len(el)):] {
		if d.poprzedzaPustaLinia(e.początek) {
			return true
		}
	}
	return false
}

func (d *Document) pustaLiniaOd(off int) (koniec int, ok bool) {
	if off >= len(d.src) {
		return off, false
	}
	le := d.lineEnd(off)
	return le + 1, strings.TrimSpace(string(d.src[off:le])) == ""
}

// wytnijElement wycina element i zwraca jego linie zdjęte z wcięcia.
//
// Puste linie: środkowy element zabiera ze sobą tę po sobie. Ostatni zostawia
// pustą linię za sobą (to ona oddziela listę od następnej grupy) i zabiera
// własną poprzedzającą, żeby nie powstał podwójny odstęp.
func (d *Document) wytnijElement(seq *ast.SequenceNode, i int) ([]string, error) {
	el, err := d.elementyListy(seq)
	if err != nil {
		return nil, err
	}
	e := el[i]
	linie := []string{}
	for _, l := range strings.Split(strings.TrimRight(string(d.src[e.początek:e.koniec]), "\n"), "\n") {
		if len(l) >= e.dash && strings.TrimSpace(l[:e.dash]) == "" {
			l = l[e.dash:]
		} else {
			l = strings.TrimLeft(l, " ")
		}
		linie = append(linie, l)
	}

	start, end := e.początek, e.koniec
	if i < len(el)-1 {
		for {
			next, pusta := d.pustaLiniaOd(end)
			if !pusta {
				break
			}
			end = next
		}
	} else {
		_, zaPusta := d.pustaLiniaOd(end)
		if zaPusta || end >= len(d.src) {
			for start > 0 {
				pk := start - 1
				ps := d.lineStart(pk)
				if strings.TrimSpace(string(d.src[ps:pk])) != "" {
					break
				}
				start = ps
			}
		}
	}
	if err := d.replaceRange(start, end, nil); err != nil {
		return nil, err
	}
	return linie, nil
}

// wstawElement wstawia linie jako element o danym indeksie (len = na koniec).
func (d *Document) wstawElement(seq *ast.SequenceNode, indeks int, linie []string, rozdziel bool) error {
	el, err := d.elementyListy(seq)
	if err != nil {
		return err
	}
	dash := el[0].dash
	var b strings.Builder
	for _, l := range linie {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", dash) + l + "\n")
	}
	blok := b.String()

	if indeks < len(el) {
		if rozdziel {
			blok += "\n"
		}
		at := el[indeks].początek
		return d.replaceRange(at, at, []byte(blok))
	}
	at := el[len(el)-1].koniec
	if rozdziel {
		blok = "\n" + blok
	}
	if at > 0 && d.src[at-1] != '\n' {
		blok = "\n" + blok // plik bez końcowego \n
	}
	return d.replaceRange(at, at, []byte(blok))
}

func (d *Document) sekwencja(ścieżka ...any) (*ast.SequenceNode, bool) {
	n, err := d.node(ścieżka...)
	if err != nil {
		return nil, false
	}
	seq, ok := unwrap(n).(*ast.SequenceNode)
	return seq, ok
}

var errIndeks = errors.New("position out of range")

// MoveServiceTo przenosi usługę do grupy doGrupy na pozycję indeks. Indeks to
// miejsce usługi w liście docelowej PO przesunięciu (0 = pierwsza, len = ostatnia),
// więc ten sam indeks działa dla zmiany kolejności w grupie i dla przeniesienia.
// Ruch w miejsce, w którym usługa już stoi, nie zmienia ani bajta.
func (d *Document) MoveServiceTo(zGrupy, usługa, doGrupy string, indeks int) error {
	gi, si, err := d.findService(zGrupy, usługa)
	if err != nil {
		return err
	}
	ti, err := d.findGroup(doGrupy)
	if err != nil {
		return err
	}
	same := gi == ti
	if !same {
		if _, _, err := d.findService(doGrupy, usługa); err == nil {
			return fmt.Errorf("service %q already exists in group %q", usługa, doGrupy)
		}
	}
	z, _ := d.sekwencja("groups", gi, "services")
	cel, maCel := d.sekwencja("groups", ti, "services")
	dłCel := 0
	if maCel {
		dłCel = len(cel.Values)
	}
	if same {
		dłCel-- // usługa wyjdzie z listy, zanim wstawimy ją z powrotem
	}
	if indeks < 0 || indeks > dłCel {
		return fmt.Errorf("%w: %d (0..%d)", errIndeks, indeks, dłCel)
	}
	if same && indeks == si {
		return nil
	}

	rozdziel := false
	if maCel && len(cel.Values) > 0 {
		el, err := d.elementyListy(cel)
		if err != nil {
			return err
		}
		rozdziel = d.listaRozdzielona(el)
	}

	linie, err := d.wytnijElement(z, si)
	if err != nil {
		return err
	}
	if len(z.Values) == 1 {
		if err := d.ustawPustąListę(zGrupy); err != nil {
			return err
		}
	}
	cel, maCel = d.sekwencja("groups", ti, "services")
	if !maCel || len(cel.Values) == 0 {
		return d.wstawBlokDoPustej(ti, linie)
	}
	return d.wstawElement(cel, indeks, linie, rozdziel)
}

// MoveGroup przenosi grupę na pozycję indeks (po przesunięciu), razem z
// usługami i komentarzem nad nią.
func (d *Document) MoveGroup(nazwa string, indeks int) error {
	gi, err := d.findGroup(nazwa)
	if err != nil {
		return err
	}
	seq, ok := d.sekwencja("groups")
	if !ok {
		return fmt.Errorf("%w: groups nie jest listą", ErrPathNotFound)
	}
	if indeks < 0 || indeks > len(seq.Values)-1 {
		return fmt.Errorf("%w: %d (0..%d)", errIndeks, indeks, len(seq.Values)-1)
	}
	if indeks == gi {
		return nil
	}
	el, err := d.elementyListy(seq)
	if err != nil {
		return err
	}
	rozdziel := d.listaRozdzielona(el)
	linie, err := d.wytnijElement(seq, gi)
	if err != nil {
		return err
	}
	seq, _ = d.sekwencja("groups")
	return d.wstawElement(seq, indeks, linie, rozdziel)
}
