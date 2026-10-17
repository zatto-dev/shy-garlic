// Package server serves the panel: HTML, status as JSON and a stream of changes over SSE.
//
// Everything happens on the server side: checking services, fetching icons,
// reading the config. The browser does not make a single outbound request —
// thanks to that the panel works without internet, and the user's list of services
// leaks nowhere. The competition hotlinks icons to jsDelivr straight from the browser.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
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
	path    string
	monitor *health.Monitor
	tmpl    *template.Template
	st      *store.Store
	lim     *limiter
	setup   setupGate
	// allowedHosts — Host names the panel answers to (empty = any); set once in New.
	allowedHosts map[string]bool
	// sseStreams counts open /events streams per user — see handleEvents.
	sseMu       sync.Mutex
	sseStreams  map[int64]int
	icons       *Catalog
	backgrounds *BackgroundStore
	notifier    *notify.Notifier
	docker      *dockerdisc.Client

	// webhookFromEnv and instance are set once, in New, and never afterwards —
	// hence no mutex.
	webhookFromEnv bool
	// formatFromEnv — message format from a flag or variable; it has the same
	// precedence as the address, but is independent of it.
	formatFromEnv bool
	// thresholdFromFlag — --fail-threshold given explicitly. Then the file does not touch it,
	// the same as with the webhook: what came from outside at start
	// belongs to the deploy.
	thresholdFromFlag bool
	instance          []OptionInfo
	// API tokens and /metrics (SHYG-49).
	tokenLimiter *limiter // bad tokens — separate from the login limit
	tokenLogGate *logGate // one log line per address per minute
	// metricsCIDR — networks from which /metrics responds without a token; set
	// once in New, read-only afterwards.
	metricsCIDR []netip.Prefix

	usageMu    sync.Mutex
	tokenUsage map[int64]time.Time // when we last wrote last_used_at

	availabilityMu    sync.Mutex
	availabilityMap   map[string]store.ServiceAvailability
	availabilityUntil time.Time
	availabilityTTL   time.Duration

	// proxyClient fetches icons given as a full address; a separate field, because a test
	// must be able to substitute a client without the private address block
	proxyClient *http.Client
	ctx         context.Context

	// edit serializes writes to the file — two concurrent clicks in the UI
	// must not overwrite each other
	edit sync.Mutex

	// reloadMu serializes reload(): it is called by Start, a save from the panel (under
	// edit) and watch(). Lock order is always edit → reloadMu.
	reloadMu sync.Mutex

	// last — marker of the last result written to the DB, per key.
	// On the server field (not local in saveHistory), because a key change must
	// rewire it, otherwise the same result would reach the history a second time.
	lastMu sync.Mutex
	last   map[string]time.Time

	mu    sync.RWMutex
	cfg   *config.Config
	stamp string // size+mtime of the file, to detect an external edit

	// File state: an error (cannot be loaded → the panel runs on the previous
	// configuration) and warnings (it loaded, but something in it is suspicious).
	// Protected by mu.
	fileErr   *config.FileError
	warnings  []config.Warning
	cfgSource string // "" | "poprzednia" | "historia" | "pusta" — where the config comes from on error
	lastErr   string // for logging each error once, not on every watch() tick

	// the second SSE sender: appearance changes. Separate from health.Monitor, because that
	// stream is untyped (data: without event:) and draw() would trip over it.
	appearanceMu   sync.Mutex
	appearanceSubs map[chan struct{}]struct{}

	// a cheaply computed counter of Docker candidates — /api/info is called on every
	// panel open, and the socket does not need to look at that every time
	muDocker    sync.Mutex
	dockerCount int
	dockerAt    time.Time
}

// Options gathers what is not the path to the config.
type Options struct {
	IconsZip      string   // optional full offline icon set
	CacheDir      string   // cache of icons fetched from the CDN
	BackgroundDir string   // directory of uploaded wallpapers (empty = feature disabled)
	Webhook       string   // notification webhook address (empty = disabled)
	FailThreshold int      // how many failed checks in a row before we notify
	DockerSock    string   // Docker socket for suggesting services (empty = default)
	TrustedProxy  []string // CIDRs of proxies allowed to say X-Forwarded-*
	MetricsAllow  []string // CIDRs from which /metrics responds without a token (default: none)
	AllowedHosts  []string // Host names the panel answers to (default: any) — protection against DNS rebinding

	// WebhookFromEnv — the address came from a flag or a variable, so the
	// panel will not overwrite it. The deploy should win over clicking: otherwise a restart of
	// the container would silently undo a change made in the UI.
	WebhookFromEnv bool
	// WebhookFormat — message format given by a flag/variable; empty = from the DB.
	WebhookFormat        string
	WebhookFormatFromEnv bool
	// ThresholdFromFlag — --fail-threshold given explicitly on the command line.
	ThresholdFromFlag bool
	// Instance — start-up options to show in the panel, read-only.
	Instance []OptionInfo
}

// OptionInfo — one instance option for the needs of the "Instance" tab.
//
// ! Only what is safe to reveal lands here. The webhook, although
// it also comes from a flag, does not go in here — /api/notify is for it, and it
// masks it.
type OptionInfo struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"` // flag | env | default
}

func New(path string, st *store.Store, o Options) (*Server, error) {
	tmpl, err := template.ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	if err := SetTrustedProxy(o.TrustedProxy); err != nil {
		return nil, err
	}
	s := &Server{
		path: path, monitor: health.New(), tmpl: tmpl, st: st,
		lim: newLimiter(), icons: NewCatalog(o.IconsZip, o.CacheDir),
		backgrounds: NewBackgroundStore(o.BackgroundDir),
		notifier:    notify.New(o.Webhook, o.FailThreshold),
		docker:      dockerdisc.New(o.DockerSock),

		webhookFromEnv:    o.WebhookFromEnv,
		formatFromEnv:     o.WebhookFormatFromEnv && o.WebhookFormat != "",
		thresholdFromFlag: o.ThresholdFromFlag,
		instance:          o.Instance,

		proxyClient:    externalIconClient(),
		appearanceSubs: map[chan struct{}]struct{}{},
		last:           map[string]time.Time{},

		tokenLimiter:    newLimiterWithThreshold(badTokenThreshold),
		tokenLogGate:    &logGate{period: time.Minute},
		tokenUsage:      map[int64]time.Time{},
		availabilityTTL: 30 * time.Second,

		allowedHosts: parseAllowedHosts(o.AllowedHosts),
		sseStreams:   map[int64]int{},
	}
	if err := s.setMetricsAllow(o.MetricsAllow); err != nil {
		return nil, err
	}

	// We lift the address from the DB only when the environment imposed nothing —
	// the precedence must be the same here and in handleNotifyPut, otherwise the panel
	// would show one thing and the monitor use another.
	if !o.WebhookFromEnv {
		address, err := st.Setting(context.Background(), "notify.webhook")
		if err != nil {
			return nil, fmt.Errorf("reading webhook from DB: %w", err)
		}
		if address != "" {
			s.notifier.SetWebhook(address)
		}
	}

	// Format: the same precedence — environment, then DB, then discord.
	// An unknown format from the DB (e.g. from a newer version) must not disable alerts,
	// so SetFormat turns it into the default.
	if s.formatFromEnv {
		s.notifier.SetFormat(notify.Format(o.WebhookFormat))
	} else if f, err := st.Setting(context.Background(), "notify.format"); err != nil {
		return nil, fmt.Errorf("reading webhook format from DB: %w", err)
	} else if f != "" {
		s.notifier.SetFormat(notify.Format(f))
	}
	// A fresh instance: print the setup token now, so it is in the log before the
	// first visitor arrives.
	if n, err := st.UserCount(context.Background()); err == nil && n == 0 {
		s.SetupToken()
	}
	return s, nil
}

// ── file state banner ────────────────────────────────────────────────────────

// bannerAPI — what the card shows above the list of services: a file error (with the
// line number) and/or warnings. Texts ready, in the language of the request.
type bannerAPI struct {
	State    string   `json:"state"` // ok | error | warnings
	Title    string   `json:"title"`
	Error    string   `json:"error"`
	Warnings []string `json:"warnings"`
}

func (s *Server) instanceBanner(l i18n.Lang) bannerAPI {
	s.mu.RLock()
	err := s.fileErr
	warns := append([]config.Warning(nil), s.warnings...)
	source := s.cfgSource
	s.mu.RUnlock()

	b := bannerAPI{State: "ok", Warnings: []string{}}
	if err != nil {
		// warnings concern the PREVIOUS version of the file (line numbers have
		// drifted) — on error we do not show them next to the new one
		warns = nil
	}
	for _, o := range warns {
		b.Warnings = append(b.Warnings, warningDescription(l, o))
	}
	if len(b.Warnings) > 0 {
		b.State = "warnings"
		b.Title = i18n.T(l, "banner_warn_title")
	}
	if err != nil {
		b.State = "error"
		key := "banner_error_previous"
		switch source {
		case "historia":
			key = "banner_error_history"
		case "pusta":
			key = "banner_error_empty"
		}
		b.Title = i18n.T(l, key)
		switch {
		case err.Line > 0 && err.Column > 0:
			b.Error = fmt.Sprintf(i18n.T(l, "banner_position"), err.Line, err.Column, err.Message)
		case err.Line > 0:
			b.Error = fmt.Sprintf(i18n.T(l, "banner_line"), err.Line, err.Message)
		default:
			b.Error = err.Message
		}
	}
	return b
}

func warningDescription(l i18n.Lang, o config.Warning) string {
	switch o.Kind {
	case config.KindCheck:
		return fmt.Sprintf(i18n.T(l, "banner_warn_check"), o.Service, o.Group, o.Key, o.Address, o.Suggestion)
	case config.KindCredentials:
		return fmt.Sprintf(i18n.T(l, "banner_warn_credentials"), o.Service, o.Group, o.Address)
	}
	if o.Kind == config.KindAddress {
		return fmt.Sprintf(i18n.T(l, "banner_warn_address"), o.Service, o.Group, o.Address)
	}
	where := o.Path
	if where == "" {
		where = i18n.T(l, "banner_top_level")
	}
	t := fmt.Sprintf(i18n.T(l, "banner_warn_key"), o.Key, where)
	if o.Suggestion != "" {
		t += " " + fmt.Sprintf(i18n.T(l, "banner_warn_suggestion"), o.Suggestion)
	}
	if o.Line > 0 {
		t = fmt.Sprintf(i18n.T(l, "banner_warn_line"), o.Line) + ": " + t
	}
	return t
}

// ── broadcasting an appearance change ────────────────────────────────────────

func (s *Server) subscribeAppearance() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	s.appearanceMu.Lock()
	s.appearanceSubs[c] = struct{}{}
	s.appearanceMu.Unlock()
	return c, func() {
		s.appearanceMu.Lock()
		delete(s.appearanceSubs, c)
		s.appearanceMu.Unlock()
	}
}

// broadcastAppearance wakes the open cards. Called from reload(), NOT from withEdit —
// thanks to that changes made to the file from outside also reach them (git pull,
// Ansible), which today wake no subscriber.
func (s *Server) broadcastAppearance() {
	s.appearanceMu.Lock()
	subs := make([]chan struct{}, 0, len(s.appearanceSubs))
	for c := range s.appearanceSubs {
		subs = append(subs, c)
	}
	s.appearanceMu.Unlock()
	for _, c := range subs {
		select {
		case c <- struct{}{}:
		default: // the receiver cannot keep up — it will send the full state anyway
		}
	}
}

// configBytes reads the configuration file from disk.
func configBytes(path string) ([]byte, error) { return os.ReadFile(path) }

// Start loads the config, starts checking and watches the file.
func (s *Server) Start(ctx context.Context) error {
	s.ctx = ctx
	if err := s.loadAfterStart(ctx); err != nil {
		return err
	}
	go s.watch(ctx)
	go s.saveHistory(ctx)
	go s.cleanup(ctx)
	go func() {
		s.icons.Refresh(ctx)
		slog.Info("icons", "mode", s.icons.Mode(), "available", s.icons.Count())
	}()
	return nil
}

// saveHistory copies the check results to SQLite. Thanks to that the panel knows
// what it was an hour ago — no lightweight panel on the market has this.
func (s *Server) saveHistory(ctx context.Context) {
	changes, unpin := s.monitor.Subscribe()
	defer unpin()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			for key, r := range s.monitor.Snapshot() {
				s.lastMu.Lock()
				new := !r.At.IsZero() && r.At.After(s.last[key])
				if new {
					s.last[key] = r.At
				}
				s.lastMu.Unlock()
				if !new {
					continue
				}
				err := s.st.SaveResult(ctx, store.Result{
					Key: key, State: string(r.State), Code: r.Code,
					MS: r.MS, Err: r.Err, At: r.At,
				})
				if err != nil {
					slog.Debug("history write", "err", err)
				}
				s.notify(ctx, key, r)
			}
		}
	}
}

// migrateKeys moves everything indexed by the service key
// (group/name), after a rename or a move to another group: the history
// in SQLite, the monitor state and the notifier state. Called after a successful YAML save
// and BEFORE reload() — reload throws away the state of keys it does not know.
//
// ! A DB migration error is only logged: the already-saved YAML is the source of
// truth and must not be rolled back for a chart. The worst outcome is an empty bar.
func (s *Server) migrateKeys(ctx context.Context, pairs map[string]string) {
	if len(pairs) == 0 {
		return
	}
	if err := s.st.MoveKeys(ctx, pairs); err != nil {
		slog.Error("history migration after key change", "err", err, "pairs", len(pairs))
	}
	s.monitor.MoveKeys(pairs)
	s.notifier.Move(pairs)
	s.lastMu.Lock()
	for old, new := range pairs {
		if old == new {
			continue
		}
		if t, ok := s.last[old]; ok {
			s.last[new] = t
			delete(s.last, old)
		}
	}
	s.lastMu.Unlock()
}

// notify passes the result to the notifier together with the service description.
func (s *Server) notify(ctx context.Context, serviceKey string, r health.Result) {
	if !s.notifier.Enabled() {
		return
	}
	group, name, _ := strings.Cut(serviceKey, "/")
	address := ""
	s.mu.RLock()
	if s.cfg != nil {
		for _, g := range s.cfg.Groups {
			for _, svc := range g.Services {
				if key(g.Name, svc.Name) == serviceKey {
					address = svc.URL
				}
			}
		}
	}
	s.mu.RUnlock()

	s.notifier.Observe(ctx, notify.Event{
		Key: serviceKey, Name: name, Group: group, URL: config.RedactURL(address),
		State: string(r.State), Code: r.Code, Err: r.Err, MS: r.MS, At: r.At,
	})
}

// cleanup removes expired sessions and history older than 30 days.
func (s *Server) cleanup(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		_ = s.st.CleanupSessions(ctx)
		if n, err := s.st.Prune(ctx, 30*24*time.Hour); err == nil && n > 0 {
			slog.Info("history pruned", "removed", n)
		}
		// the only place where the panel itself deletes a file from disk — and only one
		// that nobody uses, is older than 30 days and pushes the directory
		// over the limit
		s.backgrounds.SweepStale(s.instanceAppearance().Image)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) Close() {
	s.monitor.Close()
	s.backgrounds.Close()
}

// loadAfterStart loads the config at start-up, but does not die on a bad file.
//
// ! Previously an error in the YAML ended the process, and in Docker with restart: always that is
// a restart loop with no page where one could read why.
// Now the panel comes up, and the banner shows the error with a line number. The substitute config is
// the newest version from the SQLite history that parses; without it — an empty one.
// We do not touch the file on disk: that is the very thing the human is fixing.
func (s *Server) loadAfterStart(ctx context.Context) error {
	if err := s.reload(ctx); err == nil {
		return nil
	}
	cfg, source := s.lastGoodFromHistory(ctx)
	s.mu.Lock()
	s.cfg = cfg
	s.cfgSource = source
	s.mu.Unlock()
	s.apply(ctx, cfg)
	slog.Warn("starting despite config error", "fallback", source)
	return nil
}

func (s *Server) lastGoodFromHistory(ctx context.Context) (*config.Config, string) {
	if versions, err := s.st.Versions(ctx, 20); err == nil {
		for _, w := range versions {
			full, err := s.st.Version(ctx, w.ID)
			if err != nil {
				continue
			}
			if cfg, err := config.Load([]byte(full.Content)); err == nil {
				return cfg, "historia"
			}
		}
	}
	empty, _ := config.Load([]byte("groups: []\n"))
	return empty, "pusta"
}

func (s *Server) reload(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	// ! stamp BEFORE the read: a file changed between the read and stat() would give a
	// stamp newer than the loaded content and the change would be lost for good
	stamp := s.stampOf()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return s.brokenConfig(stamp, config.FileError{Message: "read config: " + err.Error()}, err)
	}
	cfg, err := config.Load(raw)
	if err != nil {
		return s.brokenConfig(stamp, config.ParseFileError(err), err)
	}

	warnings := append(config.Warnings(raw), config.AddressWarnings(cfg)...)
	warnings = append(warnings, cfg.LoadWarnings...)
	for _, o := range warnings {
		slog.Warn("config: suspicious spot", "kind", o.Kind, "line", o.Line,
			"key", o.Key, "path", o.Path, "service", o.Service)
	}

	s.mu.Lock()
	s.cfg = cfg
	s.stamp = stamp
	s.fileErr = nil
	s.warnings = warnings
	s.cfgSource = ""
	s.lastErr = ""
	s.mu.Unlock()

	s.apply(ctx, cfg)
	return nil
}

// brokenConfig remembers the file error. The panel stays on the previous
// configuration, and the stamp is set DESPITE the error — thanks to that watch() does not
// retry the same file every 2 s, but waits for the next change.
func (s *Server) brokenConfig(stamp string, b config.FileError, err error) error {
	description := fmt.Sprintf("%d:%d:%s", b.Line, b.Column, b.Message)
	s.mu.Lock()
	s.stamp = stamp
	new := description != s.lastErr
	s.lastErr = description
	s.fileErr = &b
	s.mu.Unlock()
	if new { // each distinct error once
		slog.Error("config invalid — panel keeps the previous configuration",
			"line", b.Line, "column", b.Column, "err", b.Message)
	}
	s.broadcastAppearance()
	return err
}

// apply starts the monitor, the notifier and the cards on the given config.
func (s *Server) apply(ctx context.Context, cfg *config.Config) {
	// ! The threshold must get in here, not only on a save from the panel. Without that
	// setting notify.threshold in the file and restarting quietly restored the
	// default value — and in this project the file is the source of truth.
	if !s.thresholdFromFlag {
		s.notifier.SetThreshold(cfg.Notify.Threshold)
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

				Path:        svc.Check.Path,
				Contains:    svc.Check.Contains,
				NotContains: svc.Check.NotContains,
			})
		}
	}

	s.monitor.SetTargets(ctx, targets)
	live := make(map[string]bool, len(targets))
	for _, t := range targets {
		live[t.Key] = true
	}
	s.notifier.Forget(live)
	s.broadcastAppearance()
	slog.Info("config loaded", "services", len(targets), "groups", len(cfg.Groups))
}

// watch reacts to a manual edit of the file — the file is the source of truth, so the panel
// must adapt to it, not the other way round.
func (s *Server) watch(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.checkFileChange(ctx)
		}
	}
}

// checkFileChange reloads the config if the file changed on disk.
//
// ! Under s.edit: a save from the panel holds this mutex from Save all the way to reload(), so
// watch never reloads in the middle of a save (between Save and history/git
// the stamp is still old). After leaving the save the stamp already matches and
// watch does nothing.
func (s *Server) checkFileChange(ctx context.Context) {
	s.edit.Lock()
	defer s.edit.Unlock()
	s.mu.RLock()
	old := s.stamp
	s.mu.RUnlock()
	if new := s.stampOf(); new != "" && new != old {
		slog.Info("config changed on disk, reloading")
		// the error is logged and shown by reload() — once per distinct error
		_ = s.reload(ctx)
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
	// routes behind login
	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.handleIndex)
	protected.HandleFunc("GET /api/status", s.handleStatus)
	protected.HandleFunc("GET /api/history", s.handleHistory)
	protected.HandleFunc("GET /metrics", s.handleMetrics)
	protected.HandleFunc("GET /api/tokens", s.handleTokensList)
	protected.HandleFunc("POST /api/tokens", s.handleTokenCreate)
	protected.HandleFunc("DELETE /api/tokens/{id}", s.handleTokenDelete)
	protected.HandleFunc("GET /api/service", s.handleService)
	protected.HandleFunc("POST /api/check", s.handleCheckNow)
	protected.HandleFunc("POST /api/notify/test", s.handleNotifyTest)
	protected.HandleFunc("GET /api/notify", s.handleNotifyGet)
	protected.HandleFunc("PUT /api/notify", s.handleNotifyPut)
	protected.HandleFunc("GET /api/instance", s.handleInstance)
	protected.HandleFunc("GET /api/info", s.handleInfo)
	protected.HandleFunc("GET /api/discover", s.handleDiscover)
	protected.HandleFunc("POST /api/services/bulk", s.handleServicesBulk)
	protected.HandleFunc("GET /api/icons", s.handleIcons)
	protected.HandleFunc("GET /api/config", s.handleConfigGet)
	protected.HandleFunc("PUT /api/config", s.handleConfigPut)
	protected.HandleFunc("POST /api/services", s.handleServiceCreate)
	protected.HandleFunc("PATCH /api/services", s.handleServiceUpdate)
	protected.HandleFunc("DELETE /api/services", s.handleServiceDelete)
	protected.HandleFunc("POST /api/services/move", s.handleServiceMove)
	protected.HandleFunc("POST /api/groups/move", s.handleGroupMove)
	protected.HandleFunc("PUT /api/title", s.handleTitlePut)
	protected.HandleFunc("POST /api/groups", s.handleGroupCreate)
	protected.HandleFunc("PATCH /api/groups", s.handleGroupUpdate)
	protected.HandleFunc("DELETE /api/groups", s.handleGroupDelete)
	protected.HandleFunc("GET /api/versions", s.handleVersionsList)
	protected.HandleFunc("GET /api/versions/{id}", s.handleVersionOne)
	protected.HandleFunc("POST /api/versions/{id}/restore", s.handleVersionRestore)
	protected.HandleFunc("GET /api/users", s.handleUsersList)
	protected.HandleFunc("POST /api/users", s.handleUserCreate)
	protected.HandleFunc("DELETE /api/users/{id}", s.handleUserDelete)
	protected.HandleFunc("POST /api/password", s.handlePasswordChange)
	protected.HandleFunc("GET /events", s.handleEvents)
	protected.HandleFunc("GET /icon/{name}", s.handleIcon)
	protected.HandleFunc("GET /api/appearance", s.handleAppearanceGet)
	protected.HandleFunc("PUT /api/appearance", s.handleAppearancePut)
	protected.HandleFunc("POST /api/background", s.handleBackgroundUpload)
	protected.HandleFunc("GET /api/background", s.handleBackgroundList)
	protected.HandleFunc("DELETE /api/background/{name}", s.handleBackgroundRemove)
	protected.HandleFunc("GET /bg/{name}", s.handleBg)

	mux := http.NewServeMux()
	mux.Handle("/", s.requireLogin(protected))
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
	mux.Handle("GET /assets/", noDirectoryListing(http.FileServerFS(files)))
	// requireSameOrigin covers the WHOLE mux, so also /login, /setup and /logout.
	// The Host check goes first: a request for a foreign name never reaches anything.
	return securityHeaders(requireAllowedHost(s.allowedHosts, requireSameOrigin(logging(mux))))
}

// noDirectoryListing answers 404 for a directory, instead of the file server's
// generated index of the embedded files.
func noDirectoryListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders — CSP without unsafe-inline for external scripts;
// the panel does not load anything from outside its own origin anyway.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		// the panel needs none of these browser features
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		// only when the request really came over HTTPS (directly, or through a
		// trusted proxy): over plain HTTP the header is ignored by browsers anyway,
		// and sending it on a LAN-only http:// setup would only mislead
		if overHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		// ! style-src without 'unsafe-inline' stays — that is why the template has not
		// a single style= attribute, and the background is set through CSSOM
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'; "+
				"object-src 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/events" {
			slog.Debug("req", "m", r.Method, "p", r.URL.Path, "ms", time.Since(start).Milliseconds())
		}
	})
}

type serviceView struct {
	Key   string
	Group string
	Name  string
	URL   string
	// Href — the same as URL, but only when it may be put into an href attribute
	// (http/https); empty for tcp:// and for anything suspicious.
	Href     string
	Host     string
	IconName string
	Icon     iconRef
	Size     string // "sm" | "md" | "lg" — never empty

	Description string
	Tags        []string
	// TagsJSON — the same tags as JSON in the data-tags attribute; the edit window
	// reads them from there, so a tag with a comma does not fall apart into two.
	TagsJSON string
}

// SearchText — what the filter in the header searches: name, host, description and tags.
func (w serviceView) SearchText() string {
	return strings.TrimSpace(strings.Join(append([]string{w.Name, w.Host, w.Description}, w.Tags...), " "))
}

type groupView struct {
	Name     string
	Services []serviceView
}

// appearanceView — validated values for the data-* attributes in <html>/<body>.
// The server injects them into the first paint so the panel does not flash the
// default background before JS reads localStorage.
type appearanceView struct {
	Preset string // allowlist config.BackgroundPresets
	Image  string // file name from /bg/; non-empty only when Preset=="photo"
	Blur   int    // 0..30
	Dim    int    // 0..80, opacity of the dimming layer in percent
	Tone   string // "dark" | "light"
	View   string // "grid" | "bento" | "list"
}

// appearanceAPI — the shape of the appearance in JSON (GET /api/appearance, SSE event).
type appearanceAPI struct {
	Preset string `json:"preset"`
	Image  string `json:"image"`
	Blur   int    `json:"blur"`
	Dim    int    `json:"dim"`
	Tone   string `json:"tone"`
	View   string `json:"view"`
}

// appearanceWindow is window.APPEARANCE — the same plus two things the UI has no
// way to get: whether the appearance may be saved to the file and what the upload limit is.
type appearanceWindow struct {
	appearanceAPI
	Writable bool `json:"writable"`
	LimitMB  int  `json:"limitMB"`
}

type pageView struct {
	Title     string
	Groups    []groupView
	State     template.JS
	Version   string
	User      string
	IconCount int

	Lang                   string
	T                      map[string]string
	TJSON                  template.JS
	SecondLang             string
	SecondLangName         string
	SecondLangShort        string
	SearchIconsPlaceholder string

	Banner         bannerAPI
	Appearance     appearanceView
	AppearanceJSON template.JS
	// CanWrite — today every logged-in user may save. The field exists so
	// that introducing roles is a single line, not a template rework.
	CanWrite        bool
	BackgroundLimit int
}

// ── instance appearance ──────────────────────────────────────────────────────

// instanceAppearance returns the appearance from the file, after validation. Load has already
// straightened it, but this code is the last gate before an HTML attribute — a repeat costs
// a microsecond, and protects against every future path that bypasses Load.
func (s *Server) instanceAppearance() appearanceView {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	w := appearanceView{Preset: config.DefaultPreset, Blur: config.DefaultBlur,
		Dim: config.DefaultDim, Tone: config.ToneFor(config.DefaultPreset),
		View: config.DefaultView}
	if cfg == nil {
		return w
	}
	a := cfg.Appearance
	if config.Allowed(a.Background.Preset, config.BackgroundPresets) {
		w.Preset = a.Background.Preset
		w.Tone = config.ToneFor(w.Preset) // the preset carries the tone; an explicit tone below overrides it
	}
	if config.Allowed(a.Background.Tone, config.BackgroundTones) {
		w.Tone = a.Background.Tone
	}
	if config.Allowed(a.View, config.Views) {
		w.View = a.View
	}
	if a.Background.Blur >= 0 && a.Background.Blur <= config.MaxBlur {
		w.Blur = a.Background.Blur
	}
	if a.Background.Dim >= 0 && a.Background.Dim <= config.MaxDim {
		w.Dim = a.Background.Dim
	}
	if w.Preset == config.PresetPhoto && config.BackgroundName.MatchString(a.Background.Image) {
		w.Image = a.Background.Image
	}
	if w.Preset == config.PresetPhoto && w.Image == "" {
		w.Preset = config.DefaultPreset // a photo without a file is not a background
	}
	// ! at the end, because the preset may have changed above. The tone is chosen only
	// with one's own wallpaper — the preset carries the brightness, and thus the only readable tone.
	if w.Preset != config.PresetPhoto {
		w.Tone = config.ToneFor(w.Preset)
	}
	return w
}

func (w appearanceView) toAPI() appearanceAPI {
	return appearanceAPI(w)
}

// gateAppearance — the background for /login and /setup. The wallpaper stays behind the login.
func (s *Server) gateAppearance() appearanceView {
	w := s.instanceAppearance()
	w.Image = ""
	if w.Preset == config.PresetPhoto {
		w.Preset = config.DefaultPreset
		if w.Tone == "light" {
			w.Preset = "dawn"
		}
	}
	return w
}

// second returns the language the button switches to — we have exactly two.
func second(l i18n.Lang) i18n.Lang {
	if l == i18n.PL {
		return i18n.EN
	}
	return i18n.PL
}

// fillLang completes everything the template knows about the language.
func fillLang(l i18n.Lang) (map[string]string, template.JS, string, string, string) {
	t := i18n.Map(l)
	b, err := json.Marshal(t)
	if err != nil {
		b = []byte("{}")
	}
	d := second(l)
	return t, template.JS(b), string(d), d.Name(), d.Short()
}

// Version set at build time (-ldflags).
var Version = "dev"

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		plainError(w, r, http.StatusServiceUnavailable, "error_not_loaded")
		return
	}

	lang := i18n.FromRequest(r)
	data := pageView{Title: cfg.Title, Version: Version, IconCount: s.icons.Count(), Lang: string(lang)}
	data.T, data.TJSON, data.SecondLang, data.SecondLangName, data.SecondLangShort = fillLang(lang)
	data.Appearance = s.instanceAppearance()
	data.Banner = s.instanceBanner(lang)
	data.CanWrite = true
	data.BackgroundLimit = BackgroundLimitMB
	// ! the template gets JSON from the STRUCT after validation, never raw input
	if b, err := json.Marshal(appearanceWindow{appearanceAPI: data.Appearance.toAPI(),
		Writable: data.CanWrite, LimitMB: data.BackgroundLimit}); err == nil {
		data.AppearanceJSON = template.JS(b)
	} else {
		data.AppearanceJSON = template.JS("{}")
	}
	data.SearchIconsPlaceholder = fmt.Sprintf(i18n.T(lang, "search_icons"), s.icons.Count())
	if u := UserFromRequest(r); u != nil {
		data.User = u.Login
	}
	if data.Title == "" {
		data.Title = "Shy Garlic"
	}
	for _, g := range cfg.Groups {
		by := groupView{Name: g.Name}
		for _, svc := range g.Services {
			size := svc.Size
			if !config.Allowed(size, config.Sizes) {
				size = config.DefaultSize
			}
			by.Services = append(by.Services, serviceView{
				Key:      key(g.Name, svc.Name),
				Group:    g.Name,
				Name:     svc.Name,
				URL:      config.RedactURL(svc.URL),
				Href:     hrefOf(config.RedactURL(svc.URL)),
				Host:     hostOf(svc.URL),
				IconName: svc.Icon,
				Icon:     s.resolveIcon(svc.Icon, svc.Name),
				Size:     size,

				Description: svc.Description,
				Tags:        []string(svc.Tags),
				TagsJSON:    tagsJSON(svc.Tags),
			})
		}
		data.Groups = append(data.Groups, by)
	}

	// initial state injected into the HTML — without it the panel would flash "unknown"
	// for the first half second after every refresh
	if b, err := json.Marshal(s.monitor.Snapshot()); err == nil {
		data.State = template.JS(b)
	} else {
		data.State = template.JS("{}")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// the page is dynamic (service state, language, user) — the browser cache
	// could show a version from before the language switch
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("render", "err", err)
	}
}

func tagsJSON(t config.Tags) string {
	b, err := json.Marshal(tagsToJSON(t))
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.monitor.Snapshot())
}

// maxStreamsPerUser — how many /events streams one account may keep open. A
// person has a handful of tabs; more is a script holding connections open.
const maxStreamsPerUser = 8

func streamOwner(r *http.Request) int64 {
	if u := UserFromRequest(r); u != nil {
		return u.ID
	}
	return 0
}

func (s *Server) acquireStream(r *http.Request) bool {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	id := streamOwner(r)
	if s.sseStreams[id] >= maxStreamsPerUser {
		return false
	}
	s.sseStreams[id]++
	return true
}

func (s *Server) releaseStream(r *http.Request) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	id := streamOwner(r)
	if s.sseStreams[id] <= 1 {
		delete(s.sseStreams, id)
		return
	}
	s.sseStreams[id]--
}

// streamWriteTimeout — how long one write to an event stream may block. A var so
// a test can shorten it.
var streamWriteTimeout = 30 * time.Second

// deadlineWriter gives every write its own deadline; a failed write makes the
// handler return, which releases the stream slot.
type deadlineWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	return d.w.Write(p)
}

// flush pushes buffered data to the connection under a fresh deadline: the actual
// network write happens here, not in Write.
func (d *deadlineWriter) flush() error {
	_ = d.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	return d.rc.Flush()
}

// handleEvents is the SSE stream — the panel gets changes immediately, without polling.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		plainError(w, r, http.StatusInternalServerError, "error_internal")
		return
	}
	// each stream holds a goroutine, a monitor subscription and a connection
	if !s.acquireStream(r) {
		plainError(w, r, http.StatusTooManyRequests, "error_too_many_streams")
		return
	}
	defer s.releaseStream(r)
	// The server's WriteTimeout would cut a stream that is meant to live for hours,
	// so the stream manages its own: every write gets a fresh deadline (see
	// deadlineWriter). Without one, a client that stops reading (zero TCP window)
	// would hold its slot, a goroutine and a monitor subscription forever.
	out := &deadlineWriter{w: w, rc: http.NewResponseController(w)}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	changes, unpin := s.monitor.Subscribe()
	defer unpin()
	appearances, unpinAppearance := s.subscribeAppearance()
	defer unpinAppearance()

	// the default stream (without `event:`) is still the health snapshot — it must not
	// be touched, because draw() on the other side does not check the event type
	send := func() bool {
		b, err := json.Marshal(s.monitor.Snapshot())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(out, "data: %s\n\n", b); err != nil {
			return false
		}
		if out.flush() != nil {
			return false
		}
		return true
	}
	sendAppearance := func() bool {
		b, err := json.Marshal(s.instanceAppearance().toAPI())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(out, "event: appearance\ndata: %s\n\n", b); err != nil {
			return false
		}
		if out.flush() != nil {
			return false
		}
		return true
	}
	// layout and title ride the same wake-up as the appearance: a change from another tab
	// or from the file must reach all open tabs
	sendLayout := func() bool {
		b, err := json.Marshal(s.instanceLayout())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(out, "event: layout\ndata: %s\n\n", b); err != nil {
			return false
		}
		if out.flush() != nil {
			return false
		}
		return true
	}
	// the file banner rides the same wake-up — an error appears and disappears on
	// open tabs without a refresh
	lang := i18n.FromRequest(r)
	sendBanner := func() bool {
		b, err := json.Marshal(s.instanceBanner(lang))
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(out, "event: config\ndata: %s\n\n", b); err != nil {
			return false
		}
		if out.flush() != nil {
			return false
		}
		return true
	}
	if !send() {
		return
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !send() {
				return
			}
		case <-appearances:
			if !sendAppearance() || !sendLayout() || !sendBanner() {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(out, ": keepalive\n\n"); err != nil {
				return
			}
			if out.flush() != nil {
				return
			}
		}
	}
}

// layoutAPI — title and order of groups/services (names only). The card compares it with
// its own DOM and reloads only when they have drifted apart.
type layoutAPI struct {
	Title  string        `json:"title"`
	Groups []layoutGroup `json:"groups"`
}

type layoutGroup struct {
	Name     string   `json:"name"`
	Services []string `json:"services"`
}

func (s *Server) instanceLayout() layoutAPI {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	u := layoutAPI{Title: "Shy Garlic", Groups: []layoutGroup{}}
	if cfg == nil {
		return u
	}
	if cfg.Title != "" {
		u.Title = cfg.Title
	}
	for _, g := range cfg.Groups {
		groupLayout := layoutGroup{Name: g.Name, Services: []string{}}
		for _, svc := range g.Services {
			groupLayout.Services = append(groupLayout.Services, svc.Name)
		}
		u.Groups = append(u.Groups, groupLayout)
	}
	return u
}

// hrefOf returns the address for an href attribute or "" — see config.ClickableAddress.
func hrefOf(raw string) string {
	if config.ClickableAddress(raw) {
		return raw
	}
	return ""
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return config.RedactURL(raw)
	}
	return u.Host
}

func firstLetter(s string) string {
	for _, r := range strings.TrimSpace(s) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// ── HTTP server limits ───────────────────────────────────────────────────────

const (
	readTimeout    = 30 * time.Second
	writeTimeout   = 60 * time.Second
	idleTimeout    = 120 * time.Second
	maxHeaderBytes = 64 << 10
)

// NewHTTPServer builds the http.Server with the limits the panel runs under.
//
// ReadTimeout and WriteTimeout bound a slow client holding a goroutine and a
// connection. The two handlers that legitimately take longer lift their own
// deadline: /events (SSE, open for hours) and the wallpaper upload.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return newHTTPServer(addr, h, writeTimeout)
}

func newHTTPServer(addr string, h http.Handler, write time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      write,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}
