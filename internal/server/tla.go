package server

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
)

// Tapety użytkownika leżą na serwerze, nie w przeglądarce.
//
// Prototyp trzymał obraz w localStorage i sam przyznawał się do porażki
// komunikatem „Image too large to remember". Plik na dysku znosi ten limit, ale
// w zamian otwiera jedyne w tym panelu miejsce, gdzie użytkownik wgrywa bajty.
// Dlatego cała obsługa stoi na trzech zasadach:
//
//  1. nazwa od klienta NIGDY nie tworzy ścieżki — trzon jest przepisywany na
//     nasz alfabet, a rozszerzenie bierze się wyłącznie ze sniffa zawartości;
//  2. każda operacja plikowa idzie przez uchwyt os.Root, więc traversal i
//     podłożony symlink są odcięte na poziomie syscalli, a nie porównywania
//     stringów;
//  3. odpowiedź z /bg/ jest zapiaskowana własnym CSP — nawet gdyby polyglot
//     przeszedł walidację, nie ma prawa nic wykonać.

const (
	limitPlikuTła  = 8 << 20 // 8 MiB na jeden plik
	LimitTłaMB     = 8       // to samo w megabajtach — dla UI i komunikatów
	limitPlikówTła = 20
	limitBajtówTła = 100 << 20
	maxTrzonuTła   = 48
	czasUploadu    = 30 * time.Second
	wiekŚmieciaTła = 30 * 24 * time.Hour
)

// mimeTła — stała mapa format→MIME. Do odpowiedzi idzie WYŁĄCZNIE stąd, nigdy
// nagłówek od klienta ani zgadywanie z nazwy.
var mimeTła = map[string]string{
	"jpg":  "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
	"gif":  "image/gif",
	"avif": "image/avif",
}

var (
	errTłaWyłączone = errors.New("katalog teł niedostępny")
	errBrakTła      = errors.New("nie ma takiego tła")
	errLimitTła     = errors.New("limit teł osiągnięty")
)

// WpisTła — jedna wgrana tapeta w odpowiedzi JSON.
type WpisTła struct {
	Nazwa  string `json:"name"`
	Typ    string `json:"type"`
	Bajtów int64  `json:"bytes"`
	Kiedy  string `json:"at"`
}

type KatalogTeł struct {
	dir      string
	root     *os.Root
	dostępny bool
	// semafor przycina równoległe uploady — dwa naraz wystarczą, a bez tego
	// kilkanaście żądań po 8 MB potrafi zapchać warstwę zapisywalną
	semafor chan struct{}
}

// NowyKatalogTeł przygotowuje katalog i od razu sprawdza, czy da się w nim
// pisać. Porażka nie wywala panelu — wyłącza funkcję i mówi, co naprawić.
func NowyKatalogTeł(dir string) *KatalogTeł {
	k := &KatalogTeł{dir: dir, semafor: make(chan struct{}, 2)}
	if dir == "" {
		return k
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Error("nie mogę utworzyć katalogu teł", "dir", dir, "err", err)
		return k
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		slog.Error("nie mogę otworzyć katalogu teł", "dir", dir, "err", err)
		return k
	}
	// próbny zapis: lepiej wiedzieć teraz niż po przyjęciu i zdekodowaniu 8 MB
	próbny := ".proba-zapisu.tmp"
	f, err := root.OpenFile(próbny, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Close()
		slog.Error("katalog teł niezapisywalny, wgrywanie tapet wyłączone",
			"dir", dir, "err", err, "napraw", "chown -R 65532:65532 "+dir)
		return k
	}
	_ = f.Close()
	_ = root.Remove(próbny)

	k.root, k.dostępny = root, true
	slog.Info("katalog teł gotowy", "dir", dir)
	return k
}

func (k *KatalogTeł) Dostępny() bool { return k != nil && k.dostępny }

func (k *KatalogTeł) Zamknij() {
	if k != nil && k.root != nil {
		_ = k.root.Close()
	}
}

// ── nazwy ────────────────────────────────────────────────────────────────────

// sanityzujNazwę robi z nazwy podanej przez klienta trzon, który na pewno nie
// utworzy ścieżki.
//
// ! filepath.Base NIE tnie `..\x` na Linuksie, a r.PathValue oddaje segment już
// od-eskejpowany, więc `%2e%2e%2f` dociera tu jako `../`. Dlatego nie filtrujemy
// znanych ładunków, tylko przepisujemy nazwę na alfabet, w którym żaden z nich
// nie da się wyrazić. Rozszerzenie dokłada osobno sniff zawartości.
func sanityzujNazwę(surowa, format string) (string, bool) {
	trzon := strings.ToLower(surowa)
	if i := strings.LastIndexAny(trzon, `/\`); i >= 0 {
		trzon = trzon[i+1:]
	}
	if i := strings.LastIndexByte(trzon, '.'); i > 0 {
		trzon = trzon[:i] // rozszerzenie od klienta jest bez znaczenia
	}

	var b strings.Builder
	for i := 0; i < len(trzon); i++ {
		c := trzon[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-':
			// zwiń powtórzone separatory
			if (c == '.' || c == '-') && b.Len() > 0 {
				if p := b.String()[b.Len()-1]; p == '.' || p == '-' {
					continue
				}
			}
			b.WriteByte(c)
		}
	}
	trzon = strings.Trim(b.String(), ".-")
	if len(trzon) > maxTrzonuTła {
		trzon = strings.Trim(trzon[:maxTrzonuTła], ".-")
	}
	if trzon == "" {
		trzon = "tlo"
	}

	nazwa := trzon + "." + format
	if _, znany := mimeTła[format]; !znany || !config.BackgroundName.MatchString(nazwa) {
		return "", false
	}
	return nazwa, true
}

// formatZNazwy czyta rozszerzenie z NASZEJ nazwy — tej, którą sami zapisaliśmy
// po sniffie. Nazwa klienta tędy nie przechodzi.
func formatZNazwy(nazwa string) (string, bool) {
	i := strings.LastIndexByte(nazwa, '.')
	if i < 0 {
		return "", false
	}
	format := nazwa[i+1:]
	_, ok := mimeTła[format]
	return format, ok
}

// ── rozpoznawanie formatu ────────────────────────────────────────────────────

// wykryjTypObrazu ustala format WYŁĄCZNIE z zawartości pliku.
//
// ! SVG nie ma tu wpisu i mieć nie będzie: otwarty jako dokument wykonuje
// <script>, a nasz CSP musi trzymać 'unsafe-inline' dla skryptów, bo index.html
// wstrzykuje stan usług.
func wykryjTypObrazu(nagłówek []byte) (string, bool) {
	if czyAVIF(nagłówek) {
		return "avif", true
	}
	switch http.DetectContentType(nagłówek) {
	case "image/jpeg":
		return "jpg", true
	case "image/png":
		return "png", true
	case "image/webp":
		return "webp", true
	case "image/gif":
		return "gif", true
	}
	return "", false
}

// czyAVIF rozpoznaje pudełko ISO-BMFF `ftyp` z marką avif/avis.
// http.DetectContentType AVIF-a nie zna i oddałby application/octet-stream.
func czyAVIF(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	if marka := string(b[8:12]); marka == "avif" || marka == "avis" {
		return true
	}
	rozmiar := int(binary.BigEndian.Uint32(b[:4]))
	if rozmiar > len(b) || rozmiar < 16 {
		rozmiar = len(b)
	}
	for i := 16; i+4 <= rozmiar; i += 4 { // marki zgodne stoją za major_brand i wersją
		if m := string(b[i : i+4]); m == "avif" || m == "avis" {
			return true
		}
	}
	return false
}

// ── operacje na plikach ──────────────────────────────────────────────────────

// Lista zwraca wgrane tapety, od najnowszej.
func (k *KatalogTeł) Lista() ([]WpisTła, int64, error) {
	if !k.Dostępny() {
		return nil, 0, errTłaWyłączone
	}
	wpisy, err := os.ReadDir(k.dir)
	if err != nil {
		return nil, 0, err
	}
	out := make([]WpisTła, 0, len(wpisy))
	var razem int64
	for _, e := range wpisy {
		if e.IsDir() || !config.BackgroundName.MatchString(e.Name()) {
			continue // tymczasówki zaczynają się od kropki i wypadają tutaj
		}
		format, ok := formatZNazwy(e.Name())
		if !ok {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		razem += fi.Size()
		out = append(out, WpisTła{
			Nazwa: e.Name(), Typ: mimeTła[format],
			Bajtów: fi.Size(), Kiedy: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kiedy > out[j].Kiedy })
	return out, razem, nil
}

// wolnaNazwa dokłada sufiks, dopóki nazwa jest zajęta. Nigdy nie nadpisuje
// cudzego pliku — dwie osoby wgrywające „tlo.png" dostają dwa pliki.
func (k *KatalogTeł) wolnaNazwa(nazwa string) (string, error) {
	if _, err := k.root.Stat(nazwa); errors.Is(err, os.ErrNotExist) {
		return nazwa, nil
	}
	kropka := strings.LastIndexByte(nazwa, '.')
	if kropka <= 0 {
		return "", errBrakTła
	}
	trzon, format := nazwa[:kropka], nazwa[kropka+1:]
	for i := 2; i < 100; i++ {
		kandydat := fmt.Sprintf("%s-%d.%s", trzon, i, format)
		if !config.BackgroundName.MatchString(kandydat) {
			return "", errLimitTła
		}
		if _, err := k.root.Stat(kandydat); errors.Is(err, os.ErrNotExist) {
			return kandydat, nil
		}
	}
	return "", errLimitTła
}

// Zapisz kładzie strumień na dysk atomowo: najpierw plik tymczasowy, dopiero po
// pełnym zapisie i fsyncu rename pod właściwą nazwę.
//
// ! Wzorzec z config/save.go, nie z icons.go — tam brakuje Sync przed Rename,
// co dla ikony z CDN jest nieszkodliwe (można ją pobrać znowu), a dla
// nieodtwarzalnego pliku użytkownika już nie.
func (k *KatalogTeł) Zapisz(nazwaKlienta string, nagłówek []byte, reszta io.Reader) (WpisTła, error) {
	if !k.Dostępny() {
		return WpisTła{}, errTłaWyłączone
	}
	format, ok := wykryjTypObrazu(nagłówek)
	if !ok {
		return WpisTła{}, errors.New("nieobsługiwany format obrazu")
	}
	nazwa, ok := sanityzujNazwę(nazwaKlienta, format)
	if !ok {
		return WpisTła{}, errors.New("nazwa nie do uratowania")
	}

	lista, razem, err := k.Lista()
	if err != nil {
		return WpisTła{}, err
	}
	if len(lista) >= limitPlikówTła || razem >= limitBajtówTła {
		return WpisTła{}, errLimitTła
	}

	los := make([]byte, 8)
	if _, err := rand.Read(los); err != nil {
		return WpisTła{}, err
	}
	tmp := ".wgrywanie-" + hex.EncodeToString(los) + ".tmp"
	f, err := k.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return WpisTła{}, err
	}
	sprzątaj := func() {
		_ = f.Close()
		_ = k.root.Remove(tmp)
	}

	n, err := f.Write(nagłówek)
	if err != nil {
		sprzątaj()
		return WpisTła{}, err
	}
	skopiowane, err := io.Copy(f, reszta)
	if err != nil {
		sprzątaj()
		return WpisTła{}, err
	}
	rozmiar := int64(n) + skopiowane
	if razem+rozmiar > limitBajtówTła {
		sprzątaj()
		return WpisTła{}, errLimitTła
	}
	if err := f.Sync(); err != nil {
		sprzątaj()
		return WpisTła{}, err
	}
	// umask w distroless obciąłby bity z OpenFile, stąd jawny Chmod
	if err := f.Chmod(0o600); err != nil {
		sprzątaj()
		return WpisTła{}, err
	}
	if err := f.Close(); err != nil {
		_ = k.root.Remove(tmp)
		return WpisTła{}, err
	}

	docelowa, err := k.wolnaNazwa(nazwa)
	if err != nil {
		_ = k.root.Remove(tmp)
		return WpisTła{}, err
	}
	if err := k.root.Rename(tmp, docelowa); err != nil {
		_ = k.root.Remove(tmp)
		return WpisTła{}, err
	}
	return WpisTła{
		Nazwa: docelowa, Typ: mimeTła[format], Bajtów: rozmiar,
		Kiedy: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// Usuń kasuje tapetę po nazwie z NASZEGO alfabetu.
//
// ! Kolejność jest istotna: najpierw regex, potem Stat (brak = 404 i koniec,
// bez dotykania czegokolwiek), a dopiero na końcu Remove. handleServiceDelete
// bierze dziś ?name= bez żadnej kontroli — to jest dokładnie ten wzorzec,
// którego nowy kod ma nie powtórzyć.
func (k *KatalogTeł) Usuń(nazwa string) error {
	if !k.Dostępny() {
		return errTłaWyłączone
	}
	if !config.BackgroundName.MatchString(nazwa) {
		return errBrakTła
	}
	if _, err := k.root.Stat(nazwa); err != nil {
		return errBrakTła
	}
	return k.root.Remove(nazwa)
}

// Istnieje mówi, czy plik o takiej nazwie leży w katalogu teł. Używane przy
// zapisie wyglądu: config nie ma prawa wskazać tapety, której nie ma.
func (k *KatalogTeł) Istnieje(nazwa string) bool {
	if !k.Dostępny() || !config.BackgroundName.MatchString(nazwa) {
		return false
	}
	fi, err := k.root.Stat(nazwa)
	return err == nil && !fi.IsDir()
}

// Otwórz podaje plik do wysłania. Uchwyt zamyka wołający.
func (k *KatalogTeł) Otwórz(nazwa string) (*os.File, os.FileInfo, string, error) {
	if !k.Dostępny() || !config.BackgroundName.MatchString(nazwa) {
		return nil, nil, "", errBrakTła
	}
	format, ok := formatZNazwy(nazwa)
	if !ok {
		return nil, nil, "", errBrakTła
	}
	f, err := k.root.Open(nazwa)
	if err != nil {
		return nil, nil, "", errBrakTła
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		_ = f.Close()
		return nil, nil, "", errBrakTła
	}
	return f, fi, mimeTła[format], nil
}

// SprzątnijStare kasuje NIEUŻYWANE tapety starsze niż 30 dni, i to wyłącznie
// wtedy, gdy katalog przekroczył limit. Panel nie ma prawa z własnej inicjatywy
// wyrzucać plików, których ktoś świadomie nie ruszał.
func (k *KatalogTeł) SprzątnijStare(używana string) int {
	lista, razem, err := k.Lista()
	if err != nil {
		return 0
	}
	if len(lista) <= limitPlikówTła && razem <= limitBajtówTła {
		return 0
	}
	próg := time.Now().Add(-wiekŚmieciaTła).UTC().Format(time.RFC3339)
	usunięte := 0
	for i := len(lista) - 1; i >= 0; i-- { // od najstarszej
		w := lista[i]
		if w.Nazwa == używana || w.Kiedy >= próg {
			continue
		}
		if err := k.Usuń(w.Nazwa); err != nil {
			continue
		}
		usunięte++
		razem -= w.Bajtów
		if len(lista)-usunięte <= limitPlikówTła && razem <= limitBajtówTła {
			break
		}
	}
	if usunięte > 0 {
		slog.Info("stare tapety usunięte", "ile", usunięte)
	}
	return usunięte
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// handleTłoUpload przyjmuje jeden plik obrazu.
//
// Wgranie pliku samo w sobie NIC nie zmienia w configu — dopiero wskazanie go
// jako tło instancji idzie przez PUT /api/appearance, czyli przez diff, wersję
// i commit. Tu lądują wyłącznie bajty.
func (s *Server) handleTłoUpload(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	if !s.tła.Dostępny() {
		jsonBłąd(w, http.StatusServiceUnavailable, i18n.T(lang, "tapeta_wylaczona"))
		return
	}
	// ! PIERWSZA linia handlera, przed jakimkolwiek parsowaniem
	r.Body = http.MaxBytesReader(w, r.Body, limitPlikuTła)

	select {
	case s.tła.semafor <- struct{}{}:
		defer func() { <-s.tła.semafor }()
	default:
		jsonBłąd(w, http.StatusConflict, i18n.T(lang, "tapeta_limit"))
		return
	}
	// serwer nie ma ReadTimeout (celowo, przez SSE), więc jedno nieskończenie
	// wolne ciało trzymałoby goroutine w nieskończoność
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(czasUploadu))

	// r.MultipartReader, NIGDY ParseMultipartForm: ten drugi przelewa nadmiar
	// do os.TempDir() i zapycha warstwę zapisywalną kontenera
	mr, err := r.MultipartReader()
	if err != nil {
		s.odpowiedzBłędemTła(w, lang, err)
		return
	}
	część, err := mr.NextPart()
	if err != nil {
		s.odpowiedzBłędemTła(w, lang, err)
		return
	}
	defer część.Close()
	if część.FormName() != "file" {
		jsonBłąd(w, http.StatusBadRequest, "oczekuję jednej części o nazwie \"file\"")
		return
	}

	nagłówek := make([]byte, 512)
	n, err := io.ReadFull(część, nagłówek)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		s.odpowiedzBłędemTła(w, lang, err)
		return
	}
	// ! FileName() i Content-Type części są w 100% pod kontrolą wgrywającego —
	// pierwsza służy tylko do wyprowadzenia trzonu, druga do niczego
	wpis, err := s.tła.Zapisz(część.FileName(), nagłówek[:n], część)
	if err != nil {
		s.odpowiedzBłędemTła(w, lang, err)
		return
	}
	if _, err := mr.NextPart(); !errors.Is(err, io.EOF) {
		_ = s.tła.Usuń(wpis.Nazwa)
		jsonBłąd(w, http.StatusBadRequest, "oczekuję dokładnie jednej części")
		return
	}
	slog.Info("tapeta wgrana", "nazwa", wpis.Nazwa, "bajtów", wpis.Bajtów)
	jsonOdp(w, http.StatusCreated, wpis)
}

// odpowiedzBłędemTła tłumaczy błąd zapisu na kod HTTP i komunikat dla człowieka.
func (s *Server) odpowiedzBłędemTła(w http.ResponseWriter, lang i18n.Lang, err error) {
	var zaDuże *http.MaxBytesError
	switch {
	case errors.As(err, &zaDuże), strings.Contains(err.Error(), "request body too large"):
		jsonBłąd(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf(i18n.T(lang, "tapeta_za_duza"), LimitTłaMB))
	case errors.Is(err, errLimitTła):
		jsonBłąd(w, http.StatusConflict, i18n.T(lang, "tapeta_limit"))
	case errors.Is(err, errTłaWyłączone):
		jsonBłąd(w, http.StatusServiceUnavailable, i18n.T(lang, "tapeta_wylaczona"))
	case strings.Contains(err.Error(), "format"), strings.Contains(err.Error(), "nazwa"):
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "tapeta_zly_typ"))
	default:
		slog.Warn("wgrywanie tapety nieudane", "err", err)
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_zapisu_tla"))
	}
}

func (s *Server) handleTłaLista(w http.ResponseWriter, r *http.Request) {
	lista, _, err := s.tła.Lista()
	if err != nil {
		if errors.Is(err, errTłaWyłączone) {
			jsonOdp(w, http.StatusOK, map[string]any{"items": []WpisTła{}, "used": "",
				"count": 0, "limitFiles": limitPlikówTła, "limitBytes": limitBajtówTła})
			return
		}
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOdp(w, http.StatusOK, map[string]any{
		"items": lista, "used": s.wyglądInstancji().Obraz, "count": len(lista),
		"limitFiles": limitPlikówTła, "limitBytes": limitBajtówTła,
	})
}

func (s *Server) handleTłoUsuń(w http.ResponseWriter, r *http.Request) {
	lang := i18n.FromRequest(r)
	nazwa := r.PathValue("nazwa")
	if !config.BackgroundName.MatchString(nazwa) {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
		return
	}
	// tło w użyciu kasujemy dopiero po zmianie configu — inaczej panel zostaje
	// z url() w 404, a zmiana wyglądu ma iść przez diff jak każda inna
	if s.wyglądInstancji().Obraz == nazwa {
		jsonBłąd(w, http.StatusConflict, i18n.T(lang, "tapeta_uzywana_blad"))
		return
	}
	if err := s.tła.Usuń(nazwa); err != nil {
		if errors.Is(err, errTłaWyłączone) {
			jsonBłąd(w, http.StatusServiceUnavailable, i18n.T(lang, "tapeta_wylaczona"))
			return
		}
		jsonBłąd(w, http.StatusNotFound, i18n.T(lang, "blad_wygladu"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBg serwuje wgraną tapetę — wyłącznie zalogowanym.
func (s *Server) handleBg(w http.ResponseWriter, r *http.Request) {
	nazwa := r.PathValue("nazwa")
	f, fi, typ, err := s.tła.Otwórz(nazwa)
	if err != nil {
		// 404 także przy złej nazwie: nie zdradzamy, co istnieje, a co nie
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("X-Content-Type-Options", "nosniff")
	// najmocniejsza pojedyncza obrona w tym miejscu: dokument bez prawa do
	// czegokolwiek, nawet gdyby walidacja kiedyś przepuściła polyglota
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", `inline; filename="`+nazwa+`"`)
	// ! nie public/604800 jak ikony: tło bywa podmieniane pod tą samą nazwą,
	// a `public` na zasobie za logowaniem zapamiętałoby je we wspólnym proxy
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	http.ServeContent(w, r, nazwa, fi.ModTime(), f)
}

// DomyślnyKatalogTeł — obok bazy, ale w OSOBNYM podkatalogu.
//
// ! Nigdy w jednym katalogu z bazą: plik wgrany pod nazwą `shygarlic.db`
// nadpisałby ją, LiczbaUżytkowników() zwróciłoby 0, a wtedy /setup stoi otworem
// i zakłada konto administratora bez uwierzytelnienia. Upload obrazka byłby
// przejęciem panelu.
func DomyślnyKatalogTeł(dataPath string) string {
	return filepath.Join(filepath.Dir(dataPath), "bg")
}
