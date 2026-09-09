// Package server serwuje panel: HTML, status w JSON i strumień zmian przez SSE.
//
// Wszystko dzieje się po stronie serwera: sprawdzanie usług, pobieranie ikon,
// odczyt configu. Przeglądarka nie wykonuje ani jednego żądania na zewnątrz —
// dzięki temu panel działa bez internetu, a lista usług użytkownika nigdzie
// nie wycieka. Konkurencja hotlinkuje ikony do jsDelivr prosto z przeglądarki.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/dockerdisc"
	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/notify"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

//go:embed templates/*.html assets/*
var files embed.FS

type Server struct {
	path string
	mon  *health.Monitor
	tmpl *template.Template
	st     *store.Store
	lim    *limiter
	ikony  *Katalog
	tła    *KatalogTeł
	powiad *notify.Powiadamiacz
	docker *dockerdisc.Klient

	// webhookZeŚrodowiska i instancja są ustawiane raz, w New, i nigdy potem —
	// dlatego bez mutexu.
	webhookZeŚrodowiska bool
	// prógZFlagi — --fail-threshold podany jawnie. Wtedy plik go nie rusza,
	// tak samo jak przy webhooku: to, co przyszło z zewnątrz przy starcie,
	// jest własnością deployu.
	prógZFlagi bool
	instancja  []OpisOpcji
	// klientProxy pobiera ikony podane pełnym adresem; osobne pole, bo test
	// musi umieć podstawić klienta bez blokady adresów prywatnych
	klientProxy *http.Client
	ctx         context.Context

	// edycja serializuje zapisy do pliku — dwa równoległe kliknięcia w UI
	// nie mogą nadpisać się nawzajem
	edycja sync.Mutex

	mu    sync.RWMutex
	cfg   *config.Config
	stamp string // rozmiar+mtime pliku, do wykrycia edycji z zewnątrz

	// drugi nadajnik SSE: zmiany wyglądu. Osobny od health.Monitor, bo tamten
	// strumień jest nietypowany (data: bez event:) i rysuj() by się na tym
	// przewrócił.
	muWygląd  sync.Mutex
	subWygląd map[chan struct{}]struct{}

	// tanio liczony licznik kandydatów z Dockera — /api/info woła się przy
	// każdym otwarciu panelu, a gniazdo nie musi tego oglądać za każdym razem
	muDocker    sync.Mutex
	dockerIle   int
	dockerKiedy time.Time
}

// Opcje zbiera to, co nie jest ścieżką do configu.
type Opcje struct {
	IconsZip      string   // opcjonalny komplet ikon offline
	CacheDir      string   // cache ikon pobranych z CDN
	BackgroundDir string   // katalog wgranych tapet (pusty = funkcja wyłączona)
	Webhook       string   // adres webhooka powiadomień (pusty = wyłączone)
	PrógAwarii    int      // ile nieudanych sprawdzeń z rzędu zanim powiadomimy
	DockerSock    string   // gniazdo Dockera do podpowiadania usług (pusty = domyślne)
	TrustedProxy  []string // CIDR-y proxy, którym wolno mówić X-Forwarded-*

	// WebhookZeŚrodowiska — adres przyszedł z flagi albo ze zmiennej, więc
	// panel go nie nadpisze. Deploy ma wygrywać z klikaniem: inaczej restart
	// kontenera po cichu cofałby zmianę zrobioną w UI.
	WebhookZeŚrodowiska bool
	// PrógZFlagi — --fail-threshold podany jawnie w linii poleceń.
	PrógZFlagi bool
	// Instancja — opcje startu do pokazania w panelu, wyłącznie do odczytu.
	Instancja []OpisOpcji
}

// OpisOpcji — jedna opcja instancji na potrzeby zakładki „Instancja".
//
// ! Tu trafia wyłącznie to, czego ujawnienie jest bezpieczne. Webhook, choć
// też pochodzi z flagi, tu nie wchodzi — od niego jest /api/notify, które go
// maskuje.
type OpisOpcji struct {
	Klucz   string `json:"klucz"`
	Wartość string `json:"wartosc"`
	Źródło  string `json:"zrodlo"` // flaga | srodowisko | domyslna
}

func New(path string, st *store.Store, o Opcje) (*Server, error) {
	tmpl, err := template.ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	if err := UstawZaufaneProxy(o.TrustedProxy); err != nil {
		return nil, err
	}
	s := &Server{
		path: path, mon: health.New(), tmpl: tmpl, st: st,
		lim: nowyLimiter(), ikony: NowyKatalog(o.IconsZip, o.CacheDir),
		tła:    NowyKatalogTeł(o.BackgroundDir),
		powiad: notify.Nowy(o.Webhook, o.PrógAwarii),
		docker: dockerdisc.Nowy(o.DockerSock),

		webhookZeŚrodowiska: o.WebhookZeŚrodowiska,
		prógZFlagi:          o.PrógZFlagi,
		instancja:           o.Instancja,

		klientProxy: klientIkonZewnętrznych(),
		subWygląd:   map[chan struct{}]struct{}{},
	}

	// Adres z bazy podnosimy tylko wtedy, gdy środowisko nic nie narzuciło —
	// precedencja musi być ta sama tu i w handleNotifyPut, inaczej panel
	// pokazywałby jedno, a monitor używał drugiego.
	if !o.WebhookZeŚrodowiska {
		adres, err := st.Ustawienie(context.Background(), "notify.webhook")
		if err != nil {
			return nil, fmt.Errorf("czytam webhook z bazy: %w", err)
		}
		if adres != "" {
			s.powiad.UstawWebhook(adres)
		}
	}
	return s, nil
}

// ── rozgłaszanie zmiany wyglądu ──────────────────────────────────────────────

func (s *Server) subskrybujWygląd() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	s.muWygląd.Lock()
	s.subWygląd[c] = struct{}{}
	s.muWygląd.Unlock()
	return c, func() {
		s.muWygląd.Lock()
		delete(s.subWygląd, c)
		s.muWygląd.Unlock()
	}
}

// rozgłośWygląd budzi otwarte karty. Wołany z reload(), a NIE z zEdycją —
// dzięki temu docierają też zmiany pliku zrobione z zewnątrz (git pull,
// Ansible), które dziś nie budzą żadnego subskrybenta.
func (s *Server) rozgłośWygląd() {
	s.muWygląd.Lock()
	subs := make([]chan struct{}, 0, len(s.subWygląd))
	for c := range s.subWygląd {
		subs = append(subs, c)
	}
	s.muWygląd.Unlock()
	for _, c := range subs {
		select {
		case c <- struct{}{}:
		default: // odbiorca nie nadąża — i tak wyśle pełny stan
		}
	}
}

// configBytes czyta plik konfiguracyjny z dysku.
func configBytes(path string) ([]byte, error) { return os.ReadFile(path) }

// Start wczytuje config, uruchamia sprawdzanie i pilnuje pliku.
func (s *Server) Start(ctx context.Context) error {
	s.ctx = ctx
	if err := s.reload(ctx); err != nil {
		return err
	}
	go s.watch(ctx)
	go s.zapisujHistorię(ctx)
	go s.sprzątaj(ctx)
	go func() {
		s.ikony.Odśwież(ctx)
		slog.Info("ikony", "tryb", s.ikony.Tryb(), "dostępnych", s.ikony.Liczba())
	}()
	return nil
}

// zapisujHistorię przepisuje wyniki sprawdzeń do SQLite. Dzięki temu panel wie,
// co było godzinę temu — żaden lekki panel na rynku tego nie ma.
func (s *Server) zapisujHistorię(ctx context.Context) {
	zmiany, odepnij := s.mon.Subscribe()
	defer odepnij()
	ostatnie := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-zmiany:
			for klucz, r := range s.mon.Snapshot() {
				if r.At.IsZero() || !r.At.After(ostatnie[klucz]) {
					continue
				}
				ostatnie[klucz] = r.At
				err := s.st.ZapiszWynik(ctx, store.Wynik{
					Klucz: klucz, Stan: string(r.State), Kod: r.Code,
					MS: r.MS, Błąd: r.Err, Kiedy: r.At,
				})
				if err != nil {
					slog.Debug("zapis historii", "err", err)
				}
				s.powiadom(ctx, klucz, r)
			}
		}
	}
}

// powiadom przekazuje wynik do powiadamiacza razem z opisem usługi.
func (s *Server) powiadom(ctx context.Context, klucz string, r health.Result) {
	if !s.powiad.Włączony() {
		return
	}
	grupa, nazwa, _ := strings.Cut(klucz, "/")
	adres := ""
	s.mu.RLock()
	if s.cfg != nil {
		for _, g := range s.cfg.Groups {
			for _, svc := range g.Services {
				if key(g.Name, svc.Name) == klucz {
					adres = svc.URL
				}
			}
		}
	}
	s.mu.RUnlock()

	s.powiad.Obserwuj(ctx, notify.Zdarzenie{
		Klucz: klucz, Nazwa: nazwa, Grupa: grupa, URL: adres,
		Stan: string(r.State), Kod: r.Code, Błąd: r.Err, MS: r.MS, Kiedy: r.At,
	})
}

// sprzątaj usuwa wygasłe sesje i historię starszą niż 30 dni.
func (s *Server) sprzątaj(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		_ = s.st.SprzątnijSesje(ctx)
		if n, err := s.st.Przytnij(ctx, 30*24*time.Hour); err == nil && n > 0 {
			slog.Info("historia przycięta", "usunięto", n)
		}
		// jedyne miejsce, gdzie panel sam kasuje plik z dysku — i tylko taki,
		// którego nikt nie używa, jest starszy niż 30 dni i wypycha katalog
		// poza limit
		s.tła.SprzątnijStare(s.wyglądInstancji().Obraz)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) Close() {
	s.mon.Close()
	s.tła.Zamknij()
}

func (s *Server) reload(ctx context.Context) error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg, err := config.Load(raw)
	if err != nil {
		return err
	}

	// ! Próg musi wjechać tutaj, nie tylko przy zapisie z panelu. Bez tego
	// ustawienie notify.threshold w pliku i restart cicho przywracały wartość
	// domyślną — a w tym projekcie plik jest źródłem prawdy.
	if !s.prógZFlagi {
		s.powiad.UstawPróg(cfg.Notify.Threshold)
	}

	targets := make([]health.Target, 0, 16)
	for _, g := range cfg.Groups {
		for _, svc := range g.Services {
			if svc.URL == "" {
				continue
			}
			targets = append(targets, health.Target{
				Key:      key(g.Name, svc.Name),
				URL:      svc.URL,
				Interval: svc.Check.Interval.Duration(),
				Timeout:  svc.Check.Timeout.Duration(),
				Expect:   svc.Check.Expect,
				Insecure: svc.Check.Insecure,
			})
		}
	}

	s.mu.Lock()
	s.cfg = cfg
	s.stamp = s.stampOf()
	s.mu.Unlock()

	s.mon.SetTargets(ctx, targets)
	żywe := make(map[string]bool, len(targets))
	for _, t := range targets {
		żywe[t.Key] = true
	}
	s.powiad.Zapomnij(żywe)
	s.rozgłośWygląd()
	slog.Info("config wczytany", "usług", len(targets), "grup", len(cfg.Groups))
	return nil
}

// watch reaguje na ręczną edycję pliku — plik jest źródłem prawdy, więc panel
// ma się do niego dostosować, a nie odwrotnie.
func (s *Server) watch(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.RLock()
			stary := s.stamp
			s.mu.RUnlock()
			if nowy := s.stampOf(); nowy != "" && nowy != stary {
				slog.Info("config zmieniony na dysku, przeładowuję")
				if err := s.reload(ctx); err != nil {
					slog.Error("przeładowanie nieudane", "err", err)
				}
			}
		}
	}
}

func (s *Server) stampOf() string {
	fi, err := os.Stat(s.path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

func key(group, service string) string { return group + "/" + service }

func (s *Server) Handler() http.Handler {
	// trasy za logowaniem
	chronione := http.NewServeMux()
	chronione.HandleFunc("GET /{$}", s.handleIndex)
	chronione.HandleFunc("GET /api/status", s.handleStatus)
	chronione.HandleFunc("GET /api/history", s.handleHistory)
	chronione.HandleFunc("GET /api/service", s.handleService)
	chronione.HandleFunc("POST /api/check", s.handleCheckNow)
	chronione.HandleFunc("POST /api/notify/test", s.handleNotifyTest)
	chronione.HandleFunc("GET /api/notify", s.handleNotifyGet)
	chronione.HandleFunc("PUT /api/notify", s.handleNotifyPut)
	chronione.HandleFunc("GET /api/instance", s.handleInstance)
	chronione.HandleFunc("GET /api/info", s.handleInfo)
	chronione.HandleFunc("GET /api/discover", s.handleDiscover)
	chronione.HandleFunc("POST /api/services/bulk", s.handleServicesBulk)
	chronione.HandleFunc("GET /api/icons", s.handleIcons)
	chronione.HandleFunc("GET /api/config", s.handleConfigGet)
	chronione.HandleFunc("PUT /api/config", s.handleConfigPut)
	chronione.HandleFunc("POST /api/services", s.handleServiceCreate)
	chronione.HandleFunc("PATCH /api/services", s.handleServiceUpdate)
	chronione.HandleFunc("DELETE /api/services", s.handleServiceDelete)
	chronione.HandleFunc("POST /api/groups", s.handleGroupCreate)
	chronione.HandleFunc("PATCH /api/groups", s.handleGroupUpdate)
	chronione.HandleFunc("DELETE /api/groups", s.handleGroupDelete)
	chronione.HandleFunc("GET /api/versions", s.handleVersionsList)
	chronione.HandleFunc("GET /api/versions/{id}", s.handleVersionOne)
	chronione.HandleFunc("POST /api/versions/{id}/restore", s.handleVersionRestore)
	chronione.HandleFunc("GET /api/users", s.handleUsersList)
	chronione.HandleFunc("POST /api/users", s.handleUserCreate)
	chronione.HandleFunc("DELETE /api/users/{id}", s.handleUserDelete)
	chronione.HandleFunc("POST /api/password", s.handlePasswordChange)
	chronione.HandleFunc("GET /events", s.handleEvents)
	chronione.HandleFunc("GET /icon/{name}", s.handleIcon)
	chronione.HandleFunc("GET /api/appearance", s.handleWyglądGet)
	chronione.HandleFunc("PUT /api/appearance", s.handleWyglądPut)
	chronione.HandleFunc("POST /api/background", s.handleTłoUpload)
	chronione.HandleFunc("GET /api/background", s.handleTłaLista)
	chronione.HandleFunc("DELETE /api/background/{nazwa}", s.handleTłoUsuń)
	chronione.HandleFunc("GET /bg/{nazwa}", s.handleBg)

	mux := http.NewServeMux()
	mux.Handle("/", s.wymagajLogowania(chronione))
	mux.HandleFunc("GET /login", s.handleLoginGET)
	mux.HandleFunc("POST /login", s.handleLoginPOST)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /setup", s.handleSetup)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("POST /lang", s.handleLang)
	mux.HandleFunc("GET /lang", s.handleLang)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /assets/", http.FileServerFS(files))
	// wymagajSameOrigin obejmuje CAŁY mux, więc także /login, /setup i /logout
	return nagłówkiBezpieczeństwa(wymagajSameOrigin(logowanie(mux)))
}

// nagłówkiBezpieczeństwa — CSP bez unsafe-inline dla skryptów zewnętrznych;
// panel i tak nie ładuje niczego spoza własnego origin.
func nagłówkiBezpieczeństwa(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		// ! style-src bez 'unsafe-inline' zostaje — dlatego w szablonie nie ma
		// ani jednego atrybutu style=, a tło ustawia się przez CSSOM
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'; "+
				"object-src 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func logowanie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/events" {
			slog.Debug("req", "m", r.Method, "p", r.URL.Path, "ms", time.Since(start).Milliseconds())
		}
	})
}

type widokUsługi struct {
	Key      string
	Group    string
	Name     string
	URL      string
	Host     string
	IconName string
	Icon     iconRef
	Rozmiar  string // "sm" | "md" | "lg" — nigdy puste
}

type widokGrupy struct {
	Name     string
	Services []widokUsługi
}

// wyglądWidok — zwalidowane wartości do atrybutów data-* w <html>/<body>.
// Serwer wstrzykuje je do pierwszego malowania, żeby panel nie mrugał
// domyślnym tłem, zanim JS przeczyta localStorage.
type wyglądWidok struct {
	Preset string // allowlista config.PresetyTła
	Obraz  string // nazwa pliku z /bg/; niepusta wyłącznie przy Preset=="photo"
	Blur   int    // 0..30
	Dim    int    // 0..80, krycie warstwy przyciemniającej w procentach
	Ton    string // "dark" | "light"
	Widok  string // "grid" | "bento" | "list"
}

// wyglądAPI — kształt wyglądu w JSON (GET /api/appearance, zdarzenie SSE).
type wyglądAPI struct {
	Preset string `json:"preset"`
	Obraz  string `json:"image"`
	Blur   int    `json:"blur"`
	Dim    int    `json:"dim"`
	Ton    string `json:"tone"`
	Widok  string `json:"view"`
}

// wyglądOkna to window.WYGLAD — to samo plus dwie rzeczy, których UI nie ma
// skąd wziąć: czy wolno zapisać wygląd do pliku i jaki jest limit uploadu.
type wyglądOkna struct {
	wyglądAPI
	Zapis   bool `json:"zapis"`
	LimitMB int  `json:"limitMB"`
}

type widokStrony struct {
	Title      string
	Groups     []widokGrupy
	Stan       template.JS
	Wersja     string
	Użytkownik string
	LiczbaIkon int

	Lang                  string
	T                     map[string]string
	TJSON                 template.JS
	DrugiJezyk            string
	DrugiJezykNazwa       string
	DrugiJezykKrotko      string
	SzukajIkonPlaceholder string

	Wyglad     wyglądWidok
	WygladJSON template.JS
	// MoznaPisac — dziś każdy zalogowany może zapisywać. Pole istnieje po to,
	// żeby wprowadzenie ról było jedną linią, a nie przeróbką szablonu.
	MoznaPisac bool
	TłaLimit   int
}

// ── wygląd instancji ─────────────────────────────────────────────────────────

// wyglądInstancji zwraca wygląd z pliku, po walidacji. Load już go wyprostował,
// ale ten kod jest ostatnią bramką przed atrybutem HTML — powtórka kosztuje
// mikrosekundę, a chroni przed każdą przyszłą ścieżką, która ominie Load.
func (s *Server) wyglądInstancji() wyglądWidok {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	w := wyglądWidok{Preset: config.DomyślnyPreset, Blur: config.DomyślnyBlur,
		Dim: config.DomyślnyDim, Ton: config.TonDla(config.DomyślnyPreset),
		Widok: config.DomyślnyWidok}
	if cfg == nil {
		return w
	}
	a := cfg.Appearance
	if config.Dozwolone(a.Background.Preset, config.PresetyTła) {
		w.Preset = a.Background.Preset
		w.Ton = config.TonDla(w.Preset) // preset niesie ton; jawny tone niżej go nadpisze
	}
	if config.Dozwolone(a.Background.Tone, config.TonyTła) {
		w.Ton = a.Background.Tone
	}
	if config.Dozwolone(a.View, config.Widoki) {
		w.Widok = a.View
	}
	if a.Background.Blur >= 0 && a.Background.Blur <= config.MaxBlur {
		w.Blur = a.Background.Blur
	}
	if a.Background.Dim >= 0 && a.Background.Dim <= config.MaxDim {
		w.Dim = a.Background.Dim
	}
	if w.Preset == config.PresetPhoto && config.NazwaTła.MatchString(a.Background.Image) {
		w.Obraz = a.Background.Image
	}
	if w.Preset == config.PresetPhoto && w.Obraz == "" {
		w.Preset = config.DomyślnyPreset // zdjęcie bez pliku to nie jest tło
	}
	// ! na końcu, bo preset mógł się wyżej zmienić. Ton wybiera się wyłącznie
	// przy własnej tapecie — preset niesie jasność, więc i jedyny czytelny ton.
	if w.Preset != config.PresetPhoto {
		w.Ton = config.TonDla(w.Preset)
	}
	return w
}

func (w wyglądWidok) doAPI() wyglądAPI {
	return wyglądAPI{Preset: w.Preset, Obraz: w.Obraz, Blur: w.Blur, Dim: w.Dim, Ton: w.Ton, Widok: w.Widok}
}

// wyglądBramy — tło dla /login i /setup. Tapeta zostaje za logowaniem.
func (s *Server) wyglądBramy() wyglądWidok {
	w := s.wyglądInstancji()
	w.Obraz = ""
	if w.Preset == config.PresetPhoto {
		w.Preset = config.DomyślnyPreset
		if w.Ton == "light" {
			w.Preset = "dawn"
		}
	}
	return w
}

// drugi zwraca język, na który przełącza przycisk — mamy dokładnie dwa.
func drugi(l i18n.Lang) i18n.Lang {
	if l == i18n.PL {
		return i18n.EN
	}
	return i18n.PL
}

// wypełnijJęzyk uzupełnia wszystko, co szablon wie o języku.
func wypełnijJęzyk(l i18n.Lang) (map[string]string, template.JS, string, string, string) {
	t := i18n.Mapa(l)
	b, err := json.Marshal(t)
	if err != nil {
		b = []byte("{}")
	}
	d := drugi(l)
	return t, template.JS(b), string(d), d.Nazwa(), d.Krótko()
}

// Version ustawiane przy buildzie (-ldflags).
var Version = "dev"

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		http.Error(w, "config not loaded", http.StatusServiceUnavailable)
		return
	}

	lang := i18n.Wybierz(r)
	dane := widokStrony{Title: cfg.Title, Wersja: Version, LiczbaIkon: s.ikony.Liczba(), Lang: string(lang)}
	dane.T, dane.TJSON, dane.DrugiJezyk, dane.DrugiJezykNazwa, dane.DrugiJezykKrotko = wypełnijJęzyk(lang)
	dane.Wyglad = s.wyglądInstancji()
	dane.MoznaPisac = true
	dane.TłaLimit = LimitTłaMB
	// ! do szablonu idzie JSON ze STRUKTURY po walidacji, nigdy surowe wejście
	if b, err := json.Marshal(wyglądOkna{wyglądAPI: dane.Wyglad.doAPI(),
		Zapis: dane.MoznaPisac, LimitMB: dane.TłaLimit}); err == nil {
		dane.WygladJSON = template.JS(b)
	} else {
		dane.WygladJSON = template.JS("{}")
	}
	dane.SzukajIkonPlaceholder = fmt.Sprintf(i18n.T(lang, "szukaj_ikon"), s.ikony.Liczba())
	if u := UżytkownikZŻądania(r); u != nil {
		dane.Użytkownik = u.Login
	}
	if dane.Title == "" {
		dane.Title = "Shy Garlic"
	}
	for _, g := range cfg.Groups {
		wg := widokGrupy{Name: g.Name}
		for _, svc := range g.Services {
			rozmiar := svc.Size
			if !config.Dozwolone(rozmiar, config.Rozmiary) {
				rozmiar = config.DomyślnyRozmiar
			}
			wg.Services = append(wg.Services, widokUsługi{
				Key:      key(g.Name, svc.Name),
				Group:    g.Name,
				Name:     svc.Name,
				URL:      svc.URL,
				Host:     hostZ(svc.URL),
				IconName: svc.Icon,
				Icon:     s.rozwiążIkonę(svc.Icon, svc.Name),
				Rozmiar:  rozmiar,
			})
		}
		dane.Groups = append(dane.Groups, wg)
	}

	// stan początkowy wstrzyknięty do HTML — bez tego panel mrugałby "unknown"
	// przez pierwsze pół sekundy po każdym odświeżeniu
	if b, err := json.Marshal(s.mon.Snapshot()); err == nil {
		dane.Stan = template.JS(b)
	} else {
		dane.Stan = template.JS("{}")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// strona jest dynamiczna (stan usług, język, użytkownik) — cache przeglądarki
	// potrafiłby pokazać wersję sprzed przełączenia języka
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", dane); err != nil {
		slog.Error("render", "err", err)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.mon.Snapshot())
}

// handleEvents to strumień SSE — panel dostaje zmiany od razu, bez odpytywania.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	zmiany, odepnij := s.mon.Subscribe()
	defer odepnij()
	wyglądy, odepnijWygląd := s.subskrybujWygląd()
	defer odepnijWygląd()

	// domyślny strumień (bez `event:`) to nadal snapshot zdrowia — nie wolno
	// go ruszyć, bo rysuj() po drugiej stronie nie sprawdza typu zdarzenia
	wyślij := func() bool {
		b, err := json.Marshal(s.mon.Snapshot())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	wyślijWygląd := func() bool {
		b, err := json.Marshal(s.wyglądInstancji().doAPI())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: wyglad\ndata: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !wyślij() {
		return
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-zmiany:
			if !wyślij() {
				return
			}
		case <-wyglądy:
			if !wyślijWygląd() {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func hostZ(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func pierwszaLitera(s string) string {
	for _, r := range strings.TrimSpace(s) {
		return strings.ToUpper(string(r))
	}
	return "?"
}
