package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

var (
	ErrPathNotFound    = errors.New("path not found in config")
	ErrServiceNotFound = errors.New("service not found")
	ErrGroupNotFound   = errors.New("group not found")
	// ErrDuplikat — nazwa już zajęta w tej samej liście (usługa w grupie, grupa).
	// Dwie usługi o tej samej nazwie scaliłyby historię sprawdzeń pod jednym kluczem.
	ErrDuplikat = errors.New("name already exists")
)

// Document to konfiguracja widziana jako TEKST, nie jako struktura danych.
//
// AST służy wyłącznie do nawigacji — do wskazania, gdzie w oryginalnych bajtach
// leży dana wartość. Zapis nigdy nie printuje AST-a, tylko podmienia zakres
// bajtów w src. Dzięki temu wszystko, czego użytkownik nie zmienił — komentarze,
// puste linie, anchory, merge-keye, wyrównanie — zostaje fizycznie nietknięte.
//
// Sprawdzone empirycznie 2026-08-14: print AST-a (nawet w goccy, który radzi
// sobie najlepiej) zbija wyrównanie spacji przed komentarzem końca linii, czyli
// rusza linie, których nikt nie edytował. Stąd splice zamiast printu.
type Document struct {
	src   []byte
	file  *ast.File
	lines []int // offset początku każdej linii (0-based indeks linii)

	// wypełniane przez Open — potrzebne do bezpiecznego zapisu
	path string
	hash [32]byte
	mode os.FileMode

	// crlf: plik miał WYŁĄCZNIE końce linii CRLF. src trzyma wtedy sam LF
	// (cała nawigacja po offsetach i wstawki liczą na jeden bajt końca linii),
	// a Bytes() przywraca CRLF. Plik z mieszanymi końcami zostaje nietknięty.
	crlf bool
}

// normalizujKońce zamienia CRLF na LF, ale tylko gdy KAŻDY \n w pliku jest
// poprzedzony \r — dzięki temu nietknięty round-trip jest bajt w bajt
// identyczny, a plik z mieszanymi końcami nie jest po cichu przerabiany.
func normalizujKońce(src []byte) ([]byte, bool) {
	crlf := bytes.Count(src, []byte("\r\n"))
	if crlf == 0 || crlf != bytes.Count(src, []byte("\n")) {
		return src, false
	}
	return bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n")), true
}

func Parse(src []byte) (*Document, error) {
	src, crlf := normalizujKońce(src)
	d := &Document{src: src, crlf: crlf}
	if err := d.reparse(); err != nil {
		return nil, err
	}
	return d, nil
}

// reparse odświeża AST i indeks linii po zmianie src.
func (d *Document) reparse() error {
	f, err := parser.ParseBytes(d.src, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	if len(f.Docs) == 0 || f.Docs[0].Body == nil {
		return errors.New("parse yaml: empty document")
	}
	d.file = f
	d.lines = d.lines[:0]
	d.lines = append(d.lines, 0)
	for i, b := range d.src {
		if b == '\n' {
			d.lines = append(d.lines, i+1)
		}
	}
	return nil
}

// Bytes zwraca aktualną treść pliku. To są prawdziwe bajty, nie re-print AST-a;
// przy pliku CRLF końce linii wracają do CRLF.
func (d *Document) Bytes() []byte {
	if d.crlf {
		return bytes.ReplaceAll(d.src, []byte("\n"), []byte("\r\n"))
	}
	return d.src
}

// root zwraca korzeń pierwszego dokumentu.
func (d *Document) root() ast.Node { return d.file.Docs[0].Body }

// scalar zwraca surową wartość tokenu.
// ! Nie używać n.String(): przy ParseComments dokleja komentarz z końca linii
// (`"Sonarr" # cudzysłowy celowo`) i psuje każde porównanie.
func scalar(n ast.Node) string {
	if n == nil {
		return ""
	}
	if t := n.GetToken(); t != nil {
		return t.Value
	}
	return ""
}

// unwrap zdejmuje opakowania, które nie niosą danych: `klucz: &anchor {...}`
// daje AnchorNode, a nie mapping. Nawigacja ma tego nie widzieć.
func unwrap(n ast.Node) ast.Node {
	for {
		switch v := n.(type) {
		case *ast.AnchorNode:
			n = v.Value
		case *ast.TagNode:
			n = v.Value
		default:
			return n
		}
	}
}

func asMapping(n ast.Node) *ast.MappingNode {
	switch v := unwrap(n).(type) {
	case *ast.MappingNode:
		return v
	case *ast.MappingValueNode:
		return &ast.MappingNode{Values: []*ast.MappingValueNode{v}}
	}
	return nil
}

// child schodzi o jeden krok: po kluczu (string) albo po indeksie (int).
func child(n ast.Node, step any) ast.Node {
	switch s := step.(type) {
	case string:
		m := asMapping(n)
		if m == nil {
			return nil
		}
		for _, kv := range m.Values {
			if scalar(kv.Key) == s {
				return kv.Value
			}
		}
	case int:
		seq, ok := unwrap(n).(*ast.SequenceNode)
		if !ok || s < 0 || s >= len(seq.Values) {
			return nil
		}
		return seq.Values[s]
	}
	return nil
}

// node zwraca węzeł spod ścieżki, np. node("groups", 0, "services", 1, "url").
func (d *Document) node(path ...any) (ast.Node, error) {
	cur := d.root()
	for i, step := range path {
		next := child(cur, step)
		if next == nil {
			return nil, fmt.Errorf("%w: %v (krok %d)", ErrPathNotFound, path[:i+1], i)
		}
		cur = next
	}
	return cur, nil
}

// offsetOf przelicza pozycję tokenu (linia/kolumna, obie 1-based) na offset bajtowy.
func (d *Document) offsetOf(n ast.Node) (int, error) {
	tok := n.GetToken()
	if tok == nil || tok.Position == nil {
		return 0, fmt.Errorf("%w: węzeł bez pozycji", ErrPathNotFound)
	}
	li := tok.Position.Line - 1
	if li < 0 || li >= len(d.lines) {
		return 0, fmt.Errorf("%w: linia %d poza plikiem", ErrPathNotFound, tok.Position.Line)
	}
	off := d.lines[li] + tok.Position.Column - 1
	if off < 0 || off > len(d.src) {
		return 0, fmt.Errorf("%w: offset %d poza plikiem", ErrPathNotFound, off)
	}
	return off, nil
}

// valueRange zwraca zakres bajtów [start,end) zajmowany przez wartość spod ścieżki.
// Zakres obejmuje cudzysłowy, jeśli wartość jest cytowana, i nie obejmuje
// komentarza z końca linii ani spacji przed nim.
func (d *Document) valueRange(path ...any) (int, int, error) {
	n, err := d.node(path...)
	if err != nil {
		return 0, 0, err
	}
	start, err := d.offsetOf(n)
	if err != nil {
		return 0, 0, err
	}
	return start, d.scanValueEnd(start), nil
}

// scanValueEnd wyznacza koniec wartości zaczynającej się na start.
func (d *Document) scanValueEnd(start int) int {
	src := d.src
	if start >= len(src) {
		return len(src)
	}

	switch src[start] {
	case '"', '\'':
		q := src[start]
		for i := start + 1; i < len(src); i++ {
			if src[i] == '\\' && q == '"' {
				i++
				continue
			}
			if src[i] == q {
				return i + 1
			}
			if src[i] == '\n' {
				break
			}
		}
	case '[', '{':
		open, close := src[start], byte(']')
		if open == '{' {
			close = '}'
		}
		depth := 0
		for i := start; i < len(src); i++ {
			switch src[i] {
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
	}

	// skalar bez cudzysłowów: do końca linii, minus komentarz i spacje z prawej
	end := len(src)
	for i := start; i < len(src); i++ {
		if src[i] == '\n' {
			end = i
			break
		}
	}
	for i := start; i < end-1; i++ {
		if (src[i] == ' ' || src[i] == '\t') && src[i+1] == '#' {
			end = i
			break
		}
	}
	for end > start && (src[end-1] == ' ' || src[end-1] == '\t' || src[end-1] == '\r') {
		end--
	}
	return end
}

// offsetKlucza zwraca offset bajtowy KLUCZA (nie wartości) w mapie n.
// Potrzebny wszędzie tam, gdzie wstawiamy albo usuwamy całą linię, a nie
// podmieniamy wartość — czyli tam, gdzie valueRange nie wystarcza.
func (d *Document) offsetKlucza(n ast.Node, klucz string) (int, error) {
	m := asMapping(n)
	if m == nil {
		return 0, fmt.Errorf("%w: węzeł nie jest mapą (klucz %q)", ErrPathNotFound, klucz)
	}
	for _, kv := range m.Values {
		if scalar(kv.Key) == klucz {
			return d.offsetOf(kv.Key)
		}
	}
	return 0, fmt.Errorf("%w: brak klucza %q", ErrPathNotFound, klucz)
}
