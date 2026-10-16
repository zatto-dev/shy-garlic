package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// Tokeny API tylko do odczytu (SHYG-49).
//
// Zasada jest jedna i wąska: nagłówek `Authorization: Bearer shyg_…` jest
// uznawany WYŁĄCZNIE dla GET na trasach z `trasyTokenowe`. Na każdej innej
// trasie (a zwłaszcza na każdej, która coś zmienia) jest ignorowany, jakby go
// nie było — wymagana jest sesja. Token wyciekły z Prometheusa nie może więc
// ani zmienić configu, ani założyć konta, ani stworzyć następnego tokenu.
var trasyTokenowe = map[string]bool{
	"/api/status":  true,
	"/api/history": true,
	"/metrics":     true,
}

func trasaTokenowa(r *http.Request) bool {
	return r.Method == http.MethodGet && trasyTokenowe[r.URL.Path]
}

// bearerZNagłówka wyciąga token z `Authorization: Bearer <token>`. Schemat jest
// nierozróżnialny co do wielkości liter (RFC 7235); wszystko poza dokładnie
// jednym tokenem po schemacie to „brak". Token w query stringu nie istnieje —
// URL-e lądują w logach proxy i w historii przeglądarki.
func bearerZNagłówka(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const schemat = "bearer "
	if len(h) <= len(schemat) || !strings.EqualFold(h[:len(schemat)], schemat) {
		return "", false
	}
	t := h[len(schemat):]
	if t == "" || strings.ContainsAny(t, " \t") {
		return "", false
	}
	return t, true
}

// odmówBezSesji — 401 w postaci, której oczekuje klient maszynowy.
func odmówBezSesji(w http.ResponseWriter, r *http.Request) {
	if trasaTokenowa(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Shy Garlic"`)
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// ── limity i logi nieudanych prób ────────────────────────────────────────────

// progBłędnychTokenów — ile nieudanych prób z jednego adresu w 5 minut zanim
// dostanie 429. Wyższy niż przy logowaniu (5): Prometheus z literówką w tokenie
// odpytuje co kilkanaście sekund i ma po jej poprawieniu odzyskać dostęp bez
// długiego czekania, a 2^256 możliwych tokenów i tak nie da się wylosować.
const progBłędnychTokenów = 60

// tłumikLogów pozwala logować coś najwyżej raz na `okres` na klucz — scraper z
// błędnym tokenem nie ma zalewać logów linią co 15 s.
type tłumikLogów struct {
	mu    sync.Mutex
	okres time.Duration
	ostatnio map[string]time.Time
}

func (t *tłumikLogów) wolno(klucz string, teraz time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ostatnio == nil || len(t.ostatnio) >= maksAdresówWLimiterze {
		t.ostatnio = map[string]time.Time{}
	}
	if o, ok := t.ostatnio[klucz]; ok && teraz.Sub(o) < t.okres {
		return false
	}
	t.ostatnio[klucz] = teraz
	return true
}

// uwierzytelnijToken sprawdza token z nagłówka. Zwraca true, gdy wolno
// obsłużyć żądanie; w przeciwnym razie sam odpowiada (401 albo 429).
func (s *Server) uwierzytelnijToken(w http.ResponseWriter, r *http.Request) bool {
	ip := adresIP(r)
	if !s.limTokenów.wolno(ip) {
		w.Header().Set("Retry-After", "300")
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return false
	}
	token, _ := bearerZNagłówka(r)
	t, err := s.st.SprawdźTokenAPI(r.Context(), token)
	switch {
	case err == nil:
		s.oznaczUżycieTokenu(r, t.ID)
		return true
	case errors.Is(err, store.ErrZłyToken):
		s.limTokenów.zapiszPorażkę(ip)
		if s.logiTokenów.wolno(ip, time.Now()) {
			slog.Warn("nieudane uwierzytelnienie tokenem API", "ip", ip, "ścieżka", r.URL.Path)
		}
	default:
		slog.Error("sprawdzanie tokenu API", "err", err)
	}
	odmówBezSesji(w, r)
	return false
}

// oznaczUżycieTokenu — last_used_at najwyżej raz na minutę. Pamięć podręczna
// oszczędza nawet odczyt z bazy przy częstym scrapowaniu.
func (s *Server) oznaczUżycieTokenu(r *http.Request, id int64) {
	teraz := time.Now()
	s.muUżycie.Lock()
	ostatnio, ok := s.użycieTokenów[id]
	if ok && teraz.Sub(ostatnio) < time.Minute {
		s.muUżycie.Unlock()
		return
	}
	s.użycieTokenów[id] = teraz
	s.muUżycie.Unlock()
	if err := s.st.OznaczUżycieTokenu(r.Context(), id, teraz); err != nil {
		slog.Debug("last_used_at tokenu", "err", err)
	}
}

// ── nieuwierzytelnione /metrics dla wskazanych sieci ─────────────────────────

// ustawMetrykiAllow przyjmuje CIDR-y (i gołe adresy), z których /metrics
// odpowiada bez tokenu. Pusta lista (domyślnie) = zawsze wymagany token.
func (s *Server) ustawMetrykiAllow(lista []string) error {
	p, err := parsujPrefiksy("metrics-allow", lista)
	if err != nil {
		return err
	}
	s.metrykiCIDR = p
	return nil
}

// metrykiZCIDR mówi, czy klient jest z sieci wpuszczanej bez tokenu.
//
// Dwie ostrożności, bo to jedyne miejsce, gdzie adres IP zastępuje poświadczenie:
//   - adres klienta idzie przez adresKlienta (skrajnie prawy niezaufany wpis
//     X-Forwarded-For, nagłówki tylko od zaufanych proxy);
//   - żądanie z oznakami proxy, którego nie wpisano na listę zaufanych, jest
//     odrzucane: widzimy wtedy adres pośrednika, nie klienta, a pośrednik stoi
//     zwykle w tej samej sieci LAN, więc bez tej reguły --metrics-allow
//     192.168.0.0/24 otwierałoby metryki całemu internetowi;
//   - zaufane proxy BEZ X-Forwarded-For (nginx z samym X-Real-IP, forwarder L4)
//     też jest odrzucane: adres klienta byłby wtedy adresem proxy, czyli każde
//     żądanie z internetu wyglądałoby jak LAN. Token dalej działa.
func (s *Server) metrykiZCIDR(r *http.Request) bool {
	if len(s.metrykiCIDR) == 0 || zPośrednikaBezZaufania(r) {
		return false
	}
	if odZaufanegoProxy(r) && !maWpisyXFF(r) {
		return false
	}
	a, ok := adresKlienta(r)
	if !ok {
		return false
	}
	return wPrefiksach(a, s.metrykiCIDR)
}


// ── API zarządzania tokenami (tylko sesja) ───────────────────────────────────

type tokenAPI struct {
	ID       int64  `json:"id"`
	Nazwa    string `json:"name"`
	Hint     string `json:"hint"`
	Autor    string `json:"author"`
	Created  string `json:"created"`
	LastUsed string `json:"lastUsed"` // pusty = nigdy
}

func tokenDoAPI(t store.TokenAPI) tokenAPI {
	o := tokenAPI{ID: t.ID, Nazwa: t.Nazwa, Hint: t.Wskazówka, Autor: t.Autor,
		Created: t.Utworzony.UTC().Format(time.RFC3339)}
	if !t.OstatnieUżycie.IsZero() {
		o.LastUsed = t.OstatnieUżycie.UTC().Format(time.RFC3339)
	}
	return o
}

func (s *Server) handleTokensList(w http.ResponseWriter, r *http.Request) {
	lista, err := s.st.TokenyAPI(r.Context())
	if err != nil {
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]tokenAPI, 0, len(lista))
	for _, t := range lista {
		out = append(out, tokenDoAPI(t))
	}
	jsonOdp(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Wybierz(r)
	var ż struct {
		Nazwa string `json:"name"`
	}
	if err := dekodujJSON(w, r, &ż); err != nil {
		jsonBłąd(w, http.StatusBadRequest, "zły JSON")
		return
	}
	autor := ""
	if u := UżytkownikZŻądania(r); u != nil {
		autor = u.Login
	}
	token, wpis, err := s.st.NowyTokenAPI(r.Context(), ż.Nazwa, autor)
	switch {
	case errors.Is(err, store.ErrNazwaTokenu):
		jsonBłąd(w, http.StatusBadRequest, i18n.T(lang, "blad_token_nazwa"))
		return
	case errors.Is(err, store.ErrZaDużoTokenów):
		jsonBłąd(w, http.StatusConflict, i18n.T(lang, "blad_token_limit"))
		return
	case err != nil:
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("utworzono token API", "nazwa", wpis.Nazwa, "autor", autor)
	// ! to jedyna odpowiedź z całym tokenem; nic nie ma jej zapamiętać
	w.Header().Set("Cache-Control", "no-store")
	o := tokenDoAPI(wpis)
	jsonOdp(w, http.StatusCreated, map[string]any{
		"token": token, "id": o.ID, "name": o.Nazwa, "hint": o.Hint, "created": o.Created,
	})
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonBłąd(w, http.StatusBadRequest, "złe id")
		return
	}
	if err := s.st.UsuńTokenAPI(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrBrakTokenu) {
			jsonBłąd(w, http.StatusNotFound, err.Error())
			return
		}
		jsonBłąd(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.muUżycie.Lock()
	delete(s.użycieTokenów, id)
	s.muUżycie.Unlock()
	slog.Info("odwołano token API", "id", id)
	jsonOdp(w, http.StatusOK, map[string]any{"ok": true})
}
