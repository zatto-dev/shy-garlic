package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/dockerdisc"
	"github.com/zatto-dev/shy-garlic/internal/gitrepo"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// API edytora. Każda zmiana idzie przez silnik z internal/config, więc plik
// YAML po zapisie różni się dokładnie o to, co użytkownik zmienił — komentarze,
// anchory i formatowanie zostają na miejscu.

type odpBłąd struct {
	Błąd string `json:"error"`
}

func jsonOdp(w http.ResponseWriter, kod int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(kod)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonBłąd(w http.ResponseWriter, kod int, msg string) {
	jsonOdp(w, kod, odpBłąd{Błąd: msg})
}

// Limity ciała żądań JSON. Bez nich jeden POST na kilka GB kładł proces
// (json.Decoder czyta do końca, a zalogowany to nie zawsze zaufany).
const (
	limitJSON    = 1 << 20 // 1 MiB — ciało każdego zwykłego żądania to kilkaset bajtów
	limitConfigu = 2 << 20 // 2 MiB — PUT /api/config niesie cały YAML w JSON-ie
)

// dekodujJSON czyta ciało żądania do v, nie więcej niż limitJSON bajtów.
func dekodujJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return dekodujJSONLimit(w, r, v, limitJSON)
}

func dekodujJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return json.NewDecoder(r.Body).Decode(v)
}

// zEdycją otwiera plik, woła fn, pokazuje albo zapisuje zmianę.
//
// Z parametrem ?dry=1 nic nie zapisuje — zwraca diff, żeby użytkownik zobaczył
// dokładnie, co się zmieni, ZANIM się zgodzi. To nie jest ozdoba: research
// pokazał, że przy edytorach cudzych plików problemem nie jest brak funkcji,
// tylko brak zaufania.
//
// Po udanym zapisie: wpis do historii (z możliwością cofnięcia) i commit w
// gicie, jeśli plik leży w repozytorium. Konflikt z ręczną edycją pliku kończy
// się 409, nie cichym nadpisaniem.
func (s *Server) zEdycją(w http.ResponseWriter, r *http.Request, opis string, fn func(*config.Document) error) bool {
	return s.zEdycjąZKluczami(w, r, opis, nil, fn)
}

// zEdycjąZKluczami jak zEdycją, ale po udanym zapisie przenosi historię i stan
// usług, których klucz (grupa/nazwa) się zmienił. migr dostaje config sprzed
// zmiany i zwraca pary stary → nowy; nil = nic nie przenosimy.
func (s *Server) zEdycjąZKluczami(w http.ResponseWriter, r *http.Request, opis string,
	migr func(stara *config.Config) map[string]string, fn func(*config.Document) error) bool {
	s.edycja.Lock()
	defer s.edycja.Unlock()

	przed, err := configBytes(s.path)
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, "nie mogę przeczytać configu: "+err.Error())
		return false
	}
	d, err := config.Open(s.path)
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, "nie mogę otworzyć configu: "+err.Error())
		return false
	}
	if err := fn(d); err != nil {
		kod := http.StatusBadRequest
		switch {
		case errors.Is(err, config.ErrServiceNotFound), errors.Is(err, config.ErrGroupNotFound):
			kod = http.StatusNotFound
		}
		jsonBłąd(w, kod, err.Error())
		return false
	}
	if _, err := config.Load(d.Bytes()); err != nil {
		jsonBłąd(w, http.StatusInternalServerError, "zmiana dałaby niepoprawny plik: "+err.Error())
		return false
	}

	diff := config.Diff(przed, d.Bytes(), 3)
	dodane, usunięte := config.PodsumujDiff(diff)

	if r.URL.Query().Get("dry") == "1" {
		jsonOdp(w, http.StatusOK, map[string]any{
			"dry": true, "diff": diff, "added": dodane, "removed": usunięte, "summary": opis,
		})
		return false
	}

	if err := d.Save(); err != nil {
		if errors.Is(err, config.ErrChangedOnDisk) {
			jsonBłąd(w, http.StatusConflict, i18n.T(i18n.Wybierz(r), "konflikt_pliku"))
			return false
		}
		jsonBłąd(w, http.StatusInternalServerError, "zapis nieudany: "+err.Error())
		return false
	}

	autor := "?"
	if u := UżytkownikZŻądania(r); u != nil {
		autor = u.Login
	}
	if err := s.st.ZapiszWersję(r.Context(), autor, opis, string(d.Bytes()), diff); err != nil {
		slog.Error("historia configu", "err", err)
	}
	s.commitDoGita(r.Context(), opis, autor)

	// ! po Save (YAML jest już na dysku), przed reload (który zapomina nieznane klucze)
	if migr != nil {
		var stara *config.Config
		if c, err := config.Load(przed); err == nil {
			stara = c
		}
		s.migrujKlucze(r.Context(), migr(stara))
	}

	if err := s.reload(s.ctx); err != nil {
		slog.Error("przeładowanie po zapisie", "err", err)
	}
	return true
}

// commitDoGita zostawia ślad w repozytorium, jeśli config w nim leży.
// Brak repo albo brak gita to nie błąd — po prostu nic się nie dzieje.
func (s *Server) commitDoGita(ctx context.Context, opis, autor string) {
	if !gitrepo.WRepo(ctx, s.path) {
		return
	}
	wiadomość := "shyg: " + opis
	if err := gitrepo.Commit(ctx, s.path, wiadomość, autor); err != nil {
		slog.Warn("commit configu nieudany", "err", err)
		return
	}
	slog.Info("config zacommitowany", "opis", opis, "autor", autor)
}

// ── usługi ───────────────────────────────────────────────────────────────────

type żądanieUsługi struct {
	Grupa      string `json:"group"`
	NowaGrupa  string `json:"newGroup,omitempty"`
	Nazwa    string `json:"name"`
	NowaNazwa string `json:"newName,omitempty"`
	URL      string `json:"url"`
	Ikona    string `json:"icon"`
	Interval string `json:"interval,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
	Expect   []int  `json:"expect,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
	Rozmiar  string `json:"size,omitempty"`
}

// rozmiarZŻądania przepuszcza wyłącznie allowlistę; puste zostaje puste, żeby
// nie dopisywać do pliku klucza, o który nikt nie prosił.
func rozmiarZŻądania(v string) string {
	if config.Dozwolone(v, config.Rozmiary) {
		return v
	}
	return ""
}

// poprawnaNazwaWpisu pilnuje parametrów, po których szukamy w YAML-u.
// ! handleServiceDelete i handleGroupDelete brały ?name= zupełnie bez kontroli.
func poprawnaNazwaWpisu(v string) bool {
	if v == "" || len(v) > 200 || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Server) handleServiceCreate(w http.ResponseWriter, r *http.Request) {
	var ż żądanieUsługi
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if ż.Nazwa == "" || ż.Grupa == "" {
		jsonBłąd(w, http.StatusBadRequest, "nazwa i grupa są wymagane")
		return
	}
	if !config.PoprawnyAdresUsługi(ż.URL) {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(i18n.Wybierz(r), "blad_adres_uslugi"))
		return
	}
	svc := config.Service{Name: ż.Nazwa, URL: ż.URL, Icon: ż.Ikona, Size: rozmiarZŻądania(ż.Rozmiar)}
	svc.Check = przeliczCheck(ż)

	opis := fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_dodano_usluge"), ż.Nazwa, ż.Grupa)
	if s.zEdycją(w, r, opis, func(d *config.Document) error { return d.AddService(ż.Grupa, svc) }) {
		jsonOdp(w, http.StatusCreated, map[string]any{"ok": true})
	}
}

func przeliczCheck(ż żądanieUsługi) config.Check {
	var c config.Check
	if ż.Interval != "" {
		if v, err := time.ParseDuration(ż.Interval); err == nil {
			c.Interval = config.Duration(v)
		}
	}
	if ż.Timeout != "" {
		if v, err := time.ParseDuration(ż.Timeout); err == nil {
			c.Timeout = config.Duration(v)
		}
	}
	c.Expect = ż.Expect
	c.Insecure = ż.Insecure
	return c
}

func (s *Server) handleServiceUpdate(w http.ResponseWriter, r *http.Request) {
	var ż żądanieUsługi
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if ż.Nazwa == "" || ż.Grupa == "" {
		jsonBłąd(w, http.StatusBadRequest, "nazwa i grupa są wymagane")
		return
	}
	lang := i18n.Wybierz(r)
	if !config.PoprawnyAdresUsługi(ż.URL) {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_adres_uslugi"))
		return
	}
	opis := fmt.Sprintf(i18n.T(lang, "opis_zmieniono_usluge"), ż.Nazwa)
	if ż.NowaGrupa != "" && ż.NowaGrupa != ż.Grupa {
		opis = fmt.Sprintf(i18n.T(lang, "opis_przeniesiono"), ż.Nazwa, ż.Grupa, ż.NowaGrupa)
	}
	// ! pary liczone TERAZ — fn niżej nadpisuje ż.Grupa nową grupą
	nowaGrupa, nowaNazwa := ż.Grupa, ż.Nazwa
	if ż.NowaGrupa != "" {
		nowaGrupa = ż.NowaGrupa
	}
	if ż.NowaNazwa != "" {
		nowaNazwa = ż.NowaNazwa
	}
	pary := map[string]string{key(ż.Grupa, ż.Nazwa): key(nowaGrupa, nowaNazwa)}
	migr := func(*config.Config) map[string]string { return pary }
	ok := s.zEdycjąZKluczami(w, r, opis, migr, func(d *config.Document) error {
		// przeniesienie idzie pierwsze — dalsze zmiany szukają usługi w nowej grupie
		grupa := ż.Grupa
		if ż.NowaGrupa != "" && ż.NowaGrupa != ż.Grupa {
			if err := d.MoveService(ż.Grupa, ż.Nazwa, ż.NowaGrupa); err != nil {
				return err
			}
			grupa = ż.NowaGrupa
		}
		ż.Grupa = grupa
		if ż.URL != "" {
			if err := d.SetServiceField(ż.Grupa, ż.Nazwa, "url", ż.URL); err != nil {
				return err
			}
		}
		if ż.Ikona != "" {
			if err := d.SetServiceField(ż.Grupa, ż.Nazwa, "icon", ż.Ikona); err != nil &&
				!errors.Is(err, config.ErrPathNotFound) {
				return err
			}
		}
		if rozmiar := rozmiarZŻądania(ż.Rozmiar); rozmiar != "" {
			if err := d.SetServiceField(ż.Grupa, ż.Nazwa, "size", rozmiar); err != nil &&
				!errors.Is(err, config.ErrPathNotFound) {
				return err
			}
		}
		// zmiana nazwy na końcu — inaczej kolejne szukania po starej nazwie zawiodą
		if ż.NowaNazwa != "" && ż.NowaNazwa != ż.Nazwa {
			if err := d.SetServiceField(ż.Grupa, ż.Nazwa, "name", ż.NowaNazwa); err != nil {
				return err
			}
		}
		return nil
	})
	if ok {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleServiceDelete(w http.ResponseWriter, r *http.Request) {
	grupa := r.URL.Query().Get("group")
	nazwa := r.URL.Query().Get("name")
	if !poprawnaNazwaWpisu(grupa) || !poprawnaNazwaWpisu(nazwa) {
		jsonBłąd(w, http.StatusBadRequest, "podaj group i name")
		return
	}
	opis := fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_usunieto_usluge"), nazwa, grupa)
	if s.zEdycją(w, r, opis, func(d *config.Document) error { return d.RemoveService(grupa, nazwa) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleServiceMove ustawia usługę na konkretnej pozycji — w tej samej grupie
// albo w innej. index liczy się w liście docelowej po przesunięciu.
func (s *Server) handleServiceMove(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Grupa   string `json:"group"`
		Nazwa   string `json:"name"`
		DoGrupy string `json:"toGroup"`
		Pozycja *int   `json:"index"`
	}
	lang := i18n.Wybierz(r)
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if ż.DoGrupy == "" {
		ż.DoGrupy = ż.Grupa
	}
	if !poprawnaNazwaWpisu(ż.Grupa) || !poprawnaNazwaWpisu(ż.Nazwa) ||
		!poprawnaNazwaWpisu(ż.DoGrupy) || ż.Pozycja == nil {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_przenoszenie"))
		return
	}
	opis := fmt.Sprintf(i18n.T(lang, "opis_kolejnosc_uslugi"), ż.Nazwa, ż.DoGrupy, *ż.Pozycja+1)
	if ż.DoGrupy != ż.Grupa {
		opis = fmt.Sprintf(i18n.T(lang, "opis_przeniesiono"), ż.Nazwa, ż.Grupa, ż.DoGrupy)
	}
	pary := map[string]string{key(ż.Grupa, ż.Nazwa): key(ż.DoGrupy, ż.Nazwa)}
	if s.zEdycjąZKluczami(w, r, opis, func(*config.Config) map[string]string { return pary },
		func(d *config.Document) error {
			return d.MoveServiceTo(ż.Grupa, ż.Nazwa, ż.DoGrupy, *ż.Pozycja)
		}) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleGroupMove ustawia całą grupę na pozycji index.
func (s *Server) handleGroupMove(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Nazwa   string `json:"name"`
		Pozycja *int   `json:"index"`
	}
	lang := i18n.Wybierz(r)
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if !poprawnaNazwaWpisu(ż.Nazwa) || ż.Pozycja == nil {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_przenoszenie"))
		return
	}
	opis := fmt.Sprintf(i18n.T(lang, "opis_kolejnosc_grupy"), ż.Nazwa, *ż.Pozycja+1)
	if s.zEdycją(w, r, opis, func(d *config.Document) error { return d.MoveGroup(ż.Nazwa, *ż.Pozycja) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── tytuł panelu ─────────────────────────────────────────────────────────────

// handleTitlePut zapisuje top-level `title` jednym spliceem. Walidacja leży
// po stronie serwera niezależnie od pola w przeglądarce.
func (s *Server) handleTitlePut(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Tytuł string `json:"title"`
	}
	lang := i18n.Wybierz(r)
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	tytuł, err := config.NormalizujTytuł(ż.Tytuł)
	if err != nil {
		klucz := "blad_tytul_znaki"
		switch {
		case errors.Is(err, config.ErrTytułPusty):
			klucz = "blad_tytul_pusty"
		case errors.Is(err, config.ErrTytułDługi):
			klucz = "blad_tytul_dlugi"
		}
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, klucz))
		return
	}
	opis := fmt.Sprintf(i18n.T(lang, "opis_tytul"), tytuł)
	if s.zEdycją(w, r, opis, func(d *config.Document) error { return d.UstawTytuł(tytuł) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true, "title": tytuł})
	}
}

// ── grupy ────────────────────────────────────────────────────────────────────

func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Nazwa string `json:"name"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil || ż.Nazwa == "" {
		jsonBłąd(w, http.StatusBadRequest, "podaj name")
		return
	}
	if s.zEdycją(w, r, fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_dodano_grupe"), ż.Nazwa),
		func(d *config.Document) error { return d.AddGroup(ż.Nazwa) }) {
		jsonOdp(w, http.StatusCreated, map[string]any{"ok": true})
	}
}

func (s *Server) handleGroupUpdate(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Nazwa     string `json:"name"`
		NowaNazwa string `json:"newName"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil || ż.Nazwa == "" || ż.NowaNazwa == "" {
		jsonBłąd(w, http.StatusBadRequest, "podaj name i newName")
		return
	}
	// każda usługa grupy zmienia klucz; pary z configu sprzed zmiany, bo nazwa
	// grupy może zawierać „/" i prefiks byłby niejednoznaczny
	migr := func(stara *config.Config) map[string]string {
		pary := map[string]string{}
		if stara == nil {
			return pary
		}
		for _, g := range stara.Groups {
			if g.Name != ż.Nazwa {
				continue
			}
			for _, svc := range g.Services {
				pary[key(g.Name, svc.Name)] = key(ż.NowaNazwa, svc.Name)
			}
		}
		return pary
	}
	if s.zEdycjąZKluczami(w, r, fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_zmieniono_grupe"), ż.Nazwa, ż.NowaNazwa),
		migr, func(d *config.Document) error { return d.RenameGroup(ż.Nazwa, ż.NowaNazwa) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	nazwa := r.URL.Query().Get("name")
	if !poprawnaNazwaWpisu(nazwa) {
		jsonBłąd(w, http.StatusBadRequest, "podaj name")
		return
	}
	if s.zEdycją(w, r, fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_usunieto_grupe"), nazwa),
		func(d *config.Document) error { return d.RemoveGroup(nazwa) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── surowy YAML ──────────────────────────────────────────────────────────────

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	raw, err := configBytes(s.path)
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOdp(w, http.StatusOK, map[string]any{"yaml": string(raw)})
}

// handleConfigPut zapisuje ręcznie zredagowany YAML — dla tych, którzy wolą
// plik od klikania. Odrzuca treść, która się nie parsuje.
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		YAML string `json:"yaml"`
	}
	if err := dekodujJSONLimit(w, r, &ż, limitConfigu); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if _, err := config.Load([]byte(ż.YAML)); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "niepoprawny YAML: "+err.Error())
		return
	}
	if s.zEdycją(w, r, i18n.T(i18n.Wybierz(r), "opis_reczna_edycja"),
		func(d *config.Document) error { return d.ReplaceAll([]byte(ż.YAML)) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── ikony ────────────────────────────────────────────────────────────────────

func (s *Server) handleIcons(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 300 {
		limit = v
	}
	nazwy := s.ikony.Szukaj(q, limit)
	// warianty niesie tylko te pozycje, które mają osobną grafikę na ciemny i
	// jasny motyw — przeglądarka na tej podstawie renderuje oba obrazy i
	// przełącza je czystym CSS-em (zero drugiego requestu przy zmianie tonu).
	warianty := make(map[string][2]string, 8)
	for _, n := range nazwy {
		if ciemny, jasny := s.ikony.Warianty(n); ciemny != "" {
			warianty[n] = [2]string{ciemny, jasny}
		}
	}
	jsonOdp(w, http.StatusOK, map[string]any{
		"icons":    nazwy,
		"warianty": warianty,
		"total":    s.ikony.Liczba(),
		"mode":     s.ikony.Tryb(),
	})
}

// ── historia ─────────────────────────────────────────────────────────────────

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	klucz := r.URL.Query().Get("key")
	if klucz == "" {
		jsonBłąd(w, http.StatusBadRequest, "podaj key")
		return
	}
	n := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 500 {
		n = v
	}
	h, err := s.st.Historia(r.Context(), klucz, n)
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	dostępność, próbek, _ := s.st.Dostępność(r.Context(), klucz, 24*time.Hour)

	type punkt struct {
		Stan  string `json:"state"`
		MS    int64  `json:"ms"`
		Kiedy string `json:"at"`
	}
	punkty := make([]punkt, 0, len(h))
	for _, w := range h {
		punkty = append(punkty, punkt{Stan: w.Stan, MS: w.MS, Kiedy: w.Kiedy.Format(time.RFC3339)})
	}
	jsonOdp(w, http.StatusOK, map[string]any{
		"key":     klucz,
		"points":  punkty,
		"uptime":  dostępność,
		"samples": próbek,
	})
}

// ── konta ────────────────────────────────────────────────────────────────────

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	lista, err := s.st.Użytkownicy(r.Context())
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	type u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	out := make([]u, 0, len(lista))
	for _, x := range lista {
		out = append(out, u{ID: x.ID, Login: x.Login})
	}
	jsonOdp(w, http.StatusOK, map[string]any{"users": out, "me": UżytkownikZŻądania(r).Login})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Login string `json:"login"`
		Hasło string `json:"password"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if _, err := s.st.DodajUżytkownika(r.Context(), strings.TrimSpace(ż.Login), ż.Hasło); err != nil {
		jsonBłąd(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOdp(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonBłąd(w, http.StatusBadRequest, "złe id")
		return
	}
	ja := UżytkownikZŻądania(r)
	if ja != nil && ja.ID == id {
		jsonBłąd(w, http.StatusBadRequest, "nie możesz usunąć własnego konta")
		return
	}
	lista, _ := s.st.Użytkownicy(r.Context())
	if len(lista) <= 1 {
		jsonBłąd(w, http.StatusBadRequest, "to ostatnie konto — panel zostałby bez dostępu")
		return
	}
	if err := s.st.UsuńUżytkownika(r.Context(), id); err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Stare string `json:"old"`
		Nowe  string `json:"new"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	ja := UżytkownikZŻądania(r)
	if ja == nil {
		jsonBłąd(w, http.StatusUnauthorized, "brak sesji")
		return
	}
	if _, err := s.st.Sprawdź(r.Context(), ja.Login, ż.Stare); err != nil {
		jsonBłąd(w, http.StatusForbidden, "obecne hasło się nie zgadza")
		return
	}
	if err := s.st.ZmieńHasło(r.Context(), ja.ID, ż.Nowe); err != nil {
		jsonBłąd(w, http.StatusBadRequest, err.Error())
		return
	}
	// ! Zmiana hasła musi wyrzucić resztę sesji. Ktoś z ukradzionym ciasteczkiem
	// inaczej zostawał w panelu do końca 30 dni. Bieżąca sesja zostaje.
	if c, err := r.Cookie(ciasteczkoSesji); err == nil {
		if err := s.st.UsuńInneSesje(r.Context(), ja.ID, c.Value); err != nil {
			slog.Error("kończenie innych sesji po zmianie hasła", "err", err)
		}
	}
	jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
}

// ── historia zmian configu ───────────────────────────────────────────────────

func (s *Server) handleVersionsList(w http.ResponseWriter, r *http.Request) {
	lista, err := s.st.Wersje(r.Context(), 30)
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	type wpis struct {
		ID       int64  `json:"id"`
		Kiedy    string `json:"at"`
		Autor    string `json:"author"`
		Opis     string `json:"summary"`
		Dodane   int    `json:"added"`
		Usunięte int    `json:"removed"`
	}
	out := make([]wpis, 0, len(lista))
	for _, w := range lista {
		d, u := config.PodsumujDiff(w.Diff)
		out = append(out, wpis{ID: w.ID, Kiedy: w.Kiedy.Format(time.RFC3339),
			Autor: w.Autor, Opis: w.Opis, Dodane: d, Usunięte: u})
	}
	jsonOdp(w, http.StatusOK, map[string]any{"versions": out, "git": gitrepo.WRepo(r.Context(), s.path)})
}

func (s *Server) handleVersionOne(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonBłąd(w, http.StatusBadRequest, "złe id")
		return
	}
	v, err := s.st.Wersja(r.Context(), id)
	if err != nil {
		jsonBłąd(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOdp(w, http.StatusOK, map[string]any{
		"id": v.ID, "at": v.Kiedy.Format(time.RFC3339), "author": v.Autor,
		"summary": v.Opis, "diff": v.Diff, "yaml": v.Treść,
	})
}

// handleVersionRestore przywraca plik do stanu z danej wersji. Samo przywrócenie
// też trafia do historii, więc cofnięcie cofnięcia jest możliwe.
func (s *Server) handleVersionRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonBłąd(w, http.StatusBadRequest, "złe id")
		return
	}
	v, err := s.st.Wersja(r.Context(), id)
	if err != nil {
		jsonBłąd(w, http.StatusNotFound, err.Error())
		return
	}
	if _, err := config.Load([]byte(v.Treść)); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "ta wersja się nie parsuje: "+err.Error())
		return
	}
	opis := fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_przywrocono"), v.ID, v.Opis)
	if s.zEdycją(w, r, opis, func(d *config.Document) error { return d.ReplaceAll([]byte(v.Treść)) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── szczegóły usługi ─────────────────────────────────────────────────────────

// handleService zbiera wszystko, co wiemy o jednej usłudze: stan teraz,
// dostępność w dwóch oknach, czasy odpowiedzi, wykres i momenty zmian stanu.
func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	klucz := r.URL.Query().Get("key")
	if klucz == "" {
		jsonBłąd(w, http.StatusBadRequest, "podaj key")
		return
	}

	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	var svc *config.Service
	var grupa string
	if cfg != nil {
		for _, g := range cfg.Groups {
			for i := range g.Services {
				if key(g.Name, g.Services[i].Name) == klucz {
					svc = &g.Services[i]
					grupa = g.Name
				}
			}
		}
	}
	if svc == nil {
		jsonBłąd(w, http.StatusNotFound, "nie ma takiej usługi")
		return
	}

	n := 120
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 1000 {
		n = v
	}
	historia, err := s.st.Historia(r.Context(), klucz, n)
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	doba, _ := s.st.Staty(r.Context(), klucz, 24*time.Hour)
	tydzień, _ := s.st.Staty(r.Context(), klucz, 7*24*time.Hour)
	zmiany, _ := s.st.Zmiany(r.Context(), klucz, 12)

	type punkt struct {
		Stan  string `json:"state"`
		MS    int64  `json:"ms"`
		Kiedy string `json:"at"`
		Kod   int    `json:"code,omitempty"`
		Błąd  string `json:"err,omitempty"`
	}
	punkty := make([]punkt, 0, len(historia))
	for _, h := range historia {
		punkty = append(punkty, punkt{Stan: h.Stan, MS: h.MS, Kiedy: h.Kiedy.Format(time.RFC3339),
			Kod: h.Kod, Błąd: h.Błąd})
	}

	type zmiana struct {
		Kiedy string `json:"at"`
		Z     string `json:"from"`
		Na    string `json:"to"`
		Błąd  string `json:"err,omitempty"`
		// Kod pozwala pokazać powód „HTTP 502" zamiast samego tekstu błędu
		Kod int `json:"code,omitempty"`
	}
	zm := make([]zmiana, 0, len(zmiany))
	for _, z := range zmiany {
		zm = append(zm, zmiana{Kiedy: z.Kiedy.Format(time.RFC3339), Z: z.Z, Na: z.Na,
			Błąd: z.Błąd, Kod: z.Kod})
	}

	statyJSON := func(x store.Statystyki) map[string]any {
		return map[string]any{"samples": x.Próbek, "uptime": x.Dostępność,
			"p50": x.P50, "p95": x.P95, "max": x.Najgorszy}
	}

	ikona := s.rozwiążIkonę(svc.Icon, svc.Name)
	jsonOdp(w, http.StatusOK, map[string]any{
		"key": klucz, "name": svc.Name, "group": grupa, "url": svc.URL, "icon": svc.Icon,
		"iconSrc": ikona.Src, "iconWariantCiemny": ikona.WariantCiemny, "iconWariantJasny": ikona.WariantJasny,
		"size": svc.Size,
		"interval": svc.Check.Interval.String(), "timeout": svc.Check.Timeout.String(),
		"expect": svc.Check.Expect, "insecure": svc.Check.Insecure,
		"now":     s.mon.Snapshot()[klucz],
		"day":     statyJSON(doba),
		"week":    statyJSON(tydzień),
		"points":  punkty,
		"changes": zm,
	})
}

// handleCheckNow sprawdza usługę natychmiast, nie czekając na harmonogram.
func (s *Server) handleCheckNow(w http.ResponseWriter, r *http.Request) {
	klucz := r.URL.Query().Get("key")
	if klucz == "" {
		jsonBłąd(w, http.StatusBadRequest, "podaj key")
		return
	}
	res, err := s.mon.SprawdźTeraz(r.Context(), klucz)
	if err != nil {
		jsonBłąd(w, http.StatusNotFound, err.Error())
		return
	}
	// wynik od razu do historii, żeby wykres go pokazał
	_ = s.st.ZapiszWynik(r.Context(), store.Wynik{
		Klucz: klucz, Stan: string(res.State), Kod: res.Code,
		MS: res.MS, Błąd: res.Err, Kiedy: res.At,
	})
	jsonOdp(w, http.StatusOK, res)
}

// handleNotifyTest wysyła wiadomość próbną, żeby dało się sprawdzić webhooka
// bez czekania, aż coś naprawdę padnie.
// maskaWebhooka zostawia schemat i host, resztę zamienia na kropki.
// Użytkownik ma rozpoznać, czy to ten adres — nie odczytać go z ekranu ani
// z odpowiedzi API.
func maskaWebhooka(adres string) string {
	if adres == "" {
		return ""
	}
	u, err := url.Parse(adres)
	if err != nil || u.Host == "" {
		return "•••"
	}
	return u.Scheme + "://" + u.Host + "/•••"
}

func (s *Server) źródłoWebhooka() string {
	switch {
	case s.webhookZeŚrodowiska:
		return "srodowisko"
	case s.powiad.Webhook() != "":
		return "baza"
	default:
		return "brak"
	}
}

func (s *Server) handleNotifyGet(w http.ResponseWriter, r *http.Request) {
	jsonOdp(w, http.StatusOK, map[string]any{
		"wlaczony": s.powiad.Włączony(),
		"webhook":  maskaWebhooka(s.powiad.Webhook()),
		"zrodlo":   s.źródłoWebhooka(),
		"prog":     s.powiad.Próg(),
	})
}

// handleNotifyPut pisze do DWÓCH warstw naraz: adres do SQLite (bo jest
// sekretem, a plik configu jest commitowany), próg do YAML-a (bo nie jest).
// To jedyne miejsce, gdzie podział warstw przecieka do API — alternatywą były
// dwie trasy i obsługa połowicznego niepowodzenia po stronie UI.
func (s *Server) handleNotifyPut(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Wybierz(r)
	var ż struct {
		Webhook *string `json:"webhook"`
		Próg    *int    `json:"prog"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}

	if ż.Webhook != nil {
		if s.webhookZeŚrodowiska {
			jsonBłąd(w, http.StatusConflict, i18n.T(lang, "webhook_ze_srodowiska"))
			return
		}
		adres := strings.TrimSpace(*ż.Webhook)
		if adres != "" {
			// ! Tylko https. Sekret jedzie w ścieżce adresu, więc po http
			// poszedłby otwartym tekstem.
			u, err := url.Parse(adres)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "webhook_zly_adres"))
				return
			}
		}
		if err := s.st.UstawUstawienie(r.Context(), "notify.webhook", adres); err != nil {
			jsonBłąd(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.powiad.UstawWebhook(adres)
	}

	if ż.Próg != nil {
		if *ż.Próg < 1 || *ż.Próg > 10 {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "webhook_zly_prog"))
			return
		}
		if !s.zEdycją(w, r, "próg powiadomień", func(d *config.Document) error {
			return d.UstawPrógPowiadomień(*ż.Próg)
		}) {
			return
		}
		s.powiad.UstawPróg(*ż.Próg)
	}

	s.handleNotifyGet(w, r)
}

// handleInstance wylicza opcje startu — wyłącznie do odczytu.
func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	jsonOdp(w, http.StatusOK, map[string]any{"opcje": s.instancja})
}

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if !s.powiad.Włączony() {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(i18n.Wybierz(r), "webhook_zrodlo_brak"))
		return
	}
	if err := s.powiad.Test(r.Context()); err != nil {
		jsonBłąd(w, http.StatusBadGateway, err.Error())
		return
	}
	jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
}

// handleInfo mówi UI, co jest w tej instancji włączone.
//
// ! "docker" NIE BYŁO tu zwracane, przez co app.js nigdy nie pokazywał
// przycisku „Znajdź usługi" — cała funkcja podpowiadania z Dockera była
// niedostępna z UI. To naprawa żywego buga, nie nowa funkcja.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	jsonOdp(w, http.StatusOK, map[string]any{
		"version":     Version,
		"notify":      s.powiad.Włączony(),
		"git":         gitrepo.WRepo(r.Context(), s.path),
		"icons":       map[string]any{"mode": s.ikony.Tryb(), "total": s.ikony.Liczba()},
		"config":      s.path,
		"docker":      s.dockerDostępny(),
		// Wyłączone w pliku to nie to samo co brak gniazda: pierwsze user
		// wybrał sam i pozycja ma zniknąć, drugie to stan środowiska i
		// pozycja ma zblednąć, żeby było widać, że w ogóle istnieje.
		"dockerWylaczony": !s.dockerWłączony(),
		"dockerNew":   s.ileNowychWDockerze(r.Context(), host),
		"backgrounds": s.tła.Dostępny(),
		"appearance":  s.wyglądInstancji().doAPI(),
	})
}

// ileNowychWDockerze liczy kontenery spoza configu. Wynik żyje minutę, bo
// /api/info woła się przy każdym otwarciu panelu, a gniazdo Dockera nie musi
// tego oglądać za każdym razem.
func (s *Server) ileNowychWDockerze(ctx context.Context, host string) int {
	if !s.dockerDostępny() {
		return 0
	}
	s.muDocker.Lock()
	defer s.muDocker.Unlock()
	if time.Since(s.dockerKiedy) < time.Minute {
		return s.dockerIle
	}
	s.dockerKiedy = time.Now()

	ctx, anuluj := context.WithTimeout(ctx, 2*time.Second)
	defer anuluj()
	kandydaci, err := s.docker.Kandydaci(ctx, host)
	if err != nil {
		return s.dockerIle
	}

	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	mamy := map[string]bool{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			for _, svc := range g.Services {
				mamy[strings.ToLower(svc.Name)] = true
				if svc.URL != "" {
					mamy[strings.ToLower(svc.URL)] = true
				}
			}
		}
	}
	nowe := 0
	for _, k := range kandydaci {
		if !mamy[strings.ToLower(k.Nazwa)] && !(k.URL != "" && mamy[strings.ToLower(k.URL)]) {
			nowe++
		}
	}
	s.dockerIle = nowe
	return nowe
}

// ── wygląd ───────────────────────────────────────────────────────────────────

func (s *Server) handleWyglądGet(w http.ResponseWriter, r *http.Request) {
	jsonOdp(w, http.StatusOK, s.wyglądInstancji().doAPI())
}

// żądanieWyglądu — same wskaźniki, bo trzeba odróżnić „nie ruszaj tego pola"
// od „ustaw je na puste". Brak pola = ta linia w YAML zostaje nietknięta.
type żądanieWyglądu struct {
	Preset *string `json:"preset"`
	Obraz  *string `json:"image"`
	Blur   *int    `json:"blur"`
	Dim    *int    `json:"dim"`
	Ton    *string `json:"tone"`
	Widok  *string `json:"view"`
}

// handleWyglądPut zapisuje wygląd instancji do YAML-a.
//
// Idzie tą samą drogą co każda inna edycja — zEdycją, czyli podgląd diffa,
// wersja w SQLite i commit do gita. Wgranie samego pliku tapety niczego w
// configu nie zmienia; zmienia dopiero ten moment.
//
// ! Walidacja jest tu SUROWSZA niż w Load: cokolwiek nie pasuje, kończy się
// 400, a nie cichym wyprostowaniem. Użytkownik ma się dowiedzieć, że się
// pomylił, zamiast dostać zapisaną wartość, o którą nie prosił.
func (s *Server) handleWyglądPut(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Wybierz(r)
	var ż żądanieWyglądu
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}

	bieżący := s.wyglądInstancji()
	pola := map[string]string{}
	var zmiany []string

	docelowyPreset, docelowyObraz := bieżący.Preset, bieżący.Obraz
	if ż.Preset != nil {
		if !config.Dozwolone(*ż.Preset, config.PresetyTła) {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		docelowyPreset = *ż.Preset
		pola["preset"] = *ż.Preset
		zmiany = append(zmiany, "preset="+*ż.Preset)
	}
	if ż.Ton != nil {
		if !config.Dozwolone(*ż.Ton, config.TonyTła) {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		pola["tone"] = *ż.Ton
		zmiany = append(zmiany, "tone="+*ż.Ton)
	}
	if ż.Widok != nil {
		if !config.Dozwolone(*ż.Widok, config.Widoki) {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		pola["view"] = *ż.Widok
		zmiany = append(zmiany, "view="+*ż.Widok)
	}
	if ż.Blur != nil {
		if *ż.Blur < 0 || *ż.Blur > config.MaxBlur {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		pola["blur"] = strconv.Itoa(*ż.Blur)
		zmiany = append(zmiany, "blur="+pola["blur"])
	}
	if ż.Dim != nil {
		if *ż.Dim < 0 || *ż.Dim > config.MaxDim {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		}
		pola["dim"] = strconv.Itoa(*ż.Dim)
		zmiany = append(zmiany, "dim="+pola["dim"])
	}
	if ż.Obraz != nil {
		obraz := *ż.Obraz
		switch {
		case obraz == "":
			// czyszczenie referencji — poprawne przy każdym presecie
		case !config.NazwaTła.MatchString(obraz), docelowyPreset != config.PresetPhoto:
			jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
			return
		case !s.tła.Istnieje(obraz):
			jsonBłąd(w, http.StatusNotFound, i18n.T(lang, "blad_wygladu"))
			return
		}
		docelowyObraz = obraz
		pola["image"] = obraz
		zmiany = append(zmiany, "image="+obraz)
	}
	if docelowyPreset == config.PresetPhoto && docelowyObraz == "" {
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_wygladu"))
		return
	}
	if len(pola) == 0 {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	opis := fmt.Sprintf(i18n.T(lang, "opis_zmieniono_wyglad"), strings.Join(zmiany, ", "))
	if s.zEdycją(w, r, opis, func(d *config.Document) error { return d.UstawWygląd(pola) }) {
		jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ── podpowiadanie usług z Dockera ────────────────────────────────────────────

// handleDiscover proponuje wpisy na podstawie działających kontenerów.
// Niczego nie zapisuje — to użytkownik decyduje, co przyjąć.
// dockerWłączony — brak klucza `discovery.docker` znaczy „jak dotąd", czyli
// podpowiadaj, jeśli gniazdo jest. Dopiero jawne `false` wyłącza.
func (s *Server) dockerWłączony() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil || s.cfg.Discovery.Docker == nil {
		return true
	}
	return *s.cfg.Discovery.Docker
}

// dockerDostępny łączy oba warunki: gniazdo musi być, a użytkownik nie mógł go
// wyłączyć w pliku.
func (s *Server) dockerDostępny() bool {
	return s.docker != nil && s.docker.Dostępny() && s.dockerWłączony()
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	// ! Dwa różne powody odmowy i dwa różne kody. 403 znaczy „wyłączyłeś to
	// sam", 404 „nie ma gniazda" — mylenie ich zmusza użytkownika do zgadywania,
	// czy szukać błędu w pliku, czy w wolumenie.
	if !s.dockerWłączony() {
		jsonBłąd(w, http.StatusForbidden, i18n.T(i18n.Wybierz(r), "docker_wylaczony"))
		return
	}
	if s.docker == nil || !s.docker.Dostępny() {
		jsonBłąd(w, http.StatusNotFound, "gniazdo Dockera niedostępne")
		return
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if h := r.URL.Query().Get("host"); h != "" {
		host = h
	}

	kandydaci, err := s.docker.Kandydaci(r.Context(), host)
	if err != nil {
		jsonBłąd(w, http.StatusBadGateway, err.Error())
		return
	}

	// odsiej to, co już jest w configu — po nazwie albo po adresie
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	mamy := map[string]bool{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			for _, svc := range g.Services {
				mamy[strings.ToLower(svc.Name)] = true
				if svc.URL != "" {
					mamy[strings.ToLower(svc.URL)] = true
				}
			}
		}
	}
	type propozycja struct {
		dockerdisc.Kandydat
		Znana bool `json:"known"`
	}
	out := make([]propozycja, 0, len(kandydaci))
	for _, k := range kandydaci {
		// Docker zna tylko obraz, a ten bywa myłący: kontener „jellyfin" z
		// obrazu nginx dostałby ikonę nginxa. Jeśli katalog ma ikonę o nazwie
		// usługi, jest trafniejsza niż ta z obrazu.
		if n := strings.ToLower(k.Nazwa); n != k.Ikona && s.ikony.Ma(n) {
			k.Ikona = n
		}
		out = append(out, propozycja{
			Kandydat: k,
			Znana:    mamy[strings.ToLower(k.Nazwa)] || (k.URL != "" && mamy[strings.ToLower(k.URL)]),
		})
	}

	grupy := []string{}
	if cfg != nil {
		for _, g := range cfg.Groups {
			grupy = append(grupy, g.Name)
		}
	}
	jsonOdp(w, http.StatusOK, map[string]any{"candidates": out, "groups": grupy, "host": host})
}

// handleServicesBulk dodaje wiele usług naraz — cała paczka idzie jako jedna
// zmiana pliku, więc user widzi jeden diff i dostaje jeden commit.
func (s *Server) handleServicesBulk(w http.ResponseWriter, r *http.Request) {
	var ż struct {
		Grupa  string          `json:"group"`
		Usługi []żądanieUsługi `json:"services"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	if len(ż.Usługi) == 0 {
		jsonBłąd(w, http.StatusBadRequest, "pusta lista")
		return
	}
	for _, u := range ż.Usługi {
		if !config.PoprawnyAdresUsługi(u.URL) {
			jsonBłąd(w, http.StatusBadRequest, i18n.T(i18n.Wybierz(r), "blad_adres_uslugi"))
			return
		}
	}

	opis := fmt.Sprintf(i18n.T(i18n.Wybierz(r), "opis_dodano_wiele"), len(ż.Usługi), ż.Grupa)
	ok := s.zEdycją(w, r, opis, func(d *config.Document) error {
		for _, u := range ż.Usługi {
			grupa := u.Grupa
			if grupa == "" {
				grupa = ż.Grupa
			}
			svc := config.Service{Name: u.Nazwa, URL: u.URL, Icon: u.Ikona,
				Size: rozmiarZŻądania(u.Rozmiar)}
			svc.Check = przeliczCheck(u)
			if err := d.AddService(grupa, svc); err != nil {
				return fmt.Errorf("%s: %w", u.Nazwa, err)
			}
		}
		return nil
	})
	if ok {
		jsonOdp(w, http.StatusCreated, map[string]any{"ok": true, "added": len(ż.Usługi)})
	}
}
