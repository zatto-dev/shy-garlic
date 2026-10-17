package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration exists because YAML has no duration type, and a bare time.Duration
// deserializes from a number of nanoseconds — nobody will write `interval: 30000000000`.
// We accept Go notation ("30s", "2m") and bare numbers treated as seconds.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string          { return time.Duration(d).String() }

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
		*d = Duration(time.Duration(secs * float64(time.Second)))
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
	// bare seconds: `interval: 30`
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(time.Duration(secs * float64(time.Second)))
	return nil
}

func (d Duration) MarshalYAML() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}
