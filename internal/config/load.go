package config

import (
	"fmt"
	"log/slog"
	"strings"

)

// Load czyta konfigurację semantycznie: rozwija merge-keye (`<<: *defaults`),
// uzupełnia brakujące parametry sprawdzania i waliduje minimum.
//
// Load NIE nadaje się do edycji pliku — do tego jest Parse/Document.
func Load(src []byte) (*Config, error) {
	var cfg Config
	if err := safeUnmarshal(src, &cfg); err != nil {
		b := rozbierzSurowy(err)
		if b.Linia == 0 {
			b.Linia = lokalizuj(src, b.Komunikat)
		}
		return nil, &BłądWczytania{BłądPliku: b, err: fmt.Errorf("parse config: %w", err)}
	}
	blurPodany, dimPodany := podaneSuwaki(src)
	applyWygląd(&cfg.Appearance, blurPodany, dimPodany)
	if cfg.Notify.Threshold < 1 {
		cfg.Notify.Threshold = DomyślnyPróg
	}
	for gi := range cfg.Groups {
		for si := range cfg.Groups[gi].Services {
			applyDefaults(&cfg.Groups[gi].Services[si].Check)
			applyRozmiar(&cfg.Groups[gi].Services[si])
			applyOpis(&cfg.Groups[gi].Services[si])
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
	// `path: health` to oczywista literówka za `path: /health` — poprawiamy
	// zamiast kończyć „down" z niezrozumiałym powodem
	if c.Path != "" && !strings.HasPrefix(c.Path, "/") && !strings.Contains(c.Path, "://") {
		slog.Warn("check.path bez wiodącego /, dopisuję", "path", c.Path)
		c.Path = "/" + c.Path
	}
}

// applyOpis czyści opis i tagi ręcznie wpisane w plik: opis jest jedną linią
// (blokowy skalar `|` zostaje sklejony spacjami), tagi bez pustych i duplikatów.
// Nie zwraca błędu — to ma być czytelne, nie zakazane; surowsza walidacja jest
// po stronie API (NormalizujOpis, NormalizujTagi).
func applyOpis(s *Service) {
	s.Description = strings.Join(strings.Fields(s.Description), " ")
	s.Tags = czyśćTagi(s.Tags)
}

func applyRozmiar(s *Service) {
	if !Dozwolone(s.Size, Rozmiary) {
		s.Size = DomyślnyRozmiar
	}
}

// applyWygląd uzupełnia domyślne i PROSTUJE wartości spoza allowlisty.
//
// ! Świadomie nie zwraca błędu: bzdura wpisana ręcznie w plik ma dać domyślny
// wygląd i wpis w logu, a nie martwy panel. Ta sama wartość przysłana z UI
// kończy się 400 — tam użytkownik ma się dowiedzieć, że się pomylił.
func applyWygląd(a *Appearance, blurPodany, dimPodany bool) {
	b := &a.Background

	if !Dozwolone(b.Preset, PresetyTła) {
		if b.Preset != "" {
			slog.Warn("nieznany preset tła w configu", "preset", b.Preset, "biorę", DomyślnyPreset)
		}
		b.Preset = DomyślnyPreset
	}
	if !blurPodany {
		b.Blur = DomyślnyBlur
	} else if b.Blur < 0 || b.Blur > MaxBlur {
		slog.Warn("rozmycie tła poza zakresem", "blur", b.Blur, "biorę", DomyślnyBlur)
		b.Blur = DomyślnyBlur
	}
	if !dimPodany {
		b.Dim = DomyślnyDim
	} else if b.Dim < 0 || b.Dim > MaxDim {
		slog.Warn("przyciemnienie tła poza zakresem", "dim", b.Dim, "biorę", DomyślnyDim)
		b.Dim = DomyślnyDim
	}
	if b.Preset != PresetPhoto {
		b.Image = ""
	} else if !NazwaTła.MatchString(b.Image) {
		if b.Image != "" {
			slog.Warn("zła nazwa pliku tapety w configu", "image", b.Image)
		}
		// preset `photo` bez użytecznego pliku to nie jest tło — wracamy do presetu
		b.Image = ""
		b.Preset = DomyślnyPreset
	}
	// ! Ton dopiero TU, bo preset mógł się wyżej zmienić — `photo` bez
	// użytecznego pliku wraca do presetu i musi wziąć jego ton.
	//
	// Ton jest wyborem WYŁĄCZNIE przy własnej tapecie: tylko tam jasności tła
	// nie da się poznać z góry. Każdy preset ma ją ustaloną, więc narzuca ton —
	// inaczej dałoby się zapisać biały tekst na kremowym tle.
	if b.Preset != PresetPhoto {
		if b.Tone != "" && b.Tone != TonDla(b.Preset) {
			slog.Warn("ton tła nie pasuje do presetu, biorę ton presetu",
				"preset", b.Preset, "tone", b.Tone, "biorę", TonDla(b.Preset))
		}
		b.Tone = TonDla(b.Preset)
	} else if !Dozwolone(b.Tone, TonyTła) {
		if b.Tone != "" {
			slog.Warn("nieznany ton tła w configu", "tone", b.Tone, "biorę", DomyślnyTon)
		}
		b.Tone = DomyślnyTon
	}
	if !Dozwolone(a.View, Widoki) {
		if a.View != "" {
			slog.Warn("nieznany widok w configu", "view", a.View, "biorę", DomyślnyWidok)
		}
		a.View = DomyślnyWidok
	}
}

// podaneSuwaki mówi, które z suwaków tła w ogóle stoją w pliku. Bez tego nie da
// się odróżnić „nie podano" (→ wartość domyślna) od świadomego zera, a zero
// jest przy obu poprawnym wyborem: ostra tapeta bez rozmycia i tapeta bez
// przyciemnienia.
//
// ! Idzie przez parser, nie przez szukanie tekstu. Wzorzec po bajtach trafiłby
// w `# dim: 0` w komentarzu i w każdą usługę, która ma takie słowo w nazwie.
func podaneSuwaki(src []byte) (blur, dim bool) {
	var pomocniczy struct {
		Appearance struct {
			Background struct {
				Blur *int `yaml:"blur"`
				Dim  *int `yaml:"dim"`
			} `yaml:"background"`
		} `yaml:"appearance"`
	}
	if err := safeUnmarshal(src, &pomocniczy); err != nil {
		return false, false
	}
	b := pomocniczy.Appearance.Background
	return b.Blur != nil, b.Dim != nil
}
