package config

import (
	"fmt"
	"time"
)

// Duration istnieje, bo YAML nie ma typu czasu, a gołe time.Duration
// deserializuje się z liczby nanosekund — nikt nie napisze `interval: 30000000000`.
// Przyjmujemy zapis Go ("30s", "2m") oraz gołe liczby traktowane jako sekundy.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string          { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(b []byte) error {
	s := string(b)
	// goccy podaje surowy skalar; zdejmij cudzysłowy jeśli są
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	if s == "" || s == "null" {
		*d = 0
		return nil
	}
	if v, err := time.ParseDuration(s); err == nil {
		*d = Duration(v)
		return nil
	}
	// gołe sekundy: `interval: 30`
	var secs float64
	if _, err := fmt.Sscanf(s, "%g", &secs); err == nil {
		*d = Duration(time.Duration(secs * float64(time.Second)))
		return nil
	}
	return fmt.Errorf("invalid duration %q", s)
}

func (d Duration) MarshalYAML() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}
