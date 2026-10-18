// Command shyg — the Shy Garlic panel.
//
// Usage:
//
//	shyg serve -config /etc/shygarlic/shygarlic.yaml -addr :8080
//	shyg check  -config ...      (one-off check, for scripts)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
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

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("shyg", "err", err)
		os.Exit(1)
	}
}

func run() error {
	command := "serve"
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		command = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	fs := flag.NewFlagSet("shyg", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("SHYG_CONFIG", "shygarlic.yaml"), "path to the config file")
	seedCfg := fs.String("seed-config", envOr("SHYG_SEED_CONFIG", ""),
		"copy this file to -config on start when -config does not exist yet (an existing config is never touched)")
	addr := fs.String("addr", envOr("SHYG_ADDR", ":8080"), "listen address")
	dataPath := fs.String("data", envOr("SHYG_DATA", "shygarlic.db"), "SQLite file with accounts and history")
	iconsZip := fs.String("icons", envOr("SHYG_ICONS", ""), "optional icons.zip — a full offline icon set, no downloading from the network")
	iconsURL := fs.String("icons-url", envOr("SHYG_ICONS_URL", server.DefaultIconsURL),
		"icon source: the base URL of an icons.zatto.dev-compatible server (index.json, svg/, png/); point it at your own mirror to keep icon lookups in-house")
	cacheDir := fs.String("cache", envOr("SHYG_CACHE", ""), "icon directory (default: icons/ next to the DB file): holds only the icons your services use and the icon index; any other .svg/.png there is deleted")
	webhook := fs.String("webhook", envOr("SHYG_WEBHOOK", os.Getenv("SHYG_DISCORD_WEBHOOK")),
		"notification webhook URL (Discord or anything that accepts JSON)")
	webhookFormat := fs.String("webhook-format", envOr("SHYG_WEBHOOK_FORMAT", ""),
		"webhook message format: "+strings.Join(formatTexts(), ", ")+" (default: from the panel settings, and there discord; the flag beats the panel)")
	failThreshold := fs.Int("fail-threshold", 2, "how many failed checks in a row before a notification is sent (the proper place is notify.threshold in the file; the flag overrides it)")
	dockerSock := fs.String("docker", envOr("SHYG_DOCKER", ""),
		"Docker socket for suggesting services (default: /var/run/docker.sock, if present)")
	backgroundDir := fs.String("backgrounds", envOr("SHYG_BACKGROUNDS", ""),
		"directory of uploaded wallpapers (default: the bg/ subdirectory next to the DB file)")
	trustedProxy := fs.String("trusted-proxy", envOr("SHYG_TRUSTED_PROXY", ""),
		"proxy CIDRs allowed to set X-Forwarded-For and X-Forwarded-Proto (comma-separated)")
	metricsAllow := fs.String("metrics-allow", envOr("SHYG_METRICS_ALLOW", ""),
		"CIDRs from which /metrics answers without a token (comma-separated; default none — token required)")
	allowedHost := fs.String("allowed-host", envOr("SHYG_ALLOWED_HOSTS", ""),
		"Host names the panel answers to, comma-separated, without ports (default: any). "+
			"Set it to the name you open the panel with; other names get 421 (protects against DNS rebinding). Loopback always works")
	debug := fs.Bool("debug", false, "debug logs")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if *webhookFormat != "" && !notify.ValidFormat(*webhookFormat) {
		return fmt.Errorf("--webhook-format %q: allowed %s", *webhookFormat, strings.Join(formatTexts(), ", "))
	}

	iconsBase, err := server.NormalizeIconsURL(*iconsURL)
	if err != nil {
		return fmt.Errorf("--icons-url: %w", err)
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	o := cliOptions{
		cfgPath: *cfgPath, seedConfig: *seedCfg, addr: *addr, dataPath: *dataPath,
		iconsZip: *iconsZip, iconsURL: iconsBase, cacheDir: *cacheDir, backgroundDir: *backgroundDir,
		webhook: *webhook, webhookFormat: *webhookFormat, failThreshold: *failThreshold, dockerSock: *dockerSock,
		trustedProxy: *trustedProxy, metricsAllow: *metricsAllow, allowedHost: *allowedHost,
	}
	o.thresholdFromFlag = source(fs, "fail-threshold", "") == "flag"
	o.instance = instanceDescription(fs, o)

	switch command {
	case "serve":
		return serve(o)
	case "check":
		return check(*cfgPath)
	case "version":
		fmt.Println("shyg", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (serve|check|version)", command)
	}
}

// cliOptions — serve() had eight positional arguments and was getting a tenth.
// With that many adjacent strings the compiler no longer catches two of them
// being swapped, and that is exactly the kind of bug you then hunt for an hour.
type cliOptions struct {
	cfgPath       string
	seedConfig    string
	addr          string
	dataPath      string
	iconsZip      string
	iconsURL      string
	cacheDir      string
	backgroundDir string
	webhook       string
	// webhookFormat — message format from a flag/variable; empty = from the DB
	webhookFormat     string
	dockerSock        string
	trustedProxy      string
	metricsAllow      string
	allowedHost       string
	failThreshold     int
	thresholdFromFlag bool
	instance          []server.OptionInfo
}

// source says where a flag's value came from. A flag given on the command line
// beats an environment variable, because that is how envOr works.
func source(fs *flag.FlagSet, name, envKey string) string {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	if set {
		return "flag"
	}
	if envKey != "" && os.Getenv(envKey) != "" {
		return "env"
	}
	return "default"
}

// instanceDescription gathers the start-up options to show in the panel.
//
// ! The webhook is deliberately absent. It is a secret, so it goes through a
// separate route (/api/notify) that masks it.
func instanceDescription(fs *flag.FlagSet, o cliOptions) []server.OptionInfo {
	return []server.OptionInfo{
		{Key: "addr", Value: o.addr, Source: source(fs, "addr", "SHYG_ADDR")},
		{Key: "config", Value: o.cfgPath, Source: source(fs, "config", "SHYG_CONFIG")},
		{Key: "seed-config", Value: o.seedConfig, Source: source(fs, "seed-config", "SHYG_SEED_CONFIG")},
		{Key: "data", Value: o.dataPath, Source: source(fs, "data", "SHYG_DATA")},
		{Key: "cache", Value: o.cacheDir, Source: source(fs, "cache", "SHYG_CACHE")},
		{Key: "backgrounds", Value: o.backgroundDir, Source: source(fs, "backgrounds", "SHYG_BACKGROUNDS")},
		{Key: "icons", Value: o.iconsZip, Source: source(fs, "icons", "SHYG_ICONS")},
		{Key: "icons-url", Value: o.iconsURL, Source: source(fs, "icons-url", "SHYG_ICONS_URL")},
		{Key: "docker", Value: o.dockerSock, Source: source(fs, "docker", "SHYG_DOCKER")},
		{Key: "webhook-format", Value: o.webhookFormat, Source: source(fs, "webhook-format", "SHYG_WEBHOOK_FORMAT")},
		{Key: "trusted-proxy", Value: o.trustedProxy, Source: source(fs, "trusted-proxy", "SHYG_TRUSTED_PROXY")},
		{Key: "metrics-allow", Value: o.metricsAllow, Source: source(fs, "metrics-allow", "SHYG_METRICS_ALLOW")},
		{Key: "allowed-host", Value: o.allowedHost, Source: source(fs, "allowed-host", "SHYG_ALLOWED_HOSTS")},
	}
}

func formatTexts() []string {
	out := make([]string, len(notify.Formats))
	for i, f := range notify.Formats {
		out[i] = string(f)
	}
	return out
}

func envOr(key, defaults string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaults
}

func serve(o cliOptions) error {
	server.Version = version

	if o.seedConfig != "" {
		seeded, err := seedConfig(o.cfgPath, o.seedConfig)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// the image sets a default seed; a deployment that mounts something
			// else over it still starts, exactly as without --seed-config
			slog.Warn("seed config not found, starting without seeding", "seed", o.seedConfig)
		case err != nil:
			return err
		}
		if seeded {
			slog.Info("config seeded", "from", o.seedConfig, "to", o.cfgPath)
		}
	}

	if o.cacheDir == "" {
		o.cacheDir = filepath.Join(filepath.Dir(o.dataPath), "icons")
	}

	if o.backgroundDir == "" {
		o.backgroundDir = server.DefaultBackgroundDir(o.dataPath)
	}
	if err := checkDirs(o); err != nil {
		return err
	}

	st, err := store.Open(o.dataPath)
	if err != nil {
		return fmt.Errorf("db %s: %w", o.dataPath, err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// handy on the first deploy: an account from ENV instead of clicking through /setup
	if err := server.BootstrapUser(ctx, st,
		os.Getenv("SHYG_ADMIN_USER"), os.Getenv("SHYG_ADMIN_PASSWORD")); err != nil {
		slog.Warn("account bootstrap", "err", err)
	}

	s, err := server.New(o.cfgPath, st, server.Options{
		IconsZip: o.iconsZip, IconsURL: o.iconsURL, CacheDir: o.cacheDir, BackgroundDir: o.backgroundDir,
		Webhook: o.webhook, FailThreshold: o.failThreshold, DockerSock: o.dockerSock,
		TrustedProxy: split(o.trustedProxy),
		MetricsAllow: split(o.metricsAllow),
		AllowedHosts: split(o.allowedHost),

		WebhookFromEnv: o.webhook != "",
		WebhookFormat:  o.webhookFormat, WebhookFormatFromEnv: o.webhookFormat != "",
		ThresholdFromFlag: o.thresholdFromFlag,
		Instance:          o.instance,
	})
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Start(ctx); err != nil {
		return err
	}

	srv := server.NewHTTPServer(o.addr, s.Handler())

	go func() {
		<-ctx.Done()
		closing, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(closing)
	}()

	slog.Info("shyg starting", "addr", o.addr, "config", o.cfgPath, "db", o.dataPath,
		"backgrounds", o.backgroundDir, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("shyg stopped")
	return nil
}

func split(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// checkDirs makes sure wallpapers do not land where the DB or the config live.
//
// ! This is not hygiene but security: a file uploaded into the DB directory under
// the name `shygarlic.db` would overwrite it, UserCount() would return 0, and
// then every request goes to /setup, which creates an administrator account
// WITHOUT authentication. Uploading an image would be a panel takeover.
func checkDirs(o cliOptions) error {
	backgrounds, err := filepath.Abs(o.backgroundDir)
	if err != nil {
		return err
	}
	for _, z := range []struct{ name, path string }{
		{"the DB directory", filepath.Dir(o.dataPath)},
		{"the config directory", filepath.Dir(o.cfgPath)},
		{"the icon cache", o.cacheDir},
	} {
		other, err := filepath.Abs(z.path)
		if err != nil {
			return err
		}
		if backgrounds == other {
			return fmt.Errorf("backgrounds directory (%s) cannot be the same as %s (%s) — "+
				"an uploaded file could overwrite the DB or the config", backgrounds, z.name, other)
		}
	}
	if db, err := filepath.Abs(o.dataPath); err == nil && db == backgrounds {
		return fmt.Errorf("backgrounds directory cannot be the DB file (%s)", db)
	}
	return nil
}

// check prints what the panel sees in the config — for debugging without a browser.
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
			fmt.Printf("  %-18s %-38s every %s, expect %v\n",
				s.Name, s.URL, s.Check.Interval, s.Check.Expect)
		}
	}
	return nil
}
