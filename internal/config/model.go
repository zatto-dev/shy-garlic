// Package config reads and writes the Shy Garlic configuration.
//
// Two independent views of the same file:
//   - Load() → Config: semantic view, read-only (server, heartbeat, render).
//   - Parse() → Document: text view, for editing. Saving is a byte splice
//     into the original, so bytes nobody changed stay untouched.
package config

import (
	"regexp"
	"slices"
	"time"
)

// Default check parameters, used when a service gives none.
const (
	DefaultInterval = 30 * time.Second
	DefaultTimeout  = 5 * time.Second

	// Accepted range for check.interval and check.timeout. Below a second the
	// ticker would flood the checked service; above a day the value is
	// certainly a typo. Outside it Load falls back to the default and warns.
	MinCheckDuration = time.Second
	MaxCheckDuration = 24 * time.Hour
)

// DefaultExpect — codes treated as "the service is alive".
//
// ! 401 and 403 are here on purpose: a service that answers "you must log in"
// is alive. Competitor research (2026-08-14) showed that Homepage, Homarr and
// Glance confuse these codes with an outage or the other way round — they light
// up green whatever comes back.
var DefaultExpect = []int{200, 301, 302, 401, 403}

// ── appearance ───────────────────────────────────────────────────────────────
//
// The `appearance` block is the default look of the WHOLE instance. The file
// stays the source of truth; the browser may override it locally (localStorage),
// but what stands here is seen by everyone, everywhere. Values are closed by
// allowlists, because they land in HTML attributes and CSS rules.

var (
	BackgroundPresets = []string{"aurora", "nebula", "graphite", "dawn", "fog", "photo"}
	BackgroundTones   = []string{"dark", "light"}
	Views             = []string{"grid", "bento", "list"}
	Sizes             = []string{"sm", "md", "lg"}
)

const (
	MaxBlur     = 30
	MaxDim      = 80
	PresetPhoto = "photo"

	DefaultPreset = "aurora"
	DefaultBlur   = 10
	DefaultDim    = 50
	DefaultTone   = "dark"
	DefaultView   = "grid"
	DefaultSize   = "md"

	// DefaultThreshold — one failed check is not an outage yet.
	DefaultThreshold = 2
)

// PresetTone — every preset has a predetermined background brightness, so it
// carries the only text tone it is readable in. `photo` has none: there the
// brightness is only known to the browser, after measuring the luminance of the uploaded image.
var PresetTone = map[string]string{
	"aurora":   "dark",
	"nebula":   "dark",
	"graphite": "dark",
	"dawn":     "light",
	"fog":      "light",
}

// ToneFor returns the tone that matches the preset.
//
// ! Without it, choosing a light preset kept the tone from the previous, dark
// one — light text on a cream background. The tone cannot have a single default,
// because the correct one depends on what is underneath.
func ToneFor(preset string) string {
	if t, ok := PresetTone[preset]; ok {
		return t
	}
	return DefaultTone
}

// BackgroundName — the single source of truth about the wallpaper file name; used by both config
// and the server. The alphabet contains not a single character meaningful in a path, in CSS
// or in an HTTP header, so a name that passed it is safe
// wherever it ends up.
var BackgroundName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Allowed says whether the value is on the list of permitted ones.
func Allowed(v string, list []string) bool { return slices.Contains(list, v) }

type Config struct {
	Title      string     `yaml:"title"`
	Appearance Appearance `yaml:"appearance"`
	Notify     Notify     `yaml:"notify"`
	Discovery  Discovery  `yaml:"discovery"`
	Groups     []Group    `yaml:"groups"`

	// LoadWarnings — values Load replaced with defaults (e.g. an interval of
	// -5s). Filled by Load, never read from or written to the file.
	LoadWarnings []Warning `yaml:"-"`
}

// Notify — notification configuration that is NOT a secret.
//
// ! The webhook address is not here and never will be. This file is shown in the UI and
// committed, and the webhook URL is a credential — whoever has it can post to
// the channel. The address lives in SQLite, next to passwords and sessions.
type Notify struct {
	Threshold int `yaml:"threshold"`
}

// Discovery — what the panel should suggest when adding a service.
//
// ! A pointer, because we must tell "nothing written" (then we suggest if the
// socket is available) from a deliberate `docker: false`.
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
	// Dim — opacity of the lower end of the dimming layer, in percent.
	// The upper end is always Dim × 0.56 — a proportion from the original .28/.5,
	// so the dimming grows towards the bottom, where the tiles stand.
	//
	// ! Text colour alone is not enough: on a photo with high internal
	// contrast some tiles lie on a bright and some on a dark background, so
	// readability is only saved by stronger dimming.
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
	// Size — tile size in the bento view; empty means DefaultSize.
	Size string `yaml:"size"`
	// Description — one line under the tile name (optional). Inherited
	// through merge-key like any other field.
	Description string `yaml:"description"`
	// Tags — labels for search and filtering; small chips in the list view.
	Tags  Tags  `yaml:"tags"`
	Check Check `yaml:"check"`
}

// Check — the heartbeat semantics of a single service.
type Check struct {
	Interval Duration `yaml:"interval"`
	Timeout  Duration `yaml:"timeout"`
	Expect   []int    `yaml:"expect"`
	// Insecure — a homelab often runs on a self-signed certificate
	// (Proxmox, iDRAC, NAS boxes). Without it the panel would show a false "down".
	Insecure bool `yaml:"insecure"`

	// Content check (SHYG-48), http(s) only.
	//
	// Path replaces the path (and query) from `url` ONLY for the duration of the check —
	// scheme, host and port stay. The equivalent of "use /healthz instead of the
	// home page". Must start with `/`.
	Path string `yaml:"path"`
	// Contains — text that must be in the first 1 MiB of the response; Not… —
	// text that must not be. A mismatch is the `degraded` state (the server is alive, but
	// answers with something else), not `down`.
	Contains    string `yaml:"contains"`
	NotContains string `yaml:"not_contains"`
}
