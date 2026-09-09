package config

import (
	"slices"
	"strconv"
	"strings"
)

// replaceRange podmienia bajty [start,end) i przeparsowuje dokument.
// Po każdej mutacji offsety w AST są nieaktualne, więc reparse jest obowiązkowy.
func (d *Document) replaceRange(start, end int, repl []byte) error {
	next := slices.Concat(d.src[:start:start], repl, d.src[end:])
	prev := d.src
	d.src = next
	if err := d.reparse(); err != nil {
		d.src = prev
		_ = d.reparse() // wróć do stanu sprzed zmiany
		return err
	}
	return nil
}

// quoteLike formatuje wartość tak, żeby pasowała do stylu, który już jest w pliku.
// old to oryginalne bajty wartości (mogą zawierać cudzysłowy).
func quoteLike(old []byte, value string) string {
	if len(old) > 0 && (old[0] == '"' || old[0] == '\'') {
		return quoteWith(rune(old[0]), value)
	}
	if needsQuoting(value) {
		return quoteWith('"', value)
	}
	return value
}

func quoteWith(q rune, v string) string {
	if q == '\'' {
		// w YAML apostrof w wartości cytowanej pojedynczo podwaja się
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// needsQuoting mówi, czy gołe zapisanie wartości zmieniłoby jej znaczenie
// albo rozwaliło plik.
func needsQuoting(v string) bool {
	if v == "" {
		return true
	}
	if strings.TrimSpace(v) != v {
		return true
	}
	if strings.ContainsAny(v, "\n\r\t") {
		return true
	}
	if strings.Contains(v, ": ") || strings.HasSuffix(v, ":") || strings.Contains(v, " #") {
		return true
	}
	switch v[0] {
	case '&', '*', '!', '%', '@', '`', '>', '|', '{', '[', ',', '?', '#', '-', ':', '\'', '"':
		return true
	}
	// wartość, która sama z siebie stałaby się liczbą, boolem albo nullem
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return true
	}
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	return false
}

// lineIndent zwraca wcięcie (w bajtach) linii, w której leży offset.
func (d *Document) lineIndent(off int) int {
	start := d.lineStart(off)
	n := 0
	for i := start; i < len(d.src) && (d.src[i] == ' ' || d.src[i] == '\t'); i++ {
		n++
	}
	return n
}

func (d *Document) lineStart(off int) int {
	for i := off; i > 0; i-- {
		if d.src[i-1] == '\n' {
			return i
		}
	}
	return 0
}

func (d *Document) lineEnd(off int) int {
	for i := off; i < len(d.src); i++ {
		if d.src[i] == '\n' {
			return i
		}
	}
	return len(d.src)
}
