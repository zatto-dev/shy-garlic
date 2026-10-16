// Command shyg — panel Shy Garlic.
//
// Użycie:
//
//	shyg serve -config /etc/shygarlic/shygarlic.yaml -addr :8080
//	shyg check  -config ...      (jednorazowe sprawdzenie, do skryptów)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/notify"
	"github.com/zatto-dev/shy-garlic/internal/server"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

var wersja = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("shyg", "err", err)
		os.Exit(1)
	}
}

func run() error {
	polecenie := "serve"
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		polecenie = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	fs := flag.NewFlagSet("shyg", flag.ExitOnError)
	cfgPath := fs.String("config", envLub("SHYG_CONFIG", "shygarlic.yaml"), "ścieżka do pliku konfiguracyjnego")
	addr := fs.String("addr", envLub("SHYG_ADDR", ":8080"), "adres nasłuchu")
	dataPath := fs.String("data", envLub("SHYG_DATA", "shygarlic.db"), "plik SQLite z kontami i historią")
	iconsZip := fs.String("icons", envLub("SHYG_ICONS", ""), "opcjonalny icons.zip — komplet ikon offline, bez pobierania z sieci")
	cacheDir := fs.String("cache", envLub("SHYG_CACHE", ""), "katalog cache ikon (domyślnie obok pliku bazy)")
	webhook := fs.String("webhook", envLub("SHYG_WEBHOOK", os.Getenv("SHYG_DISCORD_WEBHOOK")),
		"adres webhooka powiadomień (Discord albo dowolny przyjmujący JSON)")
	formatWebhooka := fs.String("webhook-format", envLub("SHYG_WEBHOOK_FORMAT", ""),
		"format wiadomości webhooka: "+strings.Join(formatyTekst(), ", ")+" (domyślnie z ustawień panelu, a tam discord; flaga bije panel)")
	prógAwarii := fs.Int("fail-threshold", 2, "ile nieudanych sprawdzeń z rzędu zanim wyślemy powiadomienie (właściwe miejsce to notify.threshold w pliku; flaga go nadpisuje)")
	dockerSock := fs.String("docker", envLub("SHYG_DOCKER", ""),
		"gniazdo Dockera do podpowiadania usług (domyślnie /var/run/docker.sock, jeśli jest)")
	tłaDir := fs.String("backgrounds", envLub("SHYG_BACKGROUNDS", ""),
		"katalog wgranych tapet (domyślnie podkatalog bg/ obok pliku bazy)")
	zaufaneProxy := fs.String("trusted-proxy", envLub("SHYG_TRUSTED_PROXY", ""),
		"CIDR-y proxy, którym wolno ustawiać X-Forwarded-For i X-Forwarded-Proto (po przecinku)")
	metricsAllow := fs.String("metrics-allow", envLub("SHYG_METRICS_ALLOW", ""),
		"CIDR-y, z których /metrics odpowiada bez tokenu (po przecinku; domyślnie żadne — wymagany token)")
	debug := fs.Bool("debug", false, "logi debug")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if *formatWebhooka != "" && !notify.ValidFormat(*formatWebhooka) {
		return fmt.Errorf("--webhook-format %q: dozwolone %s", *formatWebhooka, strings.Join(formatyTekst(), ", "))
	}

	poziom := slog.LevelInfo
	if *debug {
		poziom = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: poziom})))

	o := opcjeCLI{
		cfgPath: *cfgPath, addr: *addr, dataPath: *dataPath,
		iconsZip: *iconsZip, cacheDir: *cacheDir, tłaDir: *tłaDir,
		webhook: *webhook, formatWebhooka: *formatWebhooka, prógAwarii: *prógAwarii, dockerSock: *dockerSock,
		zaufaneProxy: *zaufaneProxy, metricsAllow: *metricsAllow,
	}
	o.prógZFlagi = źródło(fs, "fail-threshold", "") == "flaga"
	o.instancja = opisInstancji(fs, o)

	switch polecenie {
	case "serve":
		return serve(o)
	case "check":
		return check(*cfgPath)
	case "version":
		fmt.Println("shyg", wersja)
		return nil
	default:
		return fmt.Errorf("nieznane polecenie %q (serve|check|version)", polecenie)
	}
}

// opcjeCLI — serve() miało osiem argumentów pozycyjnych, a dostawało dziesiąty.
// Przy takiej liczbie sąsiadujących stringów kompilator już nie łapie zamiany
// dwóch z nich miejscami, a to jest dokładnie ten rodzaj błędu, który potem
// szuka się godzinę.
type opcjeCLI struct {
	cfgPath      string
	addr         string
	dataPath     string
	iconsZip     string
	cacheDir     string
	tłaDir       string
	webhook      string
	// formatWebhooka — format wiadomości z flagi/zmiennej; pusty = z bazy
	formatWebhooka string
	dockerSock   string
	zaufaneProxy string
	metricsAllow string
	prógAwarii   int
	prógZFlagi   bool
	instancja    []server.OpisOpcji
}

// źródło mówi, skąd wzięła się wartość flagi. Flaga podana w linii poleceń
// bije zmienną środowiskową, bo tak działa envLub.
func źródło(fs *flag.FlagSet, nazwa, envKlucz string) string {
	ustawiona := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == nazwa {
			ustawiona = true
		}
	})
	if ustawiona {
		return "flaga"
	}
	if envKlucz != "" && os.Getenv(envKlucz) != "" {
		return "srodowisko"
	}
	return "domyslna"
}

// opisInstancji zbiera opcje startu do pokazania w panelu.
//
// ! Webhooka tu nie ma świadomie. Jest sekretem, więc idzie osobną trasą
// (/api/notify), która go maskuje.
func opisInstancji(fs *flag.FlagSet, o opcjeCLI) []server.OpisOpcji {
	return []server.OpisOpcji{
		{Klucz: "addr", Wartość: o.addr, Źródło: źródło(fs, "addr", "SHYG_ADDR")},
		{Klucz: "config", Wartość: o.cfgPath, Źródło: źródło(fs, "config", "SHYG_CONFIG")},
		{Klucz: "data", Wartość: o.dataPath, Źródło: źródło(fs, "data", "SHYG_DATA")},
		{Klucz: "cache", Wartość: o.cacheDir, Źródło: źródło(fs, "cache", "SHYG_CACHE")},
		{Klucz: "backgrounds", Wartość: o.tłaDir, Źródło: źródło(fs, "backgrounds", "SHYG_BACKGROUNDS")},
		{Klucz: "icons", Wartość: o.iconsZip, Źródło: źródło(fs, "icons", "SHYG_ICONS")},
		{Klucz: "docker", Wartość: o.dockerSock, Źródło: źródło(fs, "docker", "SHYG_DOCKER")},
		{Klucz: "webhook-format", Wartość: o.formatWebhooka, Źródło: źródło(fs, "webhook-format", "SHYG_WEBHOOK_FORMAT")},
		{Klucz: "trusted-proxy", Wartość: o.zaufaneProxy, Źródło: źródło(fs, "trusted-proxy", "SHYG_TRUSTED_PROXY")},
		{Klucz: "metrics-allow", Wartość: o.metricsAllow, Źródło: źródło(fs, "metrics-allow", "SHYG_METRICS_ALLOW")},
	}
}

func formatyTekst() []string {
	out := make([]string, len(notify.Formats))
	for i, f := range notify.Formats {
		out[i] = string(f)
	}
	return out
}

func envLub(klucz, domyślne string) string {
	if v := os.Getenv(klucz); v != "" {
		return v
	}
	return domyślne
}

func serve(o opcjeCLI) error {
	server.Version = wersja

	if o.cacheDir == "" {
		o.cacheDir = filepath.Join(filepath.Dir(o.dataPath), "icons")
	}
	if o.tłaDir == "" {
		o.tłaDir = server.DomyślnyKatalogTeł(o.dataPath)
	}
	if err := sprawdźKatalogi(o); err != nil {
		return err
	}

	st, err := store.Open(o.dataPath)
	if err != nil {
		return fmt.Errorf("baza %s: %w", o.dataPath, err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// wygodne przy pierwszym deployu: konto z ENV zamiast klikania w /setup
	if err := server.BootstrapUżytkownika(ctx, st,
		os.Getenv("SHYG_ADMIN_USER"), os.Getenv("SHYG_ADMIN_PASSWORD")); err != nil {
		slog.Warn("bootstrap konta", "err", err)
	}

	s, err := server.New(o.cfgPath, st, server.Opcje{
		IconsZip: o.iconsZip, CacheDir: o.cacheDir, BackgroundDir: o.tłaDir,
		Webhook: o.webhook, PrógAwarii: o.prógAwarii, DockerSock: o.dockerSock,
		TrustedProxy: rozdziel(o.zaufaneProxy),
		MetricsAllow: rozdziel(o.metricsAllow),

		WebhookZeŚrodowiska: o.webhook != "",
		WebhookFormat:       o.formatWebhooka, WebhookFormatZeŚrodowiska: o.formatWebhooka != "",
		PrógZFlagi:          o.prógZFlagi,
		Instancja:           o.instancja,
	})
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Start(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              o.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// bez WriteTimeout — SSE trzyma połączenie otwarte
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		zamykanie, anuluj := context.WithTimeout(context.Background(), 5*time.Second)
		defer anuluj()
		_ = srv.Shutdown(zamykanie)
	}()

	slog.Info("shyg startuje", "addr", o.addr, "config", o.cfgPath, "baza", o.dataPath,
		"tapety", o.tłaDir, "wersja", wersja)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("shyg zatrzymany")
	return nil
}

func rozdziel(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// sprawdźKatalogi pilnuje, żeby tapety nie lądowały tam, gdzie baza albo config.
//
// ! To nie jest higiena, tylko bezpieczeństwo: plik wgrany do katalogu bazy pod
// nazwą `shygarlic.db` nadpisałby ją, LiczbaUżytkowników() zwróciłoby 0, a
// wtedy każde żądanie leci na /setup, które zakłada konto administratora BEZ
// uwierzytelnienia. Wgranie obrazka byłoby przejęciem panelu.
func sprawdźKatalogi(o opcjeCLI) error {
	tła, err := filepath.Abs(o.tłaDir)
	if err != nil {
		return err
	}
	for _, z := range []struct{ nazwa, ścieżka string }{
		{"katalogiem bazy", filepath.Dir(o.dataPath)},
		{"katalogiem configu", filepath.Dir(o.cfgPath)},
		{"cache ikon", o.cacheDir},
	} {
		inny, err := filepath.Abs(z.ścieżka)
		if err != nil {
			return err
		}
		if tła == inny {
			return fmt.Errorf("katalog tapet (%s) nie może być tym samym co %s (%s) — "+
				"wgrany plik mógłby nadpisać bazę albo config", tła, z.nazwa, inny)
		}
	}
	if baza, err := filepath.Abs(o.dataPath); err == nil && baza == tła {
		return fmt.Errorf("katalog tapet nie może być plikiem bazy (%s)", baza)
	}
	return nil
}

// check wypisuje, co panel widzi w configu — do debugowania bez przeglądarki.
func check(cfgPath string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(raw)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", cfg.Title)
	for _, g := range cfg.Groups {
		fmt.Printf("\n%s\n", g.Name)
		for _, s := range g.Services {
			fmt.Printf("  %-18s %-38s co %s, oczekiwane %v\n",
				s.Name, s.URL, s.Check.Interval, s.Check.Expect)
		}
	}
	return nil
}
