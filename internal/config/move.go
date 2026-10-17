package config

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// Reordering: services within a group, services between groups, the groups themselves.
//
// Every move is two splices on the original text — cutting the block and inserting
// it in the new place. The block carries comments glued above the entry and fields
// the panel does not know; nothing is rebuilt from the model.

// listElement is the byte range of one list element (`- name: ...`) together with the
// comment glued above it, without the blank lines after it.
type listElement struct {
	begin, end, dash int
}

// startWithComment moves the line start back over comments standing directly
// above the entry — the same ones RemoveService takes.
//
// ! Only a comment standing in the column of the entry's dash belongs to the entry. A
// comment indented deeper is the tail of the PREVIOUS entry (e.g. a commented-out service under
// the group's last service) and must stay where it is.
func (d *Document) startWithComment(off, dash int) int {
	start := d.lineStart(off)
	for start > 0 {
		prevKey := start - 1
		ps := d.lineStart(prevKey)
		if strings.HasPrefix(strings.TrimSpace(string(d.src[ps:prevKey])), "#") && d.lineIndent(ps) == dash {
			start = ps
			continue
		}
		break
	}
	return start
}

func (d *Document) listElements(seq *ast.SequenceNode) ([]listElement, error) {
	out := make([]listElement, 0, len(seq.Values))
	for _, v := range seq.Values {
		off, err := d.offsetOf(v)
		if err != nil {
			return nil, err
		}
		dash := d.dashIndentOf(off)
		out = append(out, listElement{d.startWithComment(off, dash), d.blockEnd(off, dash), dash})
	}
	return out, nil
}

// delimitedList says whether the user separates elements with a blank line —
// we look at the second and later ones, because the first usually stands under the key.
func (d *Document) delimitedList(el []listElement) bool {
	for _, e := range el[min(1, len(el)):] {
		if d.precededByBlankLine(e.begin) {
			return true
		}
	}
	return false
}

func (d *Document) blankLineFrom(off int) (end int, ok bool) {
	if off >= len(d.src) {
		return off, false
	}
	lineEnd := d.lineEnd(off)
	return lineEnd + 1, strings.TrimSpace(string(d.src[off:lineEnd])) == ""
}

// cutElement cuts the element out and returns its lines with the indent removed.
//
// Blank lines: a middle element takes the one after it with it. The last one leaves
// a blank line behind (it is what separates the list from the next group) and takes
// its own preceding one, so no double gap appears.
func (d *Document) cutElement(seq *ast.SequenceNode, i int) ([]string, error) {
	el, err := d.listElements(seq)
	if err != nil {
		return nil, err
	}
	e := el[i]
	lines := []string{}
	for _, l := range strings.Split(strings.TrimRight(string(d.src[e.begin:e.end]), "\n"), "\n") {
		if len(l) >= e.dash && strings.TrimSpace(l[:e.dash]) == "" {
			l = l[e.dash:]
		} else {
			l = strings.TrimLeft(l, " ")
		}
		lines = append(lines, l)
	}

	start, end := e.begin, e.end
	if i < len(el)-1 {
		for {
			next, blank := d.blankLineFrom(end)
			if !blank {
				break
			}
			end = next
		}
	} else {
		_, afterBlank := d.blankLineFrom(end)
		if afterBlank || end >= len(d.src) {
			for start > 0 {
				prevKey := start - 1
				ps := d.lineStart(prevKey)
				if strings.TrimSpace(string(d.src[ps:prevKey])) != "" {
					break
				}
				start = ps
			}
		}
	}
	if err := d.replaceRange(start, end, nil); err != nil {
		return nil, err
	}
	return lines, nil
}

// insertElement inserts lines as the element at the given index (len = at the end).
func (d *Document) insertElement(seq *ast.SequenceNode, index int, lines []string, split bool) error {
	el, err := d.listElements(seq)
	if err != nil {
		return err
	}
	dash := el[0].dash
	var b strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", dash) + l + "\n")
	}
	block := b.String()

	if index < len(el) {
		if split {
			block += "\n"
		}
		atIdx := el[index].begin
		return d.replaceRange(atIdx, atIdx, []byte(block))
	}
	atIdx := el[len(el)-1].end
	if split {
		block = "\n" + block
	}
	if atIdx > 0 && d.src[atIdx-1] != '\n' {
		block = "\n" + block // file without a trailing \n
	}
	return d.replaceRange(atIdx, atIdx, []byte(block))
}

func (d *Document) sequence(path ...any) (*ast.SequenceNode, bool) {
	n, err := d.node(path...)
	if err != nil {
		return nil, false
	}
	seq, ok := unwrap(n).(*ast.SequenceNode)
	return seq, ok
}

var errIndex = ErrPosition

// MoveServiceTo moves the service to group toGroup at position index. The index is
// the service's place in the target list AFTER the move (0 = first, len = last),
// so the same index works for reordering within a group and for moving between groups.
// A move to the place where the service already stands does not change a single byte.
func (d *Document) MoveServiceTo(fromGroup, service, toGroup string, index int) error {
	gi, si, err := d.findService(fromGroup, service)
	if err != nil {
		return err
	}
	titleIdx, err := d.findGroup(toGroup)
	if err != nil {
		return err
	}
	same := gi == titleIdx
	if !same {
		if _, _, err := d.findService(toGroup, service); err == nil {
			return fmt.Errorf("%w: service %q in group %q", ErrDuplicate, service, toGroup)
		}
	}
	z, _ := d.sequence("groups", gi, "services")
	target, hasTarget := d.sequence("groups", titleIdx, "services")
	targetLen := 0
	if hasTarget {
		targetLen = len(target.Values)
	}
	if same {
		targetLen-- // the service leaves the list before we insert it back
	}
	if index < 0 || index > targetLen {
		return fmt.Errorf("%w: %d (0..%d)", errIndex, index, targetLen)
	}
	if same && index == si {
		return nil
	}

	split := false
	if hasTarget && len(target.Values) > 0 {
		el, err := d.listElements(target)
		if err != nil {
			return err
		}
		split = d.delimitedList(el)
	}

	lines, err := d.cutElement(z, si)
	if err != nil {
		return err
	}
	if len(z.Values) == 1 {
		if err := d.setEmptyList(fromGroup); err != nil {
			return err
		}
	}
	target, hasTarget = d.sequence("groups", titleIdx, "services")
	if !hasTarget || len(target.Values) == 0 {
		return d.insertBlockIntoEmpty(titleIdx, lines)
	}
	return d.insertElement(target, index, lines, split)
}

// MoveGroup moves the group to position index (after the move), together with
// its services and the comment above it.
func (d *Document) MoveGroup(name string, index int) error {
	gi, err := d.findGroup(name)
	if err != nil {
		return err
	}
	seq, ok := d.sequence("groups")
	if !ok {
		return fmt.Errorf("%w: groups is not a list", ErrPathNotFound)
	}
	if index < 0 || index > len(seq.Values)-1 {
		return fmt.Errorf("%w: %d (0..%d)", errIndex, index, len(seq.Values)-1)
	}
	if index == gi {
		return nil
	}
	el, err := d.listElements(seq)
	if err != nil {
		return err
	}
	split := d.delimitedList(el)
	lines, err := d.cutElement(seq, gi)
	if err != nil {
		return err
	}
	seq, _ = d.sequence("groups")
	return d.insertElement(seq, index, lines, split)
}
