package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration istnieje, bo YAML nie ma typu czasu, a gołe time.Duration
// deserializuje się z liczby nanosekund — nikt nie napisze `interval: 30000000000`.
// Przyjmujemy zapis Go ("30s", "2m") oraz gołe liczby traktowane jako sekundy.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string          { return time.Duration(d).String() }

// UnmarshalYAML bierze wartość przez dekoder, a nie surowe bajty: goccy podaje
// w bajtach skalar RAZEM z komentarzem z końca linii, więc `interval: 1m  # x`
// nie przechodził ParseDuration i kończył jako 1s z gałęzi „gołe sekundy".
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
	// gołe sekundy: `interval: 30`
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
