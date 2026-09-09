package config

import (
	"fmt"
	"strings"
)

// Diff pokazuje, co dokładnie zmieni się w pliku — w formacie unified, tym
// samym, który każdy zna z gita.
//
// To nie jest ozdoba. Research konkurencji (2026-08-14) wykazał, że obejścia
// dodające edytor UI do paneli YAML-owych nie zdobyły użytkowników nie dlatego,
// że były niepotrzebne, tylko dlatego, że nikt nie ufał im z zapisem do
// własnego pliku. Pokazanie diffa PRZED zapisem jest odpowiedzią na to
// wprost: nie musisz nam wierzyć, po prostu zobacz.
//
// Własna implementacja zamiast biblioteki, bo pliki konfiguracyjne mają
// kilkaset linii, a zależność mniej to zależność mniej.
func Diff(stary, nowy []byte, kontekst int) string {
	if kontekst < 0 {
		kontekst = 3
	}
	a := podzielLinie(stary)
	b := podzielLinie(nowy)
	if równe(a, b) {
		return ""
	}

	wspólne := lcs(a, b)
	kroki := ścieżka(a, b, wspólne)
	return sklejHunki(a, b, kroki, kontekst)
}

func podzielLinie(b []byte) []string {
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func równe(a, b []string) bool {
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

// lcs buduje tablicę długości najdłuższego wspólnego podciągu.
// O(n·m) — przy plikach rzędu setek linii to mikrosekundy.
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

type krok struct {
	znak  byte // ' ', '-', '+'
	tekst string
}

func ścieżka(a, b []string, t [][]int) []krok {
	var out []krok
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, krok{' ', a[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			out = append(out, krok{'-', a[i]})
			i++
		default:
			out = append(out, krok{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, krok{'-', a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, krok{'+', b[j]})
	}
	return out
}

// sklejHunki wycina z pełnej ścieżki tylko fragmenty ze zmianami plus kontekst.
func sklejHunki(a, b []string, kroki []krok, kontekst int) string {
	// które kroki są interesujące
	ważne := make([]bool, len(kroki))
	for i, k := range kroki {
		if k.znak == ' ' {
			continue
		}
		od := max(0, i-kontekst)
		do := min(len(kroki)-1, i+kontekst)
		for j := od; j <= do; j++ {
			ważne[j] = true
		}
	}

	var b3 strings.Builder
	i := 0
	liniaA, liniaB := 1, 1
	for i < len(kroki) {
		if !ważne[i] {
			switch kroki[i].znak {
			case ' ':
				liniaA++
				liniaB++
			case '-':
				liniaA++
			case '+':
				liniaB++
			}
			i++
			continue
		}

		początekA, początekB := liniaA, liniaB
		var ileA, ileB int
		var treść strings.Builder
		for i < len(kroki) && ważne[i] {
			k := kroki[i]
			treść.WriteByte(k.znak)
			treść.WriteString(k.tekst)
			treść.WriteByte('\n')
			switch k.znak {
			case ' ':
				ileA++
				ileB++
				liniaA++
				liniaB++
			case '-':
				ileA++
				liniaA++
			case '+':
				ileB++
				liniaB++
			}
			i++
		}
		fmt.Fprintf(&b3, "@@ -%d,%d +%d,%d @@\n", początekA, ileA, początekB, ileB)
		b3.WriteString(treść.String())
	}
	return b3.String()
}

// PodsumujDiff opisuje zmianę jednym zdaniem: ile linii doszło, ile zniknęło.
func PodsumujDiff(d string) (dodane, usunięte int) {
	for _, l := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			dodane++
		case strings.HasPrefix(l, "-"):
			usunięte++
		}
	}
	return
}
