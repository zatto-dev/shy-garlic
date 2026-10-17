package config

import (
	"fmt"
	"strings"
)

// Diff shows exactly what will change in the file — in unified format, the
// same one everybody knows from git.
//
// This is not decoration. Competitor research (2026-08-14) showed that
// workarounds adding a UI editor to YAML-driven dashboards did not win users
// not because they were unnecessary, but because nobody trusted them to write
// to their own file. Showing the diff BEFORE saving answers that directly:
// you don't have to take our word for it, just look.
//
// Own implementation instead of a library, because config files are a few
// hundred lines long, and one dependency less is one dependency less.
func Diff(oldValue, newValue []byte, context int) string {
	if context < 0 {
		context = 3
	}
	a := splitLines(oldValue)
	b := splitLines(newValue)
	if equalLines(a, b) {
		return ""
	}

	common := lcs(a, b)
	steps := editScript(a, b, common)
	return joinHunks(a, b, steps, context)
}

func splitLines(b []byte) []string {
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lcs builds the table of longest-common-subsequence lengths.
// O(n·m) — for files of hundreds of lines that is microseconds.
func lcs(a, b []string) [][]int {
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}
	return t
}

type step struct {
	char byte // ' ', '-', '+'
	text string
}

func editScript(a, b []string, t [][]int) []step {
	var out []step
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, step{' ', a[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			out = append(out, step{'-', a[i]})
			i++
		default:
			out = append(out, step{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, step{'-', a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, step{'+', b[j]})
	}
	return out
}

// joinHunks cuts from the full edit script only the fragments with changes plus context.
func joinHunks(a, b []string, steps []step, context int) string {
	// which steps are interesting
	keep := make([]bool, len(steps))
	for i, k := range steps {
		if k.char == ' ' {
			continue
		}
		from := max(0, i-context)
		to := min(len(steps)-1, i+context)
		for j := from; j <= to; j++ {
			keep[j] = true
		}
	}

	var b3 strings.Builder
	i := 0
	lineA, lineB := 1, 1
	for i < len(steps) {
		if !keep[i] {
			switch steps[i].char {
			case ' ':
				lineA++
				lineB++
			case '-':
				lineA++
			case '+':
				lineB++
			}
			i++
			continue
		}

		beginA, beginB := lineA, lineB
		var countA, countB int
		var content strings.Builder
		for i < len(steps) && keep[i] {
			k := steps[i]
			content.WriteByte(k.char)
			content.WriteString(k.text)
			content.WriteByte('\n')
			switch k.char {
			case ' ':
				countA++
				countB++
				lineA++
				lineB++
			case '-':
				countA++
				lineA++
			case '+':
				countB++
				lineB++
			}
			i++
		}
		fmt.Fprintf(&b3, "@@ -%d,%d +%d,%d @@\n", beginA, countA, beginB, countB)
		b3.WriteString(content.String())
	}
	return b3.String()
}

// SummarizeDiff describes the change in one sentence: how many lines were added, how many removed.
func SummarizeDiff(d string) (added, removed int) {
	for _, l := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}
