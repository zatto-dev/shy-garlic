package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Ikony nigdy nie są pobierane przez przeglądarkę użytkownika.
//
// ! Cała konkurencja wstawia w HTML <img src="https://cdn.jsdelivr.net/...">.
// To znaczy, że przeglądarka odpytuje obcy CDN o *nazwy usług, które masz w
// domu* — jsDelivr (i Cloudflare przed nim) widzi, że ktoś z Twojego IP ciągnie
// vaultwarden.svg i proxmox.svg. Do tego panel bez internetu traci ikony.
//
// Tutaj jest inaczej: przeglądarka pyta wyłącznie nasz serwer (/icon/<nazwa>),
// a serwer albo ma ikonę w cache na dysku, albo dociąga ją raz z CDN i
// zapisuje. Po pierwszym użyciu działa offline, a CDN nigdy nie widzi
// przeglądarki użytkownika — widzi jeden serwer bez kontekstu.
//
// Tryb `zip` (opcjonalny plik icons.zip) daje komplet od razu, bez ani jednego
// zapytania na zewnątrz — dla sieci odciętych od internetu.

const (
	cdnIkony  = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons@main"
	// ! tree.json w repo bywa nieaktualny — audyt 2026-08-14 pokazał 2799 pozycji
	// w tree.json przy 3363 plikach SVG w katalogu. Dlatego pytamy najpierw API
	// gita o prawdziwe drzewo, a tree.json został jako zapas.
	drzewoGitURL   = "https://api.github.com/repos/homarr-labs/dashboard-icons/git/trees/main?recursive=1"
	metadaneURL    = "https://raw.githubusercontent.com/homarr-labs/dashboard-icons/main/metadata.json"
	drzewoURL      = "https://raw.githubusercontent.com/homarr-labs/dashboard-icons/main/tree.json"
	ważnośćIndeksu = 7 * 24 * time.Hour
)

// wariantTonu to dwie osobne grafiki tej samej ikony — z ciemnym i z jasnym
// tuszem — jakie Dashboard Icons trzyma dla części pozycji właśnie po to, żeby
// dało się dobrać właściwą do tła. Ciemny tusz ginie na ciemnym motywie i
// odwrotnie, więc nazwa pliku ma znaczenie:
//   - Ciemny — plik z ciemnym tuszem, czytelny na JASNYM tle (to jest
//     metadata.json → colors.dark)
//   - Jasny  — plik z jasnym tuszem, czytelny na CIEMNYM tle (metadata.json →
//     colors.light)
type wariantTonu struct {
	Ciemny string
	Jasny  string
}

type Katalog struct {
	mu       sync.RWMutex
	nazwy    []string          // posortowane, bez rozszerzeń
	rozsz    map[string]string // nazwa → "svg" | "png"
	cacheDir string
	tryb     string // "zip" albo "cdn"

	aliasy   map[string][]string    // nazwa → inne nazwy, po których też można ją znaleźć
	warianty map[string]wariantTonu // nazwa → osobne pliki na ciemny/jasny motyw panelu (gdy są)
	zipIdx   map[string]*zip.File
	client   *http.Client
}

func NowyKatalog(zipŚcieżka, cacheDir string) *Katalog {
	k := &Katalog{
		rozsz:    map[string]string{},
		cacheDir: cacheDir,
		tryb:     "cdn",
		client:   &http.Client{Timeout: 20 * time.Second},
	}
	if zipŚcieżka != "" {
		if err := k.wczytajZip(zipŚcieżka); err != nil {
			slog.Warn("nie wczytałem icons.zip, przechodzę na CDN", "plik", zipŚcieżka, "err", err)
		} else {
			k.tryb = "zip"
		}
	}
	if k.cacheDir != "" {
		_ = os.MkdirAll(k.cacheDir, 0o755)
	}
	return k
}

func (k *Katalog) Tryb() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.tryb
}

func (k *Katalog) Liczba() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.nazwy)
}

// Warianty zwraca nazwy plików z ciemnym i jasnym tuszem dla danej ikony, gdy
// Dashboard Icons trzyma taką parę. Pusty string w obu polach = ta ikona nie
// ma osobnych wariantów, wołający ma użyć zwykłej nazwy bazowej.
func (k *Katalog) Warianty(nazwa string) (ciemny, jasny string) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	w, ok := k.warianty[nazwa]
	if !ok || w.Ciemny == "" || w.Jasny == "" {
		return "", ""
	}
	return w.Ciemny, w.Jasny
}

// ── tryb lokalny (zip) ───────────────────────────────────────────────────────

func (k *Katalog) wczytajZip(ścieżka string) error {
	r, err := zip.OpenReader(ścieżka)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.zipIdx = make(map[string]*zip.File, len(r.File))
	k.nazwy = nil
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, ".svg") {
			continue
		}
		k.zipIdx[f.Name] = f
		nazwa := strings.TrimSuffix(f.Name, ".svg")
		k.nazwy = append(k.nazwy, nazwa)
		k.rozsz[nazwa] = "svg"
	}
	sort.Strings(k.nazwy)
	slog.Info("katalog ikon z pliku", "plik", ścieżka, "ikon", len(k.nazwy))
	return nil
}

// ── tryb CDN ─────────────────────────────────────────────────────────────────

type drzewo struct {
	SVG []string `json:"svg"`
	PNG []string `json:"png"`
}

// Odśwież ładuje spis ikon: z dysku, jeśli świeży, inaczej z sieci.
// Brak internetu nie jest błędem krytycznym — panel działa dalej, a wybierak
// pokazuje to, co już leży w cache.
func (k *Katalog) Odśwież(ctx context.Context) {
	if k.Tryb() == "zip" {
		return
	}
	k.odświeżSpisNazw(ctx)

	// Warianty jasny/ciemny (do przełącznika motywu) mieszkają wyłącznie w
	// metadata.json — nie mają własnego pliku cache i próbujemy je pobrać
	// ZAWSZE, niezależnie od tego, czy spis nazw wyżej przyszedł z dysku czy
	// z sieci (najczęstsza gałąź — TTL spisu to tydzień — inaczej warianty
	// nigdy by się nie ustawiały po zwykłym restarcie procesu z ciepłym
	// cache). raw.githubusercontent.com nie ma limitu jak api.github.com
	// (git-tree), więc to tani, dodatkowy request przy każdym starcie.
	if _, err := k.zMetadanych(ctx); err != nil {
		slog.Debug("warianty jasny/ciemny niedostępne", "err", err)
	}
}

func (k *Katalog) odświeżSpisNazw(ctx context.Context) {
	plik := filepath.Join(k.cacheDir, "spis.json")
	if fi, err := os.Stat(plik); err == nil && time.Since(fi.ModTime()) < ważnośćIndeksu {
		if dane, err := os.ReadFile(plik); err == nil && k.ustawZeSpisu(dane) == nil {
			slog.Info("katalog ikon z cache", "ikon", k.Liczba())
			return
		}
	}

	if nazwy, źródło, err := k.pobierzSpis(ctx); err == nil && len(nazwy) > 0 {
		k.ustawNazwy(nazwy)
		if b, err := json.Marshal(nazwy); err == nil {
			_ = os.WriteFile(plik, b, 0o644)
		}
		slog.Info("katalog ikon pobrany", "ikon", k.Liczba(), "źródło", źródło)
		return
	}

	slog.Warn("spis ikon niedostępny, zostaję przy tym, co na dysku")
	k.awaryjnieZDysku(plik)
}

// pobierzSpis idzie od najdokładniejszego źródła do najsłabszego.
// Drzewo gita zna prawdę o katalogu, ale ma limit 60 zapytań na godzinę bez
// tokenu. metadata.json jest świeży i niesie aliasy. tree.json bywa nieaktualny,
// ale zawsze odpowiada.
func (k *Katalog) pobierzSpis(ctx context.Context) (map[string]string, string, error) {
	if nazwy, err := k.zDrzewaGita(ctx); err == nil && len(nazwy) > 0 {
		return nazwy, "git-tree", nil
	}
	if nazwy, err := k.zMetadanych(ctx); err == nil && len(nazwy) > 0 {
		return nazwy, "metadata.json", nil
	}
	nazwy, err := k.zTreeJSON(ctx)
	return nazwy, "tree.json", err
}

// zMetadanych czyta metadata.json — daje też aliasy, po których szuka wybierak.
func (k *Katalog) zMetadanych(ctx context.Context) (map[string]string, error) {
	dane, err := k.pobierzJSON(ctx, metadaneURL, 16<<20)
	if err != nil {
		return nil, err
	}
	var m map[string]struct {
		Base    string            `json:"base"`
		Aliases []string          `json:"aliases"`
		Colors  map[string]string `json:"colors"` // "dark"/"light" → nazwa pliku z tym tuszem
	}
	if err := json.Unmarshal(dane, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("puste metadane")
	}
	out := make(map[string]string, len(m))
	al := make(map[string][]string, len(m)/4)
	wa := make(map[string]wariantTonu, len(m)/6)
	for nazwa, w := range m {
		ext := w.Base
		if ext != "png" {
			ext = "svg"
		}
		out[nazwa] = ext
		if len(w.Aliases) > 0 {
			al[nazwa] = w.Aliases
		}
		if ciemny, jasny := w.Colors["dark"], w.Colors["light"]; ciemny != "" && jasny != "" {
			wa[nazwa] = wariantTonu{Ciemny: ciemny, Jasny: jasny}
		}
	}
	k.mu.Lock()
	k.aliasy = al
	k.warianty = wa
	k.mu.Unlock()
	return out, nil
}

func (k *Katalog) pobierzJSON(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (k *Katalog) zDrzewaGita(ctx context.Context) (map[string]string, error) {
	dane, err := k.pobierzJSON(ctx, drzewoGitURL, 16<<20)
	if err != nil {
		return nil, err
	}
	var d struct {
		Tree []struct {
			Path string `json:"path"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(dane, &d); err != nil {
		return nil, err
	}
	if d.Truncated || len(d.Tree) == 0 {
		return nil, fmt.Errorf("drzewo gita niekompletne")
	}
	out := map[string]string{}
	for _, t := range d.Tree {
		switch {
		case strings.HasPrefix(t.Path, "svg/") && strings.HasSuffix(t.Path, ".svg"):
			out[strings.TrimSuffix(strings.TrimPrefix(t.Path, "svg/"), ".svg")] = "svg"
		case strings.HasPrefix(t.Path, "png/") && strings.HasSuffix(t.Path, ".png"):
			n := strings.TrimSuffix(strings.TrimPrefix(t.Path, "png/"), ".png")
			if _, jest := out[n]; !jest {
				out[n] = "png"
			}
		}
	}
	return out, nil
}

func (k *Katalog) zTreeJSON(ctx context.Context) (map[string]string, error) {
	dane, err := k.pobierzJSON(ctx, drzewoURL, 8<<20)
	if err != nil {
		return nil, err
	}
	var d drzewo
	if err := json.Unmarshal(dane, &d); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range d.SVG {
		out[strings.TrimSuffix(f, ".svg")] = "svg"
	}
	for _, f := range d.PNG {
		n := strings.TrimSuffix(f, ".png")
		if _, jest := out[n]; !jest {
			out[n] = "png"
		}
	}
	return out, nil
}

func (k *Katalog) ustawNazwy(rozsz map[string]string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rozsz = rozsz
	k.nazwy = make([]string, 0, len(rozsz))
	for n := range rozsz {
		k.nazwy = append(k.nazwy, n)
	}
	sort.Strings(k.nazwy)
}

func (k *Katalog) ustawZeSpisu(dane []byte) error {
	var m map[string]string
	if err := json.Unmarshal(dane, &m); err != nil || len(m) == 0 {
		return fmt.Errorf("zły spis ikon")
	}
	k.ustawNazwy(m)
	return nil
}

func (k *Katalog) awaryjnieZDysku(plik string) {
	if dane, err := os.ReadFile(plik); err == nil && k.ustawZeSpisu(dane) == nil {
		return
	}
	k.zIndeksujCache()
}

func (k *Katalog) ustawZDrzewa(dane []byte) error {
	var d drzewo
	if err := json.Unmarshal(dane, &d); err != nil {
		return err
	}
	if len(d.SVG) == 0 {
		return fmt.Errorf("pusty spis ikon")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.nazwy = make([]string, 0, len(d.SVG))
	k.rozsz = make(map[string]string, len(d.SVG))
	for _, f := range d.SVG {
		n := strings.TrimSuffix(f, ".svg")
		k.rozsz[n] = "svg"
		k.nazwy = append(k.nazwy, n)
	}
	// część ikon istnieje wyłącznie jako PNG — niech też będą do wyboru
	for _, f := range d.PNG {
		n := strings.TrimSuffix(f, ".png")
		if _, jest := k.rozsz[n]; jest {
			continue
		}
		k.rozsz[n] = "png"
		k.nazwy = append(k.nazwy, n)
	}
	sort.Strings(k.nazwy)
	return nil
}

// zIndeksujCache buduje spis z plików, które już mamy — panel odcięty od sieci
// nadal pokazuje to, co kiedyś pobrał.
func (k *Katalog) zIndeksujCache() {
	wpisy, err := os.ReadDir(k.cacheDir)
	if err != nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.nazwy = nil
	for _, e := range wpisy {
		n := e.Name()
		switch {
		case strings.HasSuffix(n, ".svg"):
			b := strings.TrimSuffix(n, ".svg")
			k.rozsz[b] = "svg"
			k.nazwy = append(k.nazwy, b)
		case strings.HasSuffix(n, ".png"):
			b := strings.TrimSuffix(n, ".png")
			if _, jest := k.rozsz[b]; !jest {
				k.rozsz[b] = "png"
				k.nazwy = append(k.nazwy, b)
			}
		}
	}
	sort.Strings(k.nazwy)
}

// ── odczyt pojedynczej ikony ─────────────────────────────────────────────────

func (k *Katalog) Ma(nazwa string) bool {
	k.mu.RLock()
	_, ok := k.rozsz[nazwa]
	k.mu.RUnlock()
	if ok {
		return true
	}
	if k.cacheDir == "" {
		return false
	}
	for _, ext := range []string{"svg", "png"} {
		if _, err := os.Stat(filepath.Join(k.cacheDir, nazwa+"."+ext)); err == nil {
			return true
		}
	}
	return false
}

// Czytaj zwraca zawartość ikony i jej typ MIME.
// Kolejność: zip → cache na dysku → CDN (i zapis do cache).
func (k *Katalog) Czytaj(ctx context.Context, nazwa string) ([]byte, string, error) {
	if strings.ContainsAny(nazwa, `/\`) || strings.Contains(nazwa, "..") {
		return nil, "", fmt.Errorf("zła nazwa ikony")
	}

	k.mu.RLock()
	ext, znany := k.rozsz[nazwa]
	zipIdx := k.zipIdx
	k.mu.RUnlock()
	if !znany {
		ext = "svg"
	}

	if zipIdx != nil {
		if f, ok := zipIdx[nazwa+".svg"]; ok {
			rc, err := f.Open()
			if err == nil {
				defer rc.Close()
				dane, err := io.ReadAll(io.LimitReader(rc, 1<<20))
				return dane, "image/svg+xml", err
			}
		}
	}

	if k.cacheDir != "" {
		for _, e := range []string{ext, "svg", "png"} {
			// ! plik istnieje, ale ma 0 bajtów = poprzednie pobranie zostawiło
			// zatruty cache (patrz komentarz w pobierz). Taki plik pomijamy i
			// próbujemy pobrać na nowo, zamiast serwować pustkę w kółko.
			if dane, err := os.ReadFile(filepath.Join(k.cacheDir, nazwa+"."+e)); err == nil && len(dane) > 0 {
				return dane, typIkony(e), nil
			}
		}
	}

	return k.pobierz(ctx, nazwa, ext)
}

// typIkony mapuje rozszerzenie pliku ikony na MIME.
func typIkony(ext string) string {
	if ext == "png" {
		return "image/png"
	}
	return "image/svg+xml"
}

func (k *Katalog) pobierz(ctx context.Context, nazwa, ext string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/%s/%s.%s", cdnIkony, ext, nazwa, ext)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("CDN: %s", resp.Status)
	}
	dane, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, "", err
	}
	// ! jsDelivr bywa "ciepłe" — status 200, ale zero bajtów ciała, póki sam
	// jeszcze nie ściągnął pliku z GitHuba. Bez tej straży taki pusty wsad
	// lądował w cache na dysku NA STAŁE (pliki ikon nie mają TTL jak
	// spis.json), więc jedna nieudana chwila CDN-a zatruwała ikonę na zawsze —
	// to był realny powód "zepsutych" kafli w wyszukiwarce ikon.
	if len(dane) == 0 {
		return nil, "", fmt.Errorf("CDN: puste ciało odpowiedzi dla %s.%s", nazwa, ext)
	}
	if k.cacheDir != "" {
		tmp := filepath.Join(k.cacheDir, "."+nazwa+"."+ext+".tmp")
		if os.WriteFile(tmp, dane, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(k.cacheDir, nazwa+"."+ext))
		}
	}
	return dane, typIkony(ext), nil
}

// Szukaj zwraca nazwy pasujące do zapytania: najpierw te zaczynające się od
// frazy, potem zawierające ją w środku.
func (k *Katalog) Szukaj(q string, limit int) []string {
	k.mu.RLock()
	defer k.mu.RUnlock()

	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		if len(k.nazwy) < limit {
			limit = len(k.nazwy)
		}
		return append([]string(nil), k.nazwy[:limit]...)
	}
	var prefiks, środek []string
	for _, n := range k.nazwy {
		l := strings.ToLower(n)
		switch {
		case strings.HasPrefix(l, q):
			prefiks = append(prefiks, n)
		case strings.Contains(l, q):
			środek = append(środek, n)
		default:
			// alias: „8311 community" ma znaleźć ikonę „8311"
			for _, a := range k.aliasy[n] {
				if strings.Contains(strings.ToLower(a), q) {
					środek = append(środek, n)
					break
				}
			}
		}
		if len(prefiks) >= limit {
			break
		}
	}
	out := append(prefiks, środek...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ── warstwa HTTP ─────────────────────────────────────────────────────────────

// iconRef mówi szablonowi, jak wyrenderować ikonę usługi.
//
// Gdy ikona ma osobne warianty na ciemny/jasny motyw, Src zostaje pusty, a
// szablon renderuje OBA obrazy naraz (WariantCiemny/WariantJasny) — który się
// pokaże, decyduje czysty CSS na atrybucie data-ton na <html>. Motyw jest
// wyborem PRZEGLĄDARKI (bywa przestawiany bez przeładowania strony, patrz
// appearance.tone w konfiguracji), więc serwer przy renderze strony nie wie,
// który pokazać — musi dać oba i oddać decyzję CSS-owi, zero dodatkowego JS-a
// czy zapytań przy zmianie motywu.
type iconRef struct {
	Src           string // ścieżka na naszym serwerze, pusta gdy brak albo gdy są warianty
	Litera        string // fallback: kafelek z literą
	WariantCiemny string // ścieżka do wariantu na CIEMNY motyw (jasny tusz), puste = brak
	WariantJasny  string // ścieżka do wariantu na JASNY motyw (ciemny tusz), puste = brak
}

func (s *Server) rozwiążIkonę(icon, nazwa string) iconRef {
	litera := pierwszaLitera(nazwa)
	switch {
	case icon == "":
		return iconRef{Litera: litera}
	case strings.HasPrefix(icon, "http://"), strings.HasPrefix(icon, "https://"):
		return iconRef{Src: "/icon/proxy?u=" + urlEscape(icon), Litera: litera}
	default:
		n := strings.TrimSuffix(strings.TrimSuffix(icon, ".svg"), ".png")
		if ciemny, jasny := s.ikony.Warianty(n); ciemny != "" && jasny != "" {
			return iconRef{WariantCiemny: "/icon/" + ciemny, WariantJasny: "/icon/" + jasny, Litera: litera}
		}
		// nawet gdy spis się nie wczytał, spróbuj — najgorzej wyjdzie 404
		// i karta pokaże literę
		return iconRef{Src: "/icon/" + n, Litera: litera}
	}
}

func urlEscape(s string) string {
	r := strings.NewReplacer("&", "%26", "?", "%3F", " ", "%20", "#", "%23")
	return r.Replace(s)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	nazwa := r.PathValue("name")
	if nazwa == "proxy" {
		s.handleIconProxy(w, r)
		return
	}
	nazwa = strings.TrimSuffix(strings.TrimSuffix(nazwa, ".svg"), ".png")

	dane, typ, err := s.ikony.Czytaj(r.Context(), nazwa)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", typ)
	// ikona bywa SVG, czyli dokumentem z naszego originu — a script-src ma
	// 'unsafe-inline'. Piaskownica zamyka to bez odbierania ikonom działania.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// ikona pod daną nazwą jest niezmienna, więc tydzień w cache jest w
	// porządku — w przeciwieństwie do /bg/, gdzie plik bywa podmieniany
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(dane)
}

// typyIkon — jedyne, co wolno przepisać z obcego serwera.
var typyIkon = map[string]bool{
	"image/svg+xml": true, "image/png": true, "image/jpeg": true,
	"image/webp": true, "image/gif": true, "image/x-icon": true,
	"image/vnd.microsoft.icon": true,
}

// klientIkonZewnętrznych pobiera ikony spoza panelu.
//
// ! Blokada adresów prywatnych siedzi w Dialer.Control, czyli DZIAŁA NA
// ADRESIE, KTÓRY NAPRAWDĘ ZOSTAŁ WYBRANY — sprawdzanie hosta przed żądaniem
// omija się DNS-em zwracającym raz publiczny, raz lokalny adres. Bez tego
// `icon: http://192.168.0.1/…` w YAML-u robi z panelu skaner sieci domowej.
func klientIkonZewnętrznych() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: blokujAdresyLokalne}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("za dużo przekierowań")
			}
			return nil
		},
	}
}

func blokujAdresyLokalne(_, adres string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(adres)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("nierozpoznany adres %q", host)
	}
	if !adresPubliczny(ip) {
		return fmt.Errorf("adres lokalny %s jest zablokowany", ip)
	}
	return nil
}

func adresPubliczny(ip netip.Addr) bool {
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return false
	}
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 { // CGNAT 100.64/10
			return false
		}
	}
	return true
}

// handleIconProxy obsługuje ikony podane jako pełny adres — pobiera je serwer,
// nie przeglądarka.
func (s *Server) handleIconProxy(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("u")
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src, nil)
	if err != nil {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	klient := s.klientProxy
	if klient == nil {
		klient = klientIkonZewnętrznych()
	}
	resp, err := klient.Do(req)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream "+resp.Status, http.StatusBadGateway)
		return
	}
	// ! Content-Type od upstreamu był tu dotąd przepisywany DOSŁOWNIE, więc
	// `icon: https://zły/x` oddawał text/html z NASZEGO originu — czyli XSS
	// przy script-src 'unsafe-inline'. Teraz przechodzi tylko allowlista.
	typ, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !typyIkon[typ] {
		http.Error(w, "upstream nie oddał obrazu", http.StatusBadGateway)
		return
	}
	dane, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(dane)
}
