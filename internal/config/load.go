package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// Load reads the configuration semantically: expands merge keys (`<<: *defaults`),
// fills in missing check parameters and validates the minimum.
//
// Load is NOT suitable for editing the file — that is what Parse/Document are for.
func Load(src []byte) (*Config, error) {
	var cfg Config
	if err := safeUnmarshal(src, &cfg); err != nil {
		b := parseRawError(err)
		if b.Line == 0 {
			b.Line = locateLine(src, b.Message)
		}
		return nil, &LoadError{FileError: b, err: fmt.Errorf("parse config: %w", err)}
	}
	blurGiven, dimGiven := givenSliders(src)
	applyAppearance(&cfg.Appearance, blurGiven, dimGiven)
	if cfg.Notify.Threshold < 1 {
		cfg.Notify.Threshold = DefaultThreshold
	}
	for gi := range cfg.Groups {
		for si := range cfg.Groups[gi].Services {
			applyDefaults(&cfg.Groups[gi].Services[si].Check)
			applySize(&cfg.Groups[gi].Services[si])
			applyDescription(&cfg.Groups[gi].Services[si])
		}
	}
	return &cfg, nil
}

func applyDefaults(c *Check) {
	if c.Interval == 0 {
		c.Interval = Duration(DefaultInterval)
	}
	if c.Timeout == 0 {
		c.Timeout = Duration(DefaultTimeout)
	}
	if len(c.Expect) == 0 {
		c.Expect = append([]int(nil), DefaultExpect...)
	}
	// `path: health` is an obvious typo for `path: /health` — we fix it
	// instead of ending up "down" with an incomprehensible reason
	if c.Path != "" && !strings.HasPrefix(c.Path, "/") && !strings.Contains(c.Path, "://") {
		slog.Warn("check.path without leading /, prepending it", "path", c.Path)
		c.Path = "/" + c.Path
	}
}

// applyDescription cleans the description and tags written by hand in the file: the description is one line
// (a block scalar `|` is joined with spaces), tags without empty ones and duplicates.
// It returns no error — this is meant to be readable, not forbidden; stricter validation is
// on the API side (NormalizeDescription, NormalizeTags).
func applyDescription(s *Service) {
	s.Description = strings.Join(strings.Fields(s.Description), " ")
	s.Tags = cleanTags(s.Tags)
}

func applySize(s *Service) {
	if !Allowed(s.Size, Sizes) {
		s.Size = DefaultSize
	}
}

// applyAppearance fills in defaults and CLAMPS values outside the allowlist.
//
// ! Deliberately returns no error: nonsense written by hand into the file should yield the default
// appearance and a log entry, not a dead panel. The same value sent from the UI
// ends with a 400 — there the user is meant to learn they made a mistake.
func applyAppearance(a *Appearance, blurGiven, dimGiven bool) {
	b := &a.Background

	if !Allowed(b.Preset, BackgroundPresets) {
		if b.Preset != "" {
			slog.Warn("unknown background preset in config", "preset", b.Preset, "using", DefaultPreset)
		}
		b.Preset = DefaultPreset
	}
	if !blurGiven {
		b.Blur = DefaultBlur
	} else if b.Blur < 0 || b.Blur > MaxBlur {
		slog.Warn("background blur out of range", "blur", b.Blur, "using", DefaultBlur)
		b.Blur = DefaultBlur
	}
	if !dimGiven {
		b.Dim = DefaultDim
	} else if b.Dim < 0 || b.Dim > MaxDim {
		slog.Warn("background dim out of range", "dim", b.Dim, "using", DefaultDim)
		b.Dim = DefaultDim
	}
	if b.Preset != PresetPhoto {
		b.Image = ""
	} else if !BackgroundName.MatchString(b.Image) {
		if b.Image != "" {
			slog.Warn("bad wallpaper file name in config", "image", b.Image)
		}
		// preset `photo` without a usable file is not a background — fall back to the preset
		b.Image = ""
		b.Preset = DefaultPreset
	}
	// ! The tone only HERE, because the preset may have changed above — `photo` without
	// a usable file goes back to the preset and must take its tone.
	//
	// The tone is a choice ONLY with a custom wallpaper: only there the background
	// brightness cannot be known in advance. Every preset has it fixed, so it imposes the tone —
	// otherwise white text could be saved on a cream background.
	if b.Preset != PresetPhoto {
		if b.Tone != "" && b.Tone != ToneFor(b.Preset) {
			slog.Warn("background tone does not match the preset, using the preset's tone",
				"preset", b.Preset, "tone", b.Tone, "using", ToneFor(b.Preset))
		}
		b.Tone = ToneFor(b.Preset)
	} else if !Allowed(b.Tone, BackgroundTones) {
		if b.Tone != "" {
			slog.Warn("unknown background tone in config", "tone", b.Tone, "using", DefaultTone)
		}
		b.Tone = DefaultTone
	}
	if !Allowed(a.View, Views) {
		if a.View != "" {
			slog.Warn("unknown view in config", "view", a.View, "using", DefaultView)
		}
		a.View = DefaultView
	}
}

// givenSliders says which of the background sliders are present in the file at all. Without it
// "not given" (→ default value) cannot be told from a deliberate zero, and zero
// is a valid choice for both: a sharp wallpaper without blur and a wallpaper without
// dimming.
//
// ! It goes through the parser, not through text search. A byte pattern would hit
// `# dim: 0` in a comment and every service that has such a word in its name.
func givenSliders(src []byte) (blur, dim bool) {
	var helper struct {
		Appearance struct {
			Background struct {
				Blur *int `yaml:"blur"`
				Dim  *int `yaml:"dim"`
			} `yaml:"background"`
		} `yaml:"appearance"`
	}
	if err := safeUnmarshal(src, &helper); err != nil {
		return false, false
	}
	b := helper.Appearance.Background
	return b.Blur != nil, b.Dim != nil
}
