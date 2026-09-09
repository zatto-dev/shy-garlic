// Package config czyta i zapisuje konfigurację Shy Garlic.
//
// Dwa niezależne widoki tego samego pliku:
//   - Load() → Config: widok semantyczny, do odczytu (serwer, heartbeat, render).
//   - Parse() → Document: widok tekstowy, do edycji. Zapis to splice bajtowy
//     w oryginale, więc bajty, których nikt nie zmienił, zostają nietknięte.
package config

import (
	"regexp"
	"slices"
	"time"
)

// Domyślne parametry sprawdzania, gdy usługa ich nie podaje.
const (
	DefaultInterval = 30 * time.Second
	DefaultTimeout  = 5 * time.Second
)

// DefaultExpect — kody uznawane za „usługa żyje".
//
// ! 401 i 403 są tu celowo: usługa, która odpowiada „musisz się zalogować",
// żyje. Research konkurencji (2026-08-14) pokazał, że Homepage, Homarr i Glance
// mylą te kody z awarią albo odwrotnie — świecą na zielono cokolwiek wróci.
var DefaultExpect = []int{200, 301, 302, 401, 403}

// ── wygląd ───────────────────────────────────────────────────────────────────
//
// Blok `appearance` to domyślny wygląd CAŁEJ instancji. Plik zostaje źródłem
// prawdy; przeglądarka może go u siebie nadpisać (localStorage), ale to, co tu
// stoi, widzą wszyscy i wszędzie. Wartości są zamknięte allowlistami, bo lądują
// w atrybutach HTML i w regułach CSS.

var (
	PresetyTła = []string{"aurora", "nebula", "graphite", "dawn", "fog", "photo"}
	TonyTła    = []string{"dark", "light"}
	Widoki     = []string{"grid", "bento", "list"}
	Rozmiary   = []string{"sm", "md", "lg"}
)

const (
	MaxBlur     = 30
	MaxDim      = 80
	PresetPhoto = "photo"

	DomyślnyPreset  = "aurora"
	DomyślnyBlur    = 10
	DomyślnyDim     = 50
	DomyślnyTon     = "dark"
	DomyślnyWidok   = "grid"
	DomyślnyRozmiar = "md"

	// DomyślnyPróg — jedno nieudane sprawdzenie to jeszcze nie awaria.
	DomyślnyPróg = 2
)

// TonPresetu — każdy preset ma z góry ustaloną jasność tła, więc niesie ze sobą
// jedyny ton tekstu, w którym da się go czytać. `photo` go nie ma: tam jasność
// zna dopiero przeglądarka, po zmierzeniu luminancji wgranego obrazu.
var TonPresetu = map[string]string{
	"aurora":   "dark",
	"nebula":   "dark",
	"graphite": "dark",
	"dawn":     "light",
	"fog":      "light",
}

// TonDla zwraca ton pasujący do presetu.
//
// ! Bez tego wybór jasnego presetu zostawiał ton po poprzednim, ciemnym — czyli
// jasny tekst na kremowym tle. Ton nie może mieć jednej wartości domyślnej, bo
// poprawna zależy od tego, co jest pod spodem.
func TonDla(preset string) string {
	if t, ok := TonPresetu[preset]; ok {
		return t
	}
	return DomyślnyTon
}

// NazwaTła — jedyne miejsce prawdy o nazwie pliku tapety; używa jej i config,
// i serwer. Alfabet nie zawiera ani jednego znaku o znaczeniu w ścieżce, w CSS
// ani w nagłówku HTTP, więc nazwa, która przez niego przeszła, jest bezpieczna
// wszędzie, gdzie potem trafi.
var NazwaTła = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Dozwolone mówi, czy wartość stoi na liście dopuszczalnych.
func Dozwolone(v string, lista []string) bool { return slices.Contains(lista, v) }

type Config struct {
	Title      string     `yaml:"title"`
	Appearance Appearance `yaml:"appearance"`
	Notify     Notify     `yaml:"notify"`
	Discovery  Discovery  `yaml:"discovery"`
	Groups     []Group    `yaml:"groups"`
}

// Notify — konfiguracja powiadomień, która NIE jest sekretem.
//
// ! Adresu webhooka tu nie ma i nie będzie. Ten plik jest pokazywany w UI i
// commitowany, a URL webhooka jest poświadczeniem — kto go ma, ten pisze na
// kanał. Adres mieszka w SQLite, obok haseł i sesji.
type Notify struct {
	Threshold int `yaml:"threshold"`
}

// Discovery — co panel ma podpowiadać przy dodawaniu usługi.
//
// ! Wskaźnik, bo trzeba odróżnić „nie napisano nic" (wtedy podpowiadamy, jeśli
// gniazdo jest dostępne) od świadomego `docker: false`.
type Discovery struct {
	Docker *bool `yaml:"docker"`
}

type Appearance struct {
	Background Background `yaml:"background"`
	View       string     `yaml:"view"`
}

type Background struct {
	Preset string `yaml:"preset"`
	Image  string `yaml:"image"`
	Blur   int    `yaml:"blur"`
	// Dim — krycie dolnego końca warstwy przyciemniającej, w procentach.
	// Górny koniec to zawsze Dim × 0,56 — proporcja z pierwotnych .28/.5,
	// żeby przyciemnienie narastało ku dołowi, gdzie stoją kafle.
	//
	// ! Sam kolor tekstu nie wystarcza: na zdjęciu o dużym kontraście
	// wewnętrznym część kafli leży na jasnym, a część na ciemnym tle, więc
	// czytelność ratuje dopiero mocniejsze przyciemnienie.
	Dim  int    `yaml:"dim"`
	Tone string `yaml:"tone"`
}

type Group struct {
	Name     string    `yaml:"name"`
	Services []Service `yaml:"services"`
}

type Service struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Icon string `yaml:"icon"`
	// Size — rozmiar kafla w widoku bento; puste znaczy DomyślnyRozmiar.
	Size  string `yaml:"size"`
	Check Check  `yaml:"check"`
}

// Check — semantyka heartbeatu pojedynczej usługi.
type Check struct {
	Interval Duration `yaml:"interval"`
	Timeout  Duration `yaml:"timeout"`
	Expect   []int    `yaml:"expect"`
	// Insecure — homelab często stoi na samopodpisanym certyfikacie
	// (Proxmox, iDRAC, NAS-y). Bez tego panel pokazywałby fałszywe "down".
	Insecure bool `yaml:"insecure"`
}
