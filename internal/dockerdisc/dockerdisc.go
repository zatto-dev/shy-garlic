// Package dockerdisc podpowiada usługi na podstawie kontenerów działających
// na hoście.
//
// Uwaga na granicę: my nie zarządzamy Dockerem i nie zamierzamy. Czytamy listę
// kontenerów, proponujemy gotowe wpisy, a użytkownik je akceptuje — i dopiero
// wtedy trafiają do jego pliku. Plik zostaje źródłem prawdy, panel nie zaczyna
// go generować za plecami użytkownika.
//
// Gniazdo Dockera jest montowane tylko do odczytu i tylko jeśli ktoś tego chce.
// Bez gniazda funkcja po prostu nie istnieje — żadnych błędów.
package dockerdisc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DomyślneGniazdo — standardowa ścieżka gniazda Dockera.
const DomyślneGniazdo = "/var/run/docker.sock"

// Prefiks etykiet, którymi kontener może sam powiedzieć, jak ma wyglądać w panelu.
const prefiks = "shyg."

type Klient struct {
	gniazdo string // ścieżka gniazda uniksowego (tryb lokalny)
	baza    string // adres bazowy HTTP (tryb proxy)
	http    *http.Client
}

// Nowy przyjmuje albo ścieżkę gniazda, albo adres http(s) — ten drugi wariant
// jest dla socket-proxy.
//
// ! Gniazdo Dockera zamontowane wprost do kontenera z panelem wystawionym do
// internetu daje w praktyce władzę nad całym hostem, jeśli ktoś znajdzie dziurę
// w panelu. Proxy ograniczone do odczytu listy kontenerów
// (np. tecnativa/docker-socket-proxy z CONTAINERS=1) zamyka ten problem, więc
// obsługujemy oba warianty i w dokumentacji polecamy ten drugi.
func Nowy(adres string) *Klient {
	if strings.HasPrefix(adres, "http://") || strings.HasPrefix(adres, "https://") ||
		strings.HasPrefix(adres, "tcp://") {
		baza := strings.Replace(adres, "tcp://", "http://", 1)
		return &Klient{
			baza: strings.TrimRight(baza, "/"),
			http: &http.Client{Timeout: 8 * time.Second},
		}
	}
	if adres == "" {
		adres = DomyślneGniazdo
	}
	return &Klient{
		gniazdo: adres,
		http: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", adres)
				},
			},
		},
	}
}

// Dostępny mówi, czy w ogóle mamy do czego zaglądać.
func (k *Klient) Dostępny() bool {
	if k.baza != "" {
		return true // proxy sprawdzimy dopiero przy pytaniu — może wstawać wolniej
	}
	fi, err := os.Stat(k.gniazdo)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

func (k *Klient) adresListy() string {
	if k.baza != "" {
		return k.baza + "/containers/json"
	}
	return "http://docker/containers/json"
}

// Kandydat to propozycja wpisu do configu, jeszcze niczyja.
type Kandydat struct {
	Nazwa  string `json:"name"`
	URL    string `json:"url"`
	Ikona  string `json:"icon"`
	Grupa  string `json:"group,omitempty"`
	Obraz  string `json:"image"`
	Porty  []int  `json:"ports"`
	// Powód to kod, nie zdanie: tłumaczy go interfejs, bo ten pakiet nie
	// ma prawa decydować o języku panelu.
	Powód string `json:"why"` // label | port | noport
}

type kontener struct {
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

// Kandydaci czyta kontenery i zamienia je na propozycje. `host` to nazwa hosta,
// pod którą użytkownik ogląda panel — zakładamy, że kontenery na tej samej
// maszynie są pod tym samym adresem.
func (k *Klient) Kandydaci(ctx context.Context, host string) ([]Kandydat, error) {
	if !k.Dostępny() {
		return nil, fmt.Errorf("brak gniazda Dockera (%s)", k.gniazdo)
	}
	// ! bez numeru wersji w ścieżce. Demon odrzuca wersje starsze niż jego
	// MinAPIVersion (Docker 29 wymaga ≥1.44), a ścieżka bez wersji zawsze
	// trafia w domyślną wersję tego demona.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.adresListy(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker: %s", resp.Status)
	}

	var lista []kontener
	if err := json.NewDecoder(resp.Body).Decode(&lista); err != nil {
		return nil, err
	}

	var out []Kandydat
	for _, c := range lista {
		if c.State != "running" {
			continue
		}
		if c.Labels[prefiks+"enable"] == "false" {
			continue
		}
		if kand, ok := zKontenera(c, host); ok {
			out = append(out, kand)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nazwa < out[j].Nazwa })
	return out, nil
}

func zKontenera(c kontener, host string) (Kandydat, bool) {
	nazwa := ładnaNazwa(c)
	if v := c.Labels[prefiks+"name"]; v != "" {
		nazwa = v
	}

	// porty wystawione na hoście — tylko takie mają sens jako adres
	var porty []int
	for _, p := range c.Ports {
		if p.Type == "tcp" && p.PublicPort > 0 {
			porty = append(porty, p.PublicPort)
		}
	}
	sort.Ints(porty)
	porty = bezPowtórek(porty)

	k := Kandydat{
		Nazwa: nazwa,
		Obraz: bezTagu(c.Image),
		Ikona: zgadnijIkonę(c),
		Grupa: c.Labels[prefiks+"group"],
		Porty: porty,
	}

	switch {
	case c.Labels[prefiks+"url"] != "":
		k.URL = c.Labels[prefiks+"url"]
		k.Powód = "label"
	case len(porty) > 0:
		k.URL = fmt.Sprintf("http://%s:%d", host, najlepszyPort(porty))
		k.Powód = "port"
	default:
		// kontener bez portu na hoście — nie zgadujemy adresu, ale pokazujemy
		// go, bo user może mieć własny reverse proxy
		k.Powód = "noport"
	}
	return k, true
}

// najlepszyPort wybiera ten, który najpewniej jest interfejsem webowym.
func najlepszyPort(porty []int) int {
	popularne := map[int]int{80: 100, 8080: 90, 443: 85, 3000: 80, 8000: 75, 8096: 70, 9000: 65}
	najlepszy, punkty := porty[0], -1
	for _, p := range porty {
		w := popularne[p]
		if w == 0 {
			// wysokie porty efemeryczne są mniej prawdopodobne niż niskie
			w = 50 - p/2000
		}
		if w > punkty {
			najlepszy, punkty = p, w
		}
	}
	return najlepszy
}

func ładnaNazwa(c kontener) string {
	n := "kontener"
	if len(c.Names) > 0 {
		n = strings.TrimPrefix(c.Names[0], "/")
	}
	// „media-jellyfin-1" → „Jellyfin”: tnij przedrostek stacka i sufiks repliki
	części := strings.Split(n, "-")
	if len(części) > 1 {
		if _, err := strconv.Atoi(części[len(części)-1]); err == nil {
			części = części[:len(części)-1]
		}
	}
	if len(części) > 1 {
		n = części[len(części)-1]
	} else {
		n = części[0]
	}
	if n == "" {
		return "kontener"
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

func bezTagu(obraz string) string {
	if i := strings.LastIndex(obraz, ":"); i > strings.LastIndex(obraz, "/") {
		obraz = obraz[:i]
	}
	return obraz
}

// zgadnijIkonę bierze ostatni człon nazwy obrazu — „ghcr.io/gethomepage/homepage"
// daje „homepage", co zwykle trafia w nazwę z katalogu ikon.
func zgadnijIkonę(c kontener) string {
	if v := c.Labels[prefiks+"icon"]; v != "" {
		return v
	}
	obraz := bezTagu(c.Image)
	if i := strings.LastIndex(obraz, "/"); i >= 0 {
		obraz = obraz[i+1:]
	}
	return strings.ToLower(obraz)
}

func bezPowtórek(x []int) []int {
	if len(x) < 2 {
		return x
	}
	out := x[:1]
	for _, v := range x[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
