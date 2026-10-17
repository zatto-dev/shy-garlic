package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Duration exists because YAML has no duration type, and a bare time.Duration
// deserializes from a number of nanoseconds — nobody will write `interval: 30000000000`.
// We accept Go notation ("30s", "2m") and bare numbers treated as seconds.
//
// ! Out-of-range input (NaN, ±Inf, anything that overflows int64 nanoseconds)
// does NOT fail the whole file: it saturates to MaxInt64 / MinInt64 (NaN →
// MinInt64) and Load then replaces it with the default and warns, like every
// other nonsense written by hand. Converting such a float to int64 directly is
// implementation-defined in Go, which once let a hand-written value become a
// negative interval and panic time.NewTicker on every start.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string          { return time.Duration(d).String() }

// goDuration matches the syntax of time.ParseDuration, so a string that matches
// but is still refused is known to be a range problem, not a typo.
var goDuration = regexp.MustCompile(`^[+-]?(?:(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:ns|us|µs|μs|ms|s|m|h))+$`)

// secondsToDuration converts seconds to a Duration without undefined float→int
// conversions: NaN and everything past the int64 range saturate.
func secondsToDuration(secs float64) Duration {
	ns := secs * float64(time.Second)
	switch {
	case math.IsNaN(ns):
		return Duration(math.MinInt64)
	case ns >= math.MaxInt64:
		return Duration(math.MaxInt64)
	case ns <= math.MinInt64:
		return Duration(math.MinInt64)
	}
	return Duration(time.Duration(ns))
}

func saturate(negative bool) Duration {
	if negative {
		return Duration(math.MinInt64)
	}
	return Duration(math.MaxInt64)
}

// UnmarshalYAML takes the value through the decoder, not raw bytes: goccy gives
// a scalar in bytes TOGETHER with the end-of-line comment, so `interval: 1m  # x`
// failed ParseDuration and ended up as 1s from the "bare seconds" branch.
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		var secs float64
		if err := unmarshal(&secs); err != nil {
			return err
		}
		*d = secondsToDuration(secs)
		return nil
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		*d = 0
		return nil
	}
	if v, err := time.ParseDuration(s); err == nil {
		*d = Duration(v)
		return nil
	}
	if goDuration.MatchString(s) { // well-formed, so it overflowed
		*d = saturate(strings.HasPrefix(s, "-"))
		return nil
	}
	// bare seconds: `interval: 30`, plus YAML's .nan / .inf spellings
	switch strings.ToLower(s) {
	case ".nan":
		*d = Duration(math.MinInt64)
		return nil
	case ".inf", "+.inf":
		*d = Duration(math.MaxInt64)
		return nil
	case "-.inf":
		*d = Duration(math.MinInt64)
		return nil
	}
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = secondsToDuration(secs)
	return nil
}

func (d Duration) MarshalYAML() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}
