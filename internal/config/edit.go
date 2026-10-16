package config

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
)

// findGroup zwraca indeks grupy o danej nazwie.
func (d *Document) findGroup(group string) (int, error) {
	groups, err := d.node("groups")
	if err != nil {
		return 0, err
	}
	seq, ok := unwrap(groups).(*ast.SequenceNode)
	if !ok {
		return 0, fmt.Errorf("%w: groups nie jest listą", ErrPathNotFound)
	}
	for i, g := range seq.Values {
		if scalar(child(g, "name")) == group {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrGroupNotFound, group)
}

// findService zwraca indeksy grupy i usługi o podanych nazwach.
func (d *Document) findService(group, service string) (int, int, error) {
	gi, err := d.findGroup(group)
	if err != nil {
		return 0, 0, err
	}
	svcs, err := d.node("groups", gi, "services")
	if err != nil {
		return 0, 0, fmt.Errorf("%w: grupa %q nie ma usług", ErrServiceNotFound, group)
	}
	seq, ok := unwrap(svcs).(*ast.SequenceNode)
	if !ok {
		return 0, 0, fmt.Errorf("%w: services nie jest listą", ErrPathNotFound)
	}
	for i, s := range seq.Values {
		if scalar(child(s, "name")) == service {
			return gi, i, nil
		}
	}
	return 0, 0, fmt.Errorf("%w: %q w grupie %q", ErrServiceNotFound, service, group)
}

// blockEnd zwraca offset tuż za ostatnią linią z treścią należącą do elementu
// listy zaczynającego się w linii offsetu off. Puste linie na końcu bloku nie
// są wciągane — nowy element wstawiamy przed nimi, nie po.
func (d *Document) blockEnd(off, dashIndent int) int {
	end := d.lineEnd(off) + 1 // za '\n' linii z '-'
	if end > len(d.src) {
		return len(d.src)
	}
	last := end
	for i := end; i < len(d.src); {
		le := d.lineEnd(i)
		linia := string(d.src[i:le])
		if strings.TrimSpace(linia) == "" {
			i = le + 1
			continue
		}
		if d.lineIndent(i) <= dashIndent {
			break
		}
		i = le + 1
		last = i
	}
	if last > len(d.src) {
		last = len(d.src)
	}
	return last
}

// zakresWpisu zwraca kolumnę klucza spod offsetu oraz próg wcięcia, po którym
// jego blok się kończy — czyli drugi argument dla blockEnd.
//
// ! Dwa różne przypadki, stąd osobna funkcja. Zwykły klucz mapy (`view:`) ma
// rodzeństwo o TYM SAMYM wcięciu, więc blok kończy się na nim. Klucz stojący
// po myślniku (`- name:`) ma kolumnę o dwa większą niż wcięcie linii, a jego
// rodzeństwo (`url:`, `icon:`) należy jeszcze do tego samego wpisu.
func (d *Document) zakresWpisu(off int) (kolumna, próg int) {
	kolumna = off - d.lineStart(off)
	próg = kolumna
	if d.lineIndent(off) < kolumna {
		próg = kolumna - 1
	}
	return kolumna, próg
}

// dashIndentOf zwraca wcięcie myślnika elementu listy, w którego linii leży off.
func (d *Document) dashIndentOf(off int) int {
	ls := d.lineStart(off)
	for i := ls; i < len(d.src) && i < d.lineEnd(off); i++ {
		if d.src[i] == '-' {
			return i - ls
		}
	}
	return d.lineIndent(off)
}

// poprzedzaPustaLinia mówi, czy linia przed tą, w której leży off, jest pusta.
// Służy do naśladowania stylu pliku: jeśli user rozdziela usługi pustą linią,
// dopisana usługa też ma być rozdzielona.
func (d *Document) poprzedzaPustaLinia(off int) bool {
	ls := d.lineStart(off)
	if ls == 0 {
		return false
	}
	prevEnd := ls - 1
	prevStart := d.lineStart(prevEnd)
	return strings.TrimSpace(string(d.src[prevStart:prevEnd])) == ""
}

// renderService składa blok YAML usługi z zadanym wcięciem myślnika.
func renderService(svc Service, dashIndent int) string {
	ind := strings.Repeat(" ", dashIndent)
	key := ind + "  "

	var b strings.Builder
	fmt.Fprintf(&b, "%s- name: %s\n", ind, quoteLike(nil, svc.Name))
	if svc.URL != "" {
		fmt.Fprintf(&b, "%surl: %s\n", key, quoteLike(nil, svc.URL))
	}
	if svc.Icon != "" {
		fmt.Fprintf(&b, "%sicon: %s\n", key, quoteLike(nil, svc.Icon))
	}
	if svc.Size != "" {
		fmt.Fprintf(&b, "%ssize: %s\n", key, quoteLike(nil, svc.Size))
	}
	if svc.Description != "" {
		fmt.Fprintf(&b, "%sdescription: %s\n", key, quoteLike(nil, svc.Description))
	}
	if len(svc.Tags) > 0 {
		fmt.Fprintf(&b, "%stags: %s\n", key, renderTagiFlow(svc.Tags))
	}
	b.WriteString(renderCheck(svc.Check, len(key)))
	return b.String()
}

// AddService dopisuje usługę na koniec grupy. Wcięcie bierze z ostatniej usługi
// w tej grupie, a nie z założeń — plik użytkownika dyktuje styl.
func (d *Document) AddService(group string, svc Service) error {
	if svc.Name == "" {
		return errors.New("service name is required")
	}
	gi, err := d.findGroup(group)
	if err != nil {
		return err
	}
	if _, _, err := d.findService(group, svc.Name); err == nil {
		return fmt.Errorf("service %q already exists in group %q", svc.Name, group)
	}

	svcs, err := d.node("groups", gi, "services")
	if err != nil {
		return d.addServiceDoPustejGrupy(gi, svc)
	}
	seq, ok := unwrap(svcs).(*ast.SequenceNode)
	if !ok || len(seq.Values) == 0 {
		return d.addServiceDoPustejGrupy(gi, svc)
	}

	ostatni := seq.Values[len(seq.Values)-1]
	off, err := d.offsetOf(ostatni)
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	wstaw := d.blockEnd(off, dashIndent)

	blok := renderService(svc, dashIndent)
	if d.poprzedzaPustaLinia(off) {
		blok = "\n" + blok
	}
	return d.replaceRange(wstaw, wstaw, []byte(blok))
}

// addServiceDoPustejGrupy radzi sobie z dwoma przypadkami: grupa nie ma jeszcze
// klucza `services`, albo ma go z pustą listą (`services: []`). W drugim
// przypadku podmieniamy tę linię, bo dopisanie drugiego klucza dałoby
// "mapping key already defined".
func (d *Document) addServiceDoPustejGrupy(gi int, svc Service) error {
	nameNode, err := d.node("groups", gi, "name")
	if err != nil {
		return err
	}
	off, err := d.offsetOf(nameNode)
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	keyIndent := dashIndent + 2

	g, err := d.node("groups", gi)
	if err != nil {
		return err
	}
	if keyOff, err := d.offsetKlucza(g, "services"); err == nil {
		blok := strings.Repeat(" ", d.lineIndent(keyOff)) + "services:\n" +
			renderService(svc, d.lineIndent(keyOff)+2)
		// bez końcowego \n — zastępujemy linię, nie dokładamy nowej
		return d.replaceRange(d.lineStart(keyOff), d.lineEnd(keyOff), []byte(strings.TrimSuffix(blok, "\n")))
	}

	wstaw := d.blockEnd(off, dashIndent)
	blok := strings.Repeat(" ", keyIndent) + "services:\n" + renderService(svc, keyIndent+2)
	return d.replaceRange(wstaw, wstaw, []byte(blok))
}

// SetServiceField zmienia jedno pole usługi, podmieniając wyłącznie bajty tej
// wartości. Komentarz w tej samej linii, styl cytowania i wcięcie zostają.
//
// Gdy usługa tego pola jeszcze nie ma (`icon`, `size` — oba są opcjonalne),
// klucz jest dopisywany na koniec jej bloku. Bez tego pierwsze ustawienie
// ikony albo rozmiaru z UI cicho przepadało.
func (d *Document) SetServiceField(group, service, field, value string) error {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	if field == "name" && value != service {
		if _, _, err := d.findService(group, value); err == nil {
			return fmt.Errorf("%w: service %q in group %q", ErrDuplikat, value, group)
		}
	}
	start, end, err := d.valueRange("groups", gi, "services", si, field)
	if errors.Is(err, ErrPathNotFound) {
		return d.wstawKlucz(field, quoteLike(nil, value), "groups", gi, "services", si)
	}
	if err != nil {
		return err
	}
	repl := quoteLike(d.src[start:end], value)
	return d.replaceRange(start, end, []byte(repl))
}

// RemoveService usuwa usługę razem z jej komentarzem nagłówkowym (tym, który
// stoi bezpośrednio nad wpisem). Komentarz odsunięty pustą linią należy do
// pliku, nie do usługi, i zostaje.
func (d *Document) RemoveService(group, service string) error {
	gi, si, err := d.findService(group, service)
	if err != nil {
		return err
	}
	svcs, err := d.node("groups", gi, "services")
	if err != nil {
		return err
	}
	seq, ok := unwrap(svcs).(*ast.SequenceNode)
	if !ok {
		return fmt.Errorf("%w: services nie jest listą", ErrPathNotFound)
	}

	off, err := d.offsetOf(seq.Values[si])
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	start := d.początekZKomentarzem(off, dashIndent) // tylko komentarz w kolumnie myślnika
	end := d.blockEnd(off, dashIndent)

	// jeśli usuwamy ostatni wpis, zostaw pustą listę zamiast wiszącego klucza
	if len(seq.Values) == 1 {
		keyOff, err := d.offsetOf(seq.Values[0])
		if err == nil {
			_ = keyOff
		}
		if err := d.replaceRange(start, end, nil); err != nil {
			return err
		}
		return d.ustawPustąListę(group)
	}

	// zjedz pustą linię, która została po wpisie, żeby nie robić dziur
	for end < len(d.src) {
		koniecLinii := d.lineEnd(end)
		if strings.TrimSpace(string(d.src[end:koniecLinii])) != "" {
			break
		}
		end = koniecLinii + 1
	}
	return d.replaceRange(start, end, nil)
}

// ustawPustąListę zamienia osierocony klucz `services:` na `services: []`.
func (d *Document) ustawPustąListę(group string) error {
	gi, err := d.findGroup(group)
	if err != nil {
		return err
	}
	g, err := d.node("groups", gi)
	if err != nil {
		return err
	}
	off, err := d.offsetKlucza(g, "services")
	if err != nil {
		return nil // nie ma czego osierocać
	}
	koniec := d.lineEnd(off)
	if strings.Contains(string(d.src[off:koniec]), "[]") {
		return nil
	}
	return d.replaceRange(off, koniec, []byte("services: []"))
}

// AddGroup dopisuje nową, pustą grupę na koniec listy grup.
func (d *Document) AddGroup(name string) error {
	if name == "" {
		return errors.New("group name is required")
	}
	if _, err := d.findGroup(name); err == nil {
		return fmt.Errorf("group %q already exists", name)
	}
	groups, err := d.node("groups")
	if err != nil {
		return err
	}
	seq, ok := unwrap(groups).(*ast.SequenceNode)
	if !ok || len(seq.Values) == 0 {
		return fmt.Errorf("%w: brak listy grup", ErrPathNotFound)
	}
	off, err := d.offsetOf(seq.Values[len(seq.Values)-1])
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	wstaw := d.blockEnd(off, dashIndent)
	ind := strings.Repeat(" ", dashIndent)
	blok := fmt.Sprintf("%s- name: %s\n%s  services: []\n", ind, quoteLike(nil, name), ind)
	if d.poprzedzaPustaLinia(off) {
		blok = "\n" + blok
	}
	return d.replaceRange(wstaw, wstaw, []byte(blok))
}

// RemoveGroup usuwa całą grupę wraz z usługami.
func (d *Document) RemoveGroup(name string) error {
	gi, err := d.findGroup(name)
	if err != nil {
		return err
	}
	groups, err := d.node("groups")
	if err != nil {
		return err
	}
	seq := unwrap(groups).(*ast.SequenceNode)
	off, err := d.offsetOf(seq.Values[gi])
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	start := d.początekZKomentarzem(off, dashIndent)
	end := d.blockEnd(off, dashIndent)
	for end < len(d.src) {
		kl := d.lineEnd(end)
		if strings.TrimSpace(string(d.src[end:kl])) != "" {
			break
		}
		end = kl + 1
	}
	return d.replaceRange(start, end, nil)
}

// ReplaceAll podmienia całą treść pliku — używane przez edytor „surowy YAML".
// Zmiana jest odrzucana, jeśli nowa treść się nie parsuje.
func (d *Document) ReplaceAll(nowa []byte) error {
	// treść z samymi CRLF (np. z textarea) zapisujemy jako CRLF; treść z LF
	// dostaje styl pliku, który był.
	nowa, crlf := normalizujKońce(nowa)
	if !crlf && d.crlf {
		// mieszane końce w pliku CRLF — Bytes() dołoży \r do każdej linii,
		// więc istniejące \r\n dałyby \r\r\n
		nowa = bytes.ReplaceAll(nowa, []byte("\r\n"), []byte("\n"))
	}
	stary := d.crlf
	if crlf {
		d.crlf = true
	}
	if err := d.replaceRange(0, len(d.src), nowa); err != nil {
		d.crlf = stary
		return err
	}
	return nil
}

// RenameGroup zmienia nazwę grupy, nie ruszając jej zawartości.
func (d *Document) RenameGroup(stara, nowa string) error {
	if nowa == "" {
		return errors.New("group name is required")
	}
	if stara == nowa {
		return nil
	}
	if _, err := d.findGroup(nowa); err == nil {
		return fmt.Errorf("%w: group %q", ErrDuplikat, nowa)
	}
	gi, err := d.findGroup(stara)
	if err != nil {
		return err
	}
	start, end, err := d.valueRange("groups", gi, "name")
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(quoteLike(d.src[start:end], nowa)))
}

// MoveService przenosi usługę na koniec innej grupy razem z jej blokiem tekstu —
// komentarze i pola, których panel nie zna, jadą z nią. Wcięcie jest
// przeliczane pod grupę docelową. Konkretną pozycję daje MoveServiceTo.
func (d *Document) MoveService(zGrupy, usługa, doGrupy string) error {
	if zGrupy == doGrupy {
		return nil
	}
	ti, err := d.findGroup(doGrupy)
	if err != nil {
		return err
	}
	n := 0
	if seq, ok := d.sekwencja("groups", ti, "services"); ok {
		n = len(seq.Values)
	}
	return d.MoveServiceTo(zGrupy, usługa, doGrupy, n)
}

func (d *Document) wstawBlokDoPustej(gi int, linie []string) error {
	nameNode, err := d.node("groups", gi, "name")
	if err != nil {
		return err
	}
	off, err := d.offsetOf(nameNode)
	if err != nil {
		return err
	}
	dashIndent := d.dashIndentOf(off)
	keyIndent := dashIndent + 2
	ind := strings.Repeat(" ", keyIndent+2)

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", keyIndent) + "services:\n")
	for _, l := range linie {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(ind + l + "\n")
	}

	g, err := d.node("groups", gi)
	if err != nil {
		return err
	}
	if keyOff, err := d.offsetKlucza(g, "services"); err == nil {
		return d.replaceRange(d.lineStart(keyOff), d.lineEnd(keyOff),
			[]byte(strings.TrimSuffix(b.String(), "\n")))
	}
	wstaw := d.blockEnd(off, dashIndent)
	return d.replaceRange(wstaw, wstaw, []byte(b.String()))
}

// ── pojedyncze klucze poza listą usług ───────────────────────────────────────

// wstawKlucz dopisuje `klucz: wartość` na koniec mapy spod ścieżki. Wcięcie
// bierze z klucza, który już tam stoi — plik dyktuje styl, nie my.
// wartość musi być już sformatowana (quoteLike albo sprawdzona liczba).
func (d *Document) wstawKlucz(klucz, wartość string, ścieżka ...any) error {
	kolumna, wstaw, err := d.miejsceWstawienia(ścieżka...)
	if err != nil {
		return err
	}
	blok := strings.Repeat(" ", kolumna) + klucz + ": " + wartość + "\n"
	if wstaw > 0 && wstaw <= len(d.src) && d.src[wstaw-1] != '\n' {
		blok = "\n" + blok // plik bez końcowego \n — nie sklejaj linii
	}
	return d.replaceRange(wstaw, wstaw, []byte(blok))
}

// miejsceWstawienia zwraca kolumnę nowego klucza i offset, pod którym ma
// wylądować jego linia — tuż za ostatnim wpisem mapy spod ścieżki.
func (d *Document) miejsceWstawienia(ścieżka ...any) (kolumna, wstaw int, err error) {
	n, err := d.node(ścieżka...)
	if err != nil {
		return 0, 0, err
	}
	m := asMapping(n)
	if m == nil || len(m.Values) == 0 {
		return 0, 0, fmt.Errorf("%w: %v nie jest mapą z wpisami", ErrPathNotFound, ścieżka)
	}
	// od końca, bo szukamy ostatniego wpisu — a przy okazji przeskakujemy
	// węzeł bez pozycji, gdyby taki się trafił (parser bywa oszczędny przy
	// kluczach specjalnych w rodzaju `<<`)
	off := 0
	err = ErrPathNotFound
	for i := len(m.Values) - 1; i >= 0; i-- {
		if off, err = d.offsetOf(m.Values[i].Key); err == nil {
			break
		}
	}
	if err != nil {
		return 0, 0, err
	}
	kolumna, próg := d.zakresWpisu(off)
	return kolumna, d.blockEnd(off, próg), nil
}

// usuńKlucz kasuje linię (albo blok) danego klucza w mapie spod ścieżki.
// Brak klucza nie jest błędem — kasujemy to, czego i tak nie ma.
// ! Nie nadaje się do PIERWSZEGO klucza elementu listy (`- name:`): zabrałby
// razem z nim myślnik.
func (d *Document) usuńKlucz(klucz string, ścieżka ...any) error {
	n, err := d.node(ścieżka...)
	if err != nil {
		return nil
	}
	off, err := d.offsetKlucza(n, klucz)
	if err != nil {
		return nil
	}
	_, próg := d.zakresWpisu(off)
	return d.replaceRange(d.lineStart(off), d.blockEnd(off, próg), nil)
}

// SetPath zmienia wartość spod dowolnej ścieżki — to samo co SetServiceField,
// tylko bez szukania usługi. Styl cytowania z pliku wygrywa.
func (d *Document) SetPath(value string, path ...any) error {
	start, end, err := d.valueRange(path...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(quoteLike(d.src[start:end], value)))
}

// wartośćSurowa — jedyne, co wolno wpisać do pliku bez cytowania.
var wartośćSurowa = regexp.MustCompile(`^(-?[0-9]{1,6}|true|false)$`)

// SetPathRaw wpisuje wartość dosłownie, z pominięciem quoteLike.
//
// ! Istnieje wyłącznie dla liczb i booli: needsQuoting zacytowałby "10", bo
// ParseFloat się udaje, a pole `int` po drugiej stronie takiej wartości nie
// wczyta. Allowlista jest wąska celowo — surowy string użytkownika nie ma tędy
// drogi do pliku.
func (d *Document) SetPathRaw(value string, path ...any) error {
	if !wartośćSurowa.MatchString(value) {
		return fmt.Errorf("wartość surowa %q spoza allowlisty (liczba albo true/false)", value)
	}
	start, end, err := d.valueRange(path...)
	if err != nil {
		return err
	}
	return d.replaceRange(start, end, []byte(value))
}

// ── wygląd ───────────────────────────────────────────────────────────────────

// kluczeWyglądu — stała kolejność zapisu. Dwie takie same zmiany mają dać ten
// sam diff, więc nie iterujemy po mapie.
var kluczeWyglądu = []string{"preset", "image", "blur", "dim", "tone", "view"}

// UstawWygląd zapisuje podane pola bloku `appearance`. Klucza, którego nie ma
// w mapie, nie rusza wcale — dokładnie tak jak edytor usługi nie rusza pól,
// których użytkownik nie dotknął. Pusta wartość kasuje linię (tak wypada
// `image`, gdy tło przestaje być zdjęciem).
//
// Gdy bloku `appearance` nie ma w ogóle, powstaje cały, jednym spliceem.
func (d *Document) UstawWygląd(pola map[string]string) error {
	if len(pola) == 0 {
		return nil
	}
	if _, err := d.node("appearance"); err != nil {
		if !errors.Is(err, ErrPathNotFound) {
			return err
		}
		return d.DodajWygląd(wyglądZPól(pola))
	}
	for _, k := range kluczeWyglądu {
		v, jest := pola[k]
		if !jest {
			continue
		}
		if err := d.ustawPoleWyglądu(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (d *Document) ustawPoleWyglądu(klucz, wartość string) error {
	rodzic := []any{"appearance", "background"}
	if klucz == "view" {
		rodzic = []any{"appearance"}
	}
	ścieżka := append(append([]any{}, rodzic...), klucz)

	if wartość == "" {
		return d.usuńKlucz(klucz, rodzic...)
	}

	var err error
	if klucz == "blur" || klucz == "dim" {
		err = d.SetPathRaw(wartość, ścieżka...)
	} else {
		err = d.SetPath(wartość, ścieżka...)
	}
	if !errors.Is(err, ErrPathNotFound) {
		return err
	}

	// klucza jeszcze nie ma — dopisz go do mapy, jeśli ta w ogóle istnieje.
	// ! `appearance: <<: *coś` daje mapę bez naszych kluczy i bez miejsca na
	// nie; brak ścieżki nie jest wtedy błędem zapisu (tak samo traktujemy
	// brakującą `icon` w api).
	if _, e := d.node(rodzic...); e != nil {
		return nil
	}
	sformatowana := quoteLike(nil, wartość)
	if klucz == "blur" || klucz == "dim" {
		if !wartośćSurowa.MatchString(wartość) {
			return fmt.Errorf("%s %q nie jest liczbą", klucz, wartość)
		}
		sformatowana = wartość
	}
	return d.wstawKlucz(klucz, sformatowana, rodzic...)
}

func wyglądZPól(pola map[string]string) Appearance {
	w := Appearance{
		Background: Background{Preset: DomyślnyPreset, Blur: DomyślnyBlur, Dim: DomyślnyDim, Tone: DomyślnyTon},
		View:       DomyślnyWidok,
	}
	if v := pola["preset"]; v != "" {
		w.Background.Preset = v
	}
	if v := pola["image"]; v != "" {
		w.Background.Image = v
	}
	if v, err := strconv.Atoi(pola["blur"]); err == nil {
		w.Background.Blur = v
	}
	if v, err := strconv.Atoi(pola["dim"]); err == nil {
		w.Background.Dim = v
	}
	if v := pola["tone"]; v != "" {
		w.Background.Tone = v
	}
	if v := pola["view"]; v != "" {
		w.View = v
	}
	return w
}

// renderWygląd składa cały blok. Pola puste pomijamy — pusta wartość dałaby
// NullNode, po którym nawigacja już nie trafi (tak samo pomijamy puste url
// i icon w renderService).
func renderWygląd(w Appearance) string {
	var b strings.Builder
	b.WriteString("appearance:\n")
	b.WriteString("  background:\n")
	if w.Background.Preset != "" {
		fmt.Fprintf(&b, "    preset: %s\n", quoteLike(nil, w.Background.Preset))
	}
	if w.Background.Image != "" {
		fmt.Fprintf(&b, "    image: %s\n", quoteLike(nil, w.Background.Image))
	}
	fmt.Fprintf(&b, "    blur: %d\n", w.Background.Blur)
	fmt.Fprintf(&b, "    dim: %d\n", w.Background.Dim)
	if w.Background.Tone != "" {
		fmt.Fprintf(&b, "    tone: %s\n", quoteLike(nil, w.Background.Tone))
	}
	if w.View != "" {
		fmt.Fprintf(&b, "  view: %s\n", quoteLike(nil, w.View))
	}
	return b.String()
}

// DodajWygląd wstawia cały blok `appearance`, gdy go w pliku nie ma.
func (d *Document) DodajWygląd(w Appearance) error {
	if _, err := d.node("appearance"); err == nil {
		return nil // już jest — od zmian jest UstawWygląd
	}
	return d.wstawBlokNajwyższegoPoziomu(renderWygląd(w))
}

// UstawPrógPowiadomień zapisuje notify.threshold, dokładając cały blok
// `notify`, gdy pliku jeszcze nie ma o czym poprawiać.
//
// ! Bez tego pierwsze ustawienie progu w świeżej instalacji kończyło się 400
// „path not found": SetPathRaw umie podmienić klucz, ale nie umie wymyślić
// rodzica, którego nie ma.
func (d *Document) UstawPrógPowiadomień(n int) error {
	err := d.SetPathRaw(strconv.Itoa(n), "notify", "threshold")
	if !errors.Is(err, ErrPathNotFound) {
		return err
	}
	if _, e := d.node("notify"); e == nil {
		// blok jest, brakuje samego klucza
		return d.wstawKlucz("threshold", strconv.Itoa(n), "notify")
	}
	return d.wstawBlokNajwyższegoPoziomu(fmt.Sprintf("notify:\n  threshold: %d\n", n))
}

// wstawBlokNajwyższegoPoziomu wstawia gotowy blok PRZED `groups:`.
//
// ! Nie na koniec pliku: oba configi w repo kończą się komentarzem stopki w
// kolumnie 0, a dopisany blok wylądowałby pod nim i ukradł mu sens.
func (d *Document) wstawBlokNajwyższegoPoziomu(blok string) error {
	if off, err := d.offsetKlucza(d.root(), "groups"); err == nil {
		wstaw := d.lineStart(off)
		// komentarz przyklejony nad `groups:` opisuje grupy, nie wygląd —
		// wciągamy go w górę, żeby przy nich został (wzorzec RemoveService)
		for wstaw > 0 {
			poprzedniKoniec := wstaw - 1
			poprzedniStart := d.lineStart(poprzedniKoniec)
			if strings.HasPrefix(strings.TrimSpace(string(d.src[poprzedniStart:poprzedniKoniec])), "#") {
				wstaw = poprzedniStart
				continue
			}
			break
		}
		return d.replaceRange(wstaw, wstaw, []byte(blok+"\n"))
	}

	// plik minimalny, bez `groups:` — dopisujemy na koniec
	koniec := len(d.src)
	przed := "\n"
	if koniec > 0 && d.src[koniec-1] != '\n' {
		przed = "\n\n" // lineEnd zwraca offset '\n'; bez tego blok sklei się z linią
	}
	return d.replaceRange(koniec, koniec, []byte(przed+blok))
}

// UsuńWygląd kasuje cały blok `appearance` — do cofnięcia zmiany z UI.
func (d *Document) UsuńWygląd() error {
	off, err := d.offsetKlucza(d.root(), "appearance")
	if err != nil {
		return err
	}
	start := d.lineStart(off)
	_, próg := d.zakresWpisu(off)
	end := d.blockEnd(off, próg)
	for end < len(d.src) { // zjedz pustą linię po bloku, żeby nie zostawiać dziury
		koniecLinii := d.lineEnd(end)
		if strings.TrimSpace(string(d.src[end:koniecLinii])) != "" {
			break
		}
		end = koniecLinii + 1
	}
	return d.replaceRange(start, end, nil)
}

// MaksTytułu — limit długości tytułu panelu (w znakach, nie bajtach).
const MaksTytułu = 80

var (
	ErrTytułPusty = errors.New("title is empty")
	ErrTytułDługi = errors.New("title is too long")
	ErrTytułZnaki = errors.New("title contains control characters")
)

// NormalizujTytuł przycina i sprawdza tytuł. Zwraca to, co wolno zapisać do
// pliku — jedna linia, bez znaków sterujących.
func NormalizujTytuł(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ErrTytułPusty
	}
	if utf8.RuneCountInString(v) > MaksTytułu {
		return "", ErrTytułDługi
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", ErrTytułZnaki
		}
	}
	return v, nil
}

// UstawTytuł zapisuje top-level `title`. Gdy klucza nie ma, powstaje przed
// `groups:`, jak blok wyglądu. Styl cytowania z pliku wygrywa (SetPath).
func (d *Document) UstawTytuł(tytuł string) error {
	tytuł, err := NormalizujTytuł(tytuł)
	if err != nil {
		return err
	}
	err = d.SetPath(tytuł, "title")
	if !errors.Is(err, ErrPathNotFound) {
		return err
	}
	return d.wstawBlokNajwyższegoPoziomu("title: " + quoteLike(nil, tytuł) + "\n")
}
