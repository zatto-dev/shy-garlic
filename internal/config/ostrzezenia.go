package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// Dwie rzeczy, które mogą być nie tak z plikiem, mają różną wagę:
//   - błąd (Load zwraca error): nie da się go wczytać, panel działa dalej na
//     poprzedniej konfiguracji i pokazuje komunikat z numerem linii;
//   - ostrzeżenie (poniżej): plik się wczytuje, ale coś w nim nie robi tego,
//     co autor myślał — literówka w kluczu (`chek:`) albo adres, którego nie da
//     się sprawdzić. Bez tego parser po cichu ignorował pole.

const (
	RodzajKlucz = "klucz"
	RodzajAdres = "adres"
)

// Ostrzeżenie opisuje jedno podejrzane miejsce w configu. Struktura, nie
// gotowy tekst: komunikat składa serwer, w języku przeglądarki.
type Ostrzeżenie struct {
	Rodzaj   string
	Linia    int    // 0 = nieznana
	Ścieżka  string // np. groups[0].services[1]
	Klucz    string // nieznany klucz
	Sugestia string // najbliższy znany klucz, jeśli podobny
	Grupa    string // dla RodzajAdres
	Usługa   string
	Adres    string
}

type schemat struct {
	klucze map[string]*schemat
	lista  *schemat
}

var liść = &schemat{}

func obiekt(k map[string]*schemat) *schemat { return &schemat{klucze: k} }

// schematConfigu odwzorowuje model z model.go. ! Dopisując pole do Config,
// dopisz je i tu — inaczej każdy użytkownik dostanie ostrzeżenie o poprawnym
// kluczu (pilnuje tego TestOstrzeżeniaPokrywająModel).
var schematConfigu = obiekt(map[string]*schemat{
	"title": liść,
	"appearance": obiekt(map[string]*schemat{
		"view": liść,
		"background": obiekt(map[string]*schemat{
			"preset": liść, "image": liść, "blur": liść, "dim": liść, "tone": liść,
		}),
	}),
	"notify":    obiekt(map[string]*schemat{"threshold": liść}),
	"discovery": obiekt(map[string]*schemat{"docker": liść}),
	"groups": {lista: obiekt(map[string]*schemat{
		"name": liść,
		"services": {lista: obiekt(map[string]*schemat{
			"name": liść, "url": liść, "icon": liść, "size": liść,
			"description": liść, "tags": liść,
			"check": obiekt(map[string]*schemat{
				"interval": liść, "timeout": liść, "expect": liść, "insecure": liść,
			}),
		})},
	})},
})

// kluczeNiezgłaszane — miejsca na anchory. `defaults: &defaults` stoi w
// README i w przykładach, a `x-*` to konwencja z Docker Compose.
func klucząNiezgłaszany(poziomGłówny bool, k string) bool {
	return poziomGłówny && (k == "defaults" || strings.HasPrefix(k, "x-"))
}

const maxGłębokość = 32 // ochrona przed aliasami odwołującymi się do siebie

type chodzik struct {
	kotwice map[string]ast.Node
	widziane map[string]bool
	wynik   []Ostrzeżenie
}

type zbieraczKotwic struct{ k map[string]ast.Node }

func (z zbieraczKotwic) Visit(n ast.Node) ast.Visitor {
	if a, ok := n.(*ast.AnchorNode); ok {
		z.k[scalar(a.Name)] = a.Value
	}
	return z
}

// Ostrzeżenia szuka w źródle kluczy, których model nie zna. Niepoprawny YAML
// daje pustą listę — o tym informuje Load.
func Ostrzeżenia(src []byte) []Ostrzeżenie {
	f, err := parser.ParseBytes(src, 0)
	if err != nil || len(f.Docs) == 0 || f.Docs[0].Body == nil {
		return nil
	}
	c := &chodzik{kotwice: map[string]ast.Node{}, widziane: map[string]bool{}}
	ast.Walk(zbieraczKotwic{c.kotwice}, f.Docs[0].Body)
	c.węzeł(f.Docs[0].Body, schematConfigu, "", 0)
	return c.wynik
}

// rozwiń zdejmuje anchory i tagi oraz podąża za aliasem do jego celu.
func (c *chodzik) rozwiń(n ast.Node) ast.Node {
	for i := 0; i < maxGłębokość; i++ {
		switch v := n.(type) {
		case *ast.AnchorNode:
			n = v.Value
		case *ast.TagNode:
			n = v.Value
		case *ast.AliasNode:
			cel, ok := c.kotwice[scalar(v.Value)]
			if !ok {
				return nil
			}
			n = cel
		default:
			return n
		}
	}
	return nil
}

func (c *chodzik) węzeł(n ast.Node, s *schemat, ścieżka string, głębia int) {
	if głębia > maxGłębokość || s == nil {
		return
	}
	switch v := c.rozwiń(n).(type) {
	case *ast.MappingNode:
		for _, kv := range v.Values {
			c.wpis(kv, s, ścieżka, głębia)
		}
	case *ast.MappingValueNode:
		c.wpis(v, s, ścieżka, głębia)
	case *ast.SequenceNode:
		if s.lista == nil {
			return
		}
		for i, e := range v.Values {
			c.węzeł(e, s.lista, ścieżka+"["+strconv.Itoa(i)+"]", głębia+1)
		}
	}
}

func (c *chodzik) wpis(kv *ast.MappingValueNode, s *schemat, ścieżka string, głębia int) {
	if _, merge := kv.Key.(*ast.MergeKeyNode); merge {
		// `<<: *a` albo `<<: [*a, *b]` — klucze celu należą do TEGO mappingu
		if seq, ok := c.rozwiń(kv.Value).(*ast.SequenceNode); ok {
			for _, e := range seq.Values {
				c.węzeł(e, s, ścieżka, głębia+1)
			}
			return
		}
		c.węzeł(kv.Value, s, ścieżka, głębia+1)
		return
	}
	klucz := scalar(kv.Key)
	if s.klucze == nil { // liść z mappingiem w środku — nic do sprawdzenia
		return
	}
	pod, znany := s.klucze[klucz]
	if !znany {
		if klucząNiezgłaszany(ścieżka == "", klucz) {
			return
		}
		linia := 0
		if t := kv.Key.GetToken(); t != nil {
			linia = t.Position.Line
		}
		// ten sam anchor wciągnięty przez N merge'y to jedno ostrzeżenie
		id := strconv.Itoa(linia) + ":" + klucz
		if c.widziane[id] {
			return
		}
		c.widziane[id] = true
		c.wynik = append(c.wynik, Ostrzeżenie{
			Rodzaj: RodzajKlucz, Linia: linia, Ścieżka: ścieżka, Klucz: klucz,
			Sugestia: najbliższy(klucz, s.klucze),
		})
		return
	}
	nowa := klucz
	if ścieżka != "" {
		nowa = ścieżka + "." + klucz
	}
	c.węzeł(kv.Value, pod, nowa, głębia+1)
}

// najbliższy zwraca znany klucz oddalony o najwyżej 2 edycje (i krótszy niż
// połowa długości, żeby `x` nie podpowiadało `url`).
func najbliższy(klucz string, znane map[string]*schemat) string {
	najlepszy, naj := "", 3
	for k := range znane {
		if d := odległość(klucz, k); d < naj || (d == naj && d < 3 && k < najlepszy) {
			najlepszy, naj = k, d
		}
	}
	if naj <= 2 && naj*2 < len(klucz)+1 {
		return najlepszy
	}
	return ""
}

func odległość(a, b string) int {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	poprz := make([]int, len(rb)+1)
	for j := range poprz {
		poprz[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		bież := make([]int, len(rb)+1)
		bież[0] = i
		for j := 1; j <= len(rb); j++ {
			koszt := 1
			if ra[i-1] == rb[j-1] {
				koszt = 0
			}
			bież[j] = min(poprz[j]+1, bież[j-1]+1, poprz[j-1]+koszt)
		}
		poprz = bież
	}
	return poprz[len(rb)]
}

// OstrzeżeniaAdresów wskazuje usługi z adresem, którego nie da się ani
// sprawdzić, ani otworzyć (np. `javascript:`). API takich nie przyjmie, ale
// plik można zredagować ręcznie.
func OstrzeżeniaAdresów(cfg *Config) []Ostrzeżenie {
	var out []Ostrzeżenie
	if cfg == nil {
		return nil
	}
	for _, g := range cfg.Groups {
		for _, s := range g.Services {
			if !PoprawnyAdresUsługi(s.URL) {
				out = append(out, Ostrzeżenie{Rodzaj: RodzajAdres, Grupa: g.Name, Usługa: s.Name, Adres: s.URL})
			}
		}
	}
	return out
}

// BłądPliku — niepoprawny plik w formie, którą da się pokazać człowiekowi.
type BłądPliku struct {
	Linia, Kolumna int // 0 = nieznane
	Komunikat      string
}

var wzorPozycji = regexp.MustCompile(`^\[(\d+):(\d+)\]\s*`)

// BłądWczytania to błąd z Load razem z pozycją w pliku. Error() zostaje takie
// samo jak dawniej ("parse config: …"); numer linii jest do odczytania przez
// RozbierzBłąd.
type BłądWczytania struct {
	BłądPliku
	err error
}

func (e *BłądWczytania) Error() string { return e.err.Error() }
func (e *BłądWczytania) Unwrap() error { return e.err }

var wzorCzasu = regexp.MustCompile(`invalid duration "([^"]*)"`)

// lokalizuj szuka linii dla błędów, które parser zgłasza bez pozycji —
// własny UnmarshalYAML (Duration) nie dostaje tokenu. Zgaduje po treści błędu:
// pierwsza linia `interval:`/`timeout:` z tą wartością.
func lokalizuj(src []byte, komunikat string) int {
	m := wzorCzasu.FindStringSubmatch(komunikat)
	if m == nil {
		return 0
	}
	wz, err := regexp.Compile(`^\s*(?:-\s+)?(?:interval|timeout)\s*:\s*["']?` + regexp.QuoteMeta(m[1]) + `["']?\s*(?:#.*)?$`)
	if err != nil {
		return 0
	}
	for i, l := range strings.Split(string(src), "\n") {
		if wz.MatchString(strings.TrimRight(l, "\r")) {
			return i + 1
		}
	}
	return 0
}

// RozbierzBłąd wyciąga z błędu parsera numer linii i krótki komunikat. goccy
// w Error() dokleja fragment źródła z kolorami — tu go nie chcemy.
func RozbierzBłąd(err error) BłądPliku {
	var bw *BłądWczytania
	if errors.As(err, &bw) {
		return bw.BłądPliku
	}
	return rozbierzSurowy(err)
}

func rozbierzSurowy(err error) BłądPliku {
	var b BłądPliku
	var ye yaml.Error
	if errors.As(err, &ye) {
		b.Komunikat = ye.GetMessage()
		if t := ye.GetToken(); t != nil && t.Position != nil {
			b.Linia, b.Kolumna = t.Position.Line, t.Position.Column
		}
	}
	if b.Komunikat == "" {
		b.Komunikat = err.Error()
	}
	// wpis „[3:5] komunikat\n  źródło…" — jedyne, co mamy bez tokenu
	if b.Linia == 0 {
		if m := wzorPozycji.FindStringSubmatch(b.Komunikat); m != nil {
			b.Linia, _ = strconv.Atoi(m[1])
			b.Kolumna, _ = strconv.Atoi(m[2])
			b.Komunikat = b.Komunikat[len(m[0]):]
		}
	}
	if i := strings.IndexByte(b.Komunikat, '\n'); i >= 0 {
		b.Komunikat = b.Komunikat[:i]
	}
	b.Komunikat = strings.TrimSpace(wzorPozycji.ReplaceAllString(b.Komunikat, ""))
	return b
}
