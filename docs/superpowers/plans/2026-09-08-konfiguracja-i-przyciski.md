# Przebudowa konfiguracji i systemu przycisków — plan implementacji

> **Dla wykonawców agentowych:** WYMAGANY SUB-SKILL: `superpowers:subagent-driven-development` (zalecany) albo `superpowers:executing-plans`. Kroki mają checkboxy (`- [ ]`) do odhaczania.

**Cel:** Uporządkować konfigurację w cztery warstwy o jasnych granicach i zastąpić trzynaście idiomów przycisku jedną bazą `.btn`.

**Architektura:** Warstwa decyduje o tym, kiedy wartość musi być znana i czy wolno jej trafić do gita. Bootstrap i bezpieczeństwo zostają we flagach CLI, treść produktu idzie do YAML-a edytowalnego z UI, jedyne poświadczenie (webhook) ląduje w SQLite, a nadpisania per-przeglądarka w `localStorage`. Przyciski dostają jedną bazę z zamkniętym zestawem wariantów, a stany toggle jadą na atrybutach ARIA zamiast na równoległych klasach.

**Stack:** Go 1.25 (bez cgo), `modernc.org/sqlite`, `goccy/go-yaml` wyłącznie do nawigacji po AST, `html/template` + waniliowy JS + SSE. Bez node w produkcie.

**Spec:** `docs/superpowers/specs/2026-09-08-konfiguracja-i-przyciski-design.md`

**Baseline:** `2fe4750` — `./x test` zielone.

## Ograniczenia globalne

Dotyczą każdego zadania.

- **Go nigdy nie wykonuje się lokalnie.** Jedyne wejście to `./x` (`test` / `vet` / `build` / `podglad`), który sam przerzuca drzewo na CT101 (`zatto@192.168.0.178`). Uruchomienie `go` na CT111 jest zabronione — box już dwa razy się przez to zawiesił.
- **Zapis YAML-a wyłącznie splice'em** przez `config.Document` (`internal/config/edit.go`). Nigdy ponowna serializacja — kasuje puste linie i psuje `<<: *defaults`.
- **Żadnego sekretu w YAML-u.** Plik jest pokazywany w UI i commitowany przez `internal/gitrepo`.
- **Każdy nowy napis w UI przechodzi przez i18n** — wpis w `internal/i18n/i18n.go` z parą `{EN: …, PL: …}`. Napisów wprost w szablonie nie ma.
- **Wartości lądujące w atrybutach HTML albo w regułach CSS muszą mieć allowlistę** (`config.Dozwolone`) albo zakres liczbowy sprawdzony po stronie serwera.
- **Commity:** Conventional Commits po polsku, stopka `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- **Zadanie jest skończone, gdy `./x test` jest zielone.** Nie gdy się kompiluje.

---

## FAZA 1 — backend modelu konfiguracji

### Task 1: Tabela `settings` w SQLite

**Pliki:**
- Modify: `internal/store/store.go:64-100` (`migruj`), koniec pliku (nowe metody)
- Test: `internal/store/store_test.go` (nowy)

**Interfejsy:**
- Produkuje: `func (s *Store) Ustawienie(ctx context.Context, klucz string) (string, error)` — brak klucza zwraca `""` i `nil`; `func (s *Store) UstawUstawienie(ctx context.Context, klucz, wartość string) error` — upsert; pusta wartość kasuje wiersz.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

func otwórzTestowy(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestUstawienieBrakKlucza(t *testing.T) {
	st := otwórzTestowy(t)
	v, err := st.Ustawienie(context.Background(), "notify.webhook")
	if err != nil {
		t.Fatalf("brak klucza ma być ciszą, dostałem błąd: %v", err)
	}
	if v != "" {
		t.Fatalf("brak klucza ma dać pusty string, dostałem %q", v)
	}
}

func TestUstawienieZapisIOdczyt(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	if err := st.UstawUstawienie(ctx, "notify.webhook", "https://example.test/hook"); err != nil {
		t.Fatalf("UstawUstawienie: %v", err)
	}
	v, err := st.Ustawienie(ctx, "notify.webhook")
	if err != nil {
		t.Fatalf("Ustawienie: %v", err)
	}
	if v != "https://example.test/hook" {
		t.Fatalf("chcę zapisany adres, mam %q", v)
	}
}

func TestUstawienieNadpisanie(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	_ = st.UstawUstawienie(ctx, "k", "pierwsza")
	if err := st.UstawUstawienie(ctx, "k", "druga"); err != nil {
		t.Fatalf("nadpisanie: %v", err)
	}
	v, _ := st.Ustawienie(ctx, "k")
	if v != "druga" {
		t.Fatalf("chcę druga, mam %q", v)
	}
}

// Pusta wartość kasuje wiersz, a nie zapisuje pustego stringa. Dzięki temu
// „wyczyść webhook" i „nigdy nie było webhooka" to ten sam stan i reszta kodu
// nie musi ich rozróżniać.
func TestUstawieniePustaKasuje(t *testing.T) {
	st := otwórzTestowy(t)
	ctx := context.Background()
	_ = st.UstawUstawienie(ctx, "k", "coś")
	if err := st.UstawUstawienie(ctx, "k", ""); err != nil {
		t.Fatalf("kasowanie: %v", err)
	}
	v, err := st.Ustawienie(ctx, "k")
	if err != nil || v != "" {
		t.Fatalf("chcę pusto bez błędu, mam %q / %v", v, err)
	}
}
```

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: `undefined: Store.Ustawienie`, `undefined: Store.UstawUstawienie`.

- [ ] **Krok 3: Dopisz tabelę do migracji**

W `internal/store/store.go`, w `migruj()`, na końcu literału SQL przed zamykającym backtickiem:

```sql
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- [ ] **Krok 4: Dopisz metody**

Na końcu `internal/store/store.go`:

```go
// ── ustawienia instancji ─────────────────────────────────────────────────────
//
// Klucz-wartość dla rzeczy, które NIE mogą trafić do pliku konfiguracyjnego,
// bo ten jest pokazywany w UI i commitowany do gita. Dziś mieszka tu jedno:
// adres webhooka powiadomień, czyli poświadczenie.

// Ustawienie zwraca wartość albo pusty string, gdy klucza nie ma. Brak klucza
// nie jest błędem — wywołujący i tak musiałby go odróżniać od pustej wartości,
// a nie ma po co.
func (s *Store) Ustawienie(ctx context.Context, klucz string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, klucz).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// UstawUstawienie zapisuje wartość. Pusta kasuje wiersz.
func (s *Store) UstawUstawienie(ctx context.Context, klucz, wartość string) error {
	if wartość == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, klucz)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value, at) VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, at = CURRENT_TIMESTAMP`, klucz, wartość)
	return err
}
```

Sprawdź, czy `errors` jest w importach pliku — jeśli nie, dopisz.

- [ ] **Krok 5: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): tabela ustawień instancji

Klucz-wartość na rzeczy, które nie mogą trafić do pliku konfiguracyjnego,
bo ten jest commitowany. Pusta wartość kasuje wiersz, żeby 'wyczyszczone'
i 'nigdy nie ustawione' były jednym stanem.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Webhook zmienny w locie

**Pliki:**
- Modify: `internal/notify/notify.go:44-70`
- Test: `internal/notify/notify_test.go`

**Interfejsy:**
- Konsumuje: `notify.Nowy(webhook string, próg int) *Powiadamiacz` (istnieje).
- Produkuje: `func (p *Powiadamiacz) UstawWebhook(adres string)`, `func (p *Powiadamiacz) Webhook() string`, `func (p *Powiadamiacz) UstawPróg(n int)`. Wszystkie bezpieczne przy równoległym użyciu — `Obserwuj` chodzi z pętli monitora, a `UstawWebhook` z żądania HTTP.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

Dopisz do `internal/notify/notify_test.go`:

```go
func TestUstawWebhookWłączaIWyłącza(t *testing.T) {
	p := Nowy("", 2)
	if p.Włączony() {
		t.Fatal("pusty webhook ma znaczyć wyłączone")
	}
	p.UstawWebhook("https://example.test/hook")
	if !p.Włączony() {
		t.Fatal("po ustawieniu adresu ma być włączone")
	}
	if got := p.Webhook(); got != "https://example.test/hook" {
		t.Fatalf("chcę zapisany adres, mam %q", got)
	}
	p.UstawWebhook("")
	if p.Włączony() {
		t.Fatal("wyczyszczenie adresu ma wyłączyć")
	}
}

// Próg poniżej 1 nie ma sensu — jedno nieudane sprawdzenie to jeszcze nie
// awaria, a zero znaczyłoby powiadomienie przy każdym mignięciu.
func TestUstawPrógProstuje(t *testing.T) {
	p := Nowy("", 5)
	p.UstawPróg(0)
	if p.Próg() != 1 {
		t.Fatalf("próg 0 ma się wyprostować do 1, mam %d", p.Próg())
	}
	p.UstawPróg(4)
	if p.Próg() != 4 {
		t.Fatalf("chcę 4, mam %d", p.Próg())
	}
}

// Odczyt z pętli monitora i zapis z żądania HTTP chodzą równolegle. Test ma
// sens wyłącznie pod -race, ale bez niego i tak nie zaszkodzi.
func TestWebhookRównolegle(t *testing.T) {
	p := Nowy("https://example.test/a", 2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			p.UstawWebhook("https://example.test/b")
			p.UstawWebhook("https://example.test/a")
		}
		close(done)
	}()
	for i := 0; i < 500; i++ {
		_ = p.Włączony()
		_ = p.Webhook()
	}
	<-done
}
```

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: `p.UstawWebhook undefined`, `p.Webhook undefined`, `p.UstawPróg undefined`, `p.Próg undefined`.

- [ ] **Krok 3: Dodaj mutex konfiguracji i metody**

W `internal/notify/notify.go` w strukturze `Powiadamiacz` zamień pola `webhook` i `próg` na chronione:

```go
	// muKonf broni webhooka i progu. Czyta je pętla monitora, pisze żądanie
	// HTTP z panelu — bez tego wyścig jest pewny, nie hipotetyczny.
	muKonf  sync.RWMutex
	webhook string
	próg    int
```

Na końcu pliku:

```go
// UstawWebhook podmienia adres w locie. Pusty wyłącza powiadomienia.
func (p *Powiadamiacz) UstawWebhook(adres string) {
	p.muKonf.Lock()
	defer p.muKonf.Unlock()
	p.webhook = adres
}

// Webhook zwraca aktualny adres.
func (p *Powiadamiacz) Webhook() string {
	p.muKonf.RLock()
	defer p.muKonf.RUnlock()
	return p.webhook
}

// UstawPróg podmienia liczbę nieudanych sprawdzeń z rzędu. Poniżej 1 nie ma
// sensu, więc prostuje się do 1.
func (p *Powiadamiacz) UstawPróg(n int) {
	if n < 1 {
		n = 1
	}
	p.muKonf.Lock()
	defer p.muKonf.Unlock()
	p.próg = n
}

// Próg zwraca aktualny próg.
func (p *Powiadamiacz) Próg() int {
	p.muKonf.RLock()
	defer p.muKonf.RUnlock()
	return p.próg
}
```

- [ ] **Krok 4: Przełącz istniejące odczyty na metody**

W `Włączony()` zamień `return p.webhook != ""` na `return p.Webhook() != ""`.
W `wyślij()` (`notify.go:145`) zamień `p.webhook` na lokalne `adres := p.Webhook()` i użyj `adres`.
W `Obserwuj()` (`notify.go:98`) zamień `p.próg` na `p.Próg()`.

- [ ] **Krok 5: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 6: Commit**

```bash
git add internal/notify/
git commit -m "feat(powiadomienia): webhook zmienny w locie

Adres i próg chronione mutexem, bo czyta je pętla monitora, a pisać
będzie żądanie z panelu. Bez tego wyścig jest pewny, nie hipotetyczny.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Nowe pola w modelu konfiguracji

**Pliki:**
- Modify: `internal/config/model.go`, `internal/config/load.go:47-110`
- Test: `internal/config/load_test.go`

**Interfejsy:**
- Produkuje: `Config.Notify Notify` (`Threshold int`), `Config.Discovery Discovery` (`Docker *bool`), `Background.Dim int`. Stałe `DomyślnyPróg = 2`, `DomyślnyDim = 50`, `MaxDim = 80`. `func kluczPodany(src []byte, klucz string) bool` zastępuje `blurPodany`.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

Dopisz do `internal/config/load_test.go`:

```go
func TestDomyślnyPrógIDim(t *testing.T) {
	c, err := Load([]byte("title: t\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Notify.Threshold != DomyślnyPróg {
		t.Fatalf("chcę próg %d, mam %d", DomyślnyPróg, c.Notify.Threshold)
	}
	if c.Appearance.Background.Dim != DomyślnyDim {
		t.Fatalf("chcę dim %d, mam %d", DomyślnyDim, c.Appearance.Background.Dim)
	}
}

// dim: 0 to świadome „bez przyciemnienia", a nie brak klucza. Dokładnie ta
// sama pułapka co przy blur — zero jest wartością zerową typu.
func TestDimZeroToWybór(t *testing.T) {
	c, err := Load([]byte("appearance:\n  background:\n    preset: photo\n    dim: 0\ngroups: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Appearance.Background.Dim != 0 {
		t.Fatalf("dim: 0 ma zostać zerem, mam %d", c.Appearance.Background.Dim)
	}
}

func TestDimPozaZakresemProstuje(t *testing.T) {
	c, _ := Load([]byte("appearance:\n  background:\n    dim: 900\ngroups: []\n"))
	if c.Appearance.Background.Dim != DomyślnyDim {
		t.Fatalf("dim poza zakresem ma wrócić do domyślnego, mam %d", c.Appearance.Background.Dim)
	}
}

func TestPrógPoniżejJednegoProstuje(t *testing.T) {
	c, _ := Load([]byte("notify:\n  threshold: 0\ngroups: []\n"))
	if c.Notify.Threshold != DomyślnyPróg {
		t.Fatalf("próg 0 ma wrócić do domyślnego, mam %d", c.Notify.Threshold)
	}
}

// Brak klucza discovery.docker znaczy „zachowaj się jak dotąd", czyli
// podpowiadaj, jeśli gniazdo jest dostępne. Dlatego wskaźnik, nie bool.
func TestDiscoveryDomyślnieNieustawione(t *testing.T) {
	c, _ := Load([]byte("groups: []\n"))
	if c.Discovery.Docker != nil {
		t.Fatalf("bez klucza ma być nil, mam %v", *c.Discovery.Docker)
	}
	c2, _ := Load([]byte("discovery:\n  docker: false\ngroups: []\n"))
	if c2.Discovery.Docker == nil || *c2.Discovery.Docker {
		t.Fatal("docker: false ma dać wskaźnik na false")
	}
}
```

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: `c.Notify undefined`, `DomyślnyPróg undefined`, `Dim undefined`, `c.Discovery undefined`.

- [ ] **Krok 3: Dopisz pola i stałe do modelu**

W `internal/config/model.go` przy pozostałych stałych wyglądu:

```go
const (
	MaxDim      = 80
	DomyślnyDim = 50
	// DomyślnyPróg — jedno nieudane sprawdzenie to jeszcze nie awaria.
	DomyślnyPróg = 2
)
```

W `type Config struct` dopisz:

```go
	Notify    Notify    `yaml:"notify"`
	Discovery Discovery `yaml:"discovery"`
```

Nowe typy:

```go
// Notify — konfiguracja powiadomień, która NIE jest sekretem.
//
// ! Adresu webhooka tu nie ma i nie będzie. Ten plik jest pokazywany w UI i
// commitowany, a URL webhooka jest poświadczeniem. Adres mieszka w SQLite.
type Notify struct {
	Threshold int `yaml:"threshold"`
}

// Discovery — co panel ma podpowiadać przy dodawaniu usługi.
//
// ! Wskaźnik, bo trzeba odróżnić „nie napisano nic" (wtedy podpowiadamy, jeśli
// gniazdo jest) od świadomego „docker: false".
type Discovery struct {
	Docker *bool `yaml:"docker"`
}
```

W `type Background struct` dopisz:

```go
	// Dim — krycie dolnego końca warstwy przyciemniającej, w procentach.
	// Górny koniec to zawsze Dim × 0,56 — proporcja z pierwotnych .28/.5,
	// żeby przyciemnienie narastało ku dołowi, gdzie stoją kafle.
	Dim int `yaml:"dim"`
```

- [ ] **Krok 4: Uogólnij `blurPodany` i dopisz domyślne**

W `internal/config/load.go` zamień `blurPodany(src []byte) bool` na:

```go
// kluczPodany mówi, czy klucz w ogóle stoi w pliku. Potrzebne wszędzie tam,
// gdzie zero jest sensowną wartością, więc nie da się go odróżnić od braku
// po samej wartości zerowej typu.
func kluczPodany(src []byte, klucz string) bool {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(klucz) + `\s*:`)
	return re.Match(src)
}
```

Wywołania `blurPodany(src)` zamień na `kluczPodany(src, "blur")`. Sprawdź, czy `regexp` jest w importach.

W `applyWygląd` dopisz obsługę `dim` obok obsługi `blur` (ta sama struktura, inny zakres):

```go
	if !dimPodany {
		b.Dim = DomyślnyDim
	} else if b.Dim < 0 || b.Dim > MaxDim {
		slog.Warn("przyciemnienie tła poza zakresem", "dim", b.Dim, "biorę", DomyślnyDim)
		b.Dim = DomyślnyDim
	}
```

Rozszerz sygnaturę `applyWygląd(a *Appearance, blurPodany, dimPodany bool)` i popraw wywołanie w `Load`.

W `Load`, po `applyWygląd`, dopisz:

```go
	if c.Notify.Threshold < 1 {
		c.Notify.Threshold = DomyślnyPróg
	}
```

- [ ] **Krok 5: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS. Jeśli `appearance_test.go` pada na zmienionej sygnaturze `applyWygląd`, popraw wywołania w teście — to jedyna dozwolona zmiana w istniejących testach.

- [ ] **Krok 6: Commit**

```bash
git add internal/config/
git commit -m "feat(config): próg powiadomień, przyciemnienie tła, przełącznik dockera

dim i blur dzielą pułapkę: zero jest sensowną wartością, więc braku
klucza nie da się rozpoznać po wartości zerowej typu. blurPodany
uogólnione do kluczPodany.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Zapis `dim` do YAML-a

**Pliki:**
- Modify: `internal/config/edit.go:659-800` (`UstawWygląd`, `ustawPoleWyglądu`, `wyglądZPól`, `renderWygląd`)
- Test: `internal/config/edit_test.go`

**Interfejsy:**
- Konsumuje: `(*Document).UstawWygląd(pola map[string]string) error`.
- Produkuje: ta sama metoda przyjmuje klucz `"dim"` obok `preset`, `image`, `blur`, `tone`, `view`.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

```go
func TestUstawWyglądDim(t *testing.T) {
	src := "appearance:\n  background:\n    preset: photo\n    blur: 10\ngroups: []\n"
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := d.UstawWygląd(map[string]string{"dim": "70"}); err != nil {
		t.Fatalf("UstawWygląd: %v", err)
	}
	c, err := Load(d.Bytes())
	if err != nil {
		t.Fatalf("Load po edycji: %v", err)
	}
	if c.Appearance.Background.Dim != 70 {
		t.Fatalf("chcę dim 70, mam %d", c.Appearance.Background.Dim)
	}
	// Splice ma ruszyć jedną linię, nie przepisać bloku.
	if !strings.Contains(string(d.Bytes()), "blur: 10") {
		t.Fatal("sąsiednie klucze mają zostać nietknięte")
	}
}
```

Jeśli metoda dostępu do bajtów nazywa się inaczej niż `Bytes()`, użyj tej, której używają sąsiednie testy w tym pliku.

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: FAIL — `dim` nie trafia do pliku, `Dim` zostaje domyślne.

- [ ] **Krok 3: Dopisz `dim` do trzech miejsc**

W `ustawPoleWyglądu` dodaj `"dim"` do listy kluczy obsługiwanych tak jak `"blur"` (wartość liczbowa, bez cudzysłowów).
W `wyglądZPól` sparsuj `pola["dim"]` do `Background.Dim`.
W `renderWygląd` dopisz linię `dim:` obok `blur:`, w tej samej kolejności co w strukturze.

- [ ] **Krok 4: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): zapis przyciemnienia tła

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Precedencja webhooka i `/api/notify`

**Pliki:**
- Modify: `internal/server/server.go:70-100` (`Opcje`, `New`), `internal/server/api.go` (nowe handlery), `internal/server/server.go:311-343` (trasy), `internal/i18n/i18n.go`
- Test: `internal/server/notify_api_test.go` (nowy)

**Interfejsy:**
- Konsumuje: `store.Ustawienie` / `store.UstawUstawienie` (Task 1), `notify.UstawWebhook` / `Webhook` / `UstawPróg` (Task 2), `config.Notify.Threshold` (Task 3).
- Produkuje: `Opcje.WebhookZeŚrodowiska bool`; `GET /api/notify` → `{"wlaczony":bool,"webhook":"https://…/•••","zrodlo":"srodowisko"|"baza"|"brak","prog":int}`; `PUT /api/notify` → `{"webhook":string,"prog":int}`, `409` gdy webhook pochodzi ze środowiska.

Maskowanie: zostaw schemat i host, resztę ścieżki zastąp `•••` — użytkownik ma rozpoznać, czy to ten adres, nie odczytać go.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotifyGetŹródłoBaza(t *testing.T) {
	s := serwerTestowy(t)
	if err := s.st.UstawUstawienie(s.ctx, "notify.webhook", "https://example.test/hooks/abc123"); err != nil {
		t.Fatalf("zapis: %v", err)
	}
	s.powiad.UstawWebhook("https://example.test/hooks/abc123")

	rr := httptest.NewRecorder()
	s.handleNotifyGet(rr, httptest.NewRequest("GET", "/api/notify", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d", rr.Code)
	}
	var odp struct {
		Włączony bool   `json:"wlaczony"`
		Webhook  string `json:"webhook"`
		Źródło   string `json:"zrodlo"`
	}
	json.NewDecoder(rr.Body).Decode(&odp)
	if !odp.Włączony || odp.Źródło != "baza" {
		t.Fatalf("chcę włączony/baza, mam %v/%q", odp.Włączony, odp.Źródło)
	}
	if strings.Contains(odp.Webhook, "abc123") {
		t.Fatalf("sekret ma być zamaskowany, poszedł w całości: %q", odp.Webhook)
	}
	if !strings.Contains(odp.Webhook, "example.test") {
		t.Fatalf("host ma zostać, żeby dało się rozpoznać adres: %q", odp.Webhook)
	}
}

// Webhook ze środowiska jest własnością deployu. Panel nie ma prawa go
// nadpisać — inaczej restart kontenera po cichu cofnąłby zmianę z UI.
func TestNotifyPutOdrzucaGdyŹródłoŚrodowisko(t *testing.T) {
	s := serwerTestowy(t)
	s.webhookZeŚrodowiska = true

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"https://inny.test/hook","prog":3}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("chcę 409, mam %d", rr.Code)
	}
	v, _ := s.st.Ustawienie(s.ctx, "notify.webhook")
	if v != "" {
		t.Fatalf("odrzucone żądanie nie ma nic zapisywać, w bazie leży %q", v)
	}
}

func TestNotifyPutZapisujeDoBazyIWłącza(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/notify",
		strings.NewReader(`{"webhook":"https://example.test/hooks/xyz","prog":4}`))
	s.handleNotifyPut(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d — ciało: %s", rr.Code, rr.Body.String())
	}
	v, _ := s.st.Ustawienie(s.ctx, "notify.webhook")
	if v != "https://example.test/hooks/xyz" {
		t.Fatalf("adres ma wylądować w bazie, mam %q", v)
	}
	if !s.powiad.Włączony() {
		t.Fatal("po zapisie powiadomienia mają być włączone bez restartu")
	}
}
```

Jeśli w pakiecie nie ma jeszcze helpera `serwerTestowy(t)`, napisz go na wzór tego, którego używa `render_test.go` — ma zwracać `*Server` z tymczasową bazą i tymczasowym plikiem configu.

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: `s.handleNotifyGet undefined`, `s.webhookZeŚrodowiska undefined`.

- [ ] **Krok 3: Dodaj pole i wczytanie webhooka na starcie**

W `internal/server/server.go` do `type Opcje struct` dopisz:

```go
	// WebhookZeŚrodowiska — adres przyszedł z flagi albo ze zmiennej, więc
	// panel go nie nadpisze. Deploy ma wygrywać z klikaniem.
	WebhookZeŚrodowiska bool
```

Do `type Server struct` dopisz `webhookZeŚrodowiska bool` i ustaw je w `New` z `o.WebhookZeŚrodowiska`.

W `New`, po utworzeniu serwera, gdy `!o.WebhookZeŚrodowiska`, wczytaj adres z bazy i wstrzyknij do powiadamiacza:

```go
	if !o.WebhookZeŚrodowiska {
		if adres, err := st.Ustawienie(context.Background(), "notify.webhook"); err == nil && adres != "" {
			s.powiad.UstawWebhook(adres)
		}
	}
```

- [ ] **Krok 4: Dopisz handlery**

W `internal/server/api.go`:

```go
// maskaWebhooka zostawia schemat i host, resztę zamienia na kropki.
// Użytkownik ma rozpoznać, czy to ten adres — nie odczytać go z ekranu.
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
// sekretem), próg do YAML-a (bo nie jest). To jedyne miejsce, gdzie podział
// warstw przecieka do API — alternatywą były dwie trasy i obsługa
// połowicznego niepowodzenia po stronie UI.
func (s *Server) handleNotifyPut(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Wybierz(r)
	var ż struct {
		Webhook *string `json:"webhook"`
		Próg    *int    `json:"prog"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ż); err != nil {
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
			return d.SetPathRaw(strconv.Itoa(*ż.Próg), "notify", "threshold")
		}) {
			return
		}
		s.powiad.UstawPróg(*ż.Próg)
	}

	s.handleNotifyGet(w, r)
}
```

Sprawdź importy: `net/url`, `strconv`, `strings`.

! Wymuszenie `https` jest celowe. Webhook idzie na zewnątrz z sekretem w ścieżce; po `http` poszedłby otwartym tekstem.

- [ ] **Krok 5: Dopisz napisy i trasy**

W `internal/i18n/i18n.go`:

```go
	"webhook_ze_srodowiska": {EN: "The webhook is set by an environment variable, so the panel will not change it.", PL: "Webhook jest ustawiony zmienną środowiskową, więc panel go nie zmieni."},
	"webhook_zly_adres":     {EN: "The webhook address has to be an https:// URL.", PL: "Adres webhooka musi być adresem https://."},
	"webhook_zly_prog":      {EN: "The failure threshold has to be between 1 and 10.", PL: "Próg awarii musi mieścić się między 1 a 10."},
```

W `internal/server/server.go` przy pozostałych trasach chronionych:

```go
	chronione.HandleFunc("GET /api/notify", s.handleNotifyGet)
	chronione.HandleFunc("PUT /api/notify", s.handleNotifyPut)
```

- [ ] **Krok 6: Zepnij flagę w `main.go`**

W `cmd/shyg/main.go`, przy budowaniu `server.Opcje`, dopisz:

```go
	WebhookZeŚrodowiska: *webhook != "",
```

- [ ] **Krok 7: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 8: Commit**

```bash
git add internal/ cmd/
git commit -m "feat(powiadomienia): webhook ustawialny z panelu

Adres idzie do SQLite, nie do YAML-a — plik jest commitowany, a URL
webhooka jest poświadczeniem. Wartość ze środowiska wygrywa z bazą i
PUT ją odrzuca z 409, żeby restart kontenera nie cofał po cichu zmiany
z panelu. W odpowiedzi adres zamaskowany do schematu i hosta.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: `/api/instance`

**Pliki:**
- Modify: `internal/server/server.go` (`Opcje`, `New`, trasy), `internal/server/api.go`
- Test: `internal/server/notify_api_test.go`

**Interfejsy:**
- Produkuje: `Opcje.Instancja []OpisOpcji` gdzie `type OpisOpcji struct { Klucz, Wartość, Źródło string }`; `GET /api/instance` → `{"opcje":[{"klucz":"addr","wartosc":":8080","zrodlo":"domyslna"}]}`.

Źródło ma trzy wartości: `flaga`, `srodowisko`, `domyslna`.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

```go
func TestInstanceZwracaŹródłaBezSekretów(t *testing.T) {
	s := serwerTestowy(t)
	s.instancja = []OpisOpcji{
		{Klucz: "addr", Wartość: ":8080", Źródło: "domyslna"},
		{Klucz: "trusted-proxy", Wartość: "10.0.0.0/8", Źródło: "flaga"},
	}
	rr := httptest.NewRecorder()
	s.handleInstance(rr, httptest.NewRequest("GET", "/api/instance", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("chcę 200, mam %d", rr.Code)
	}
	ciało := rr.Body.String()
	for _, chcę := range []string{"addr", ":8080", "domyslna", "trusted-proxy", "flaga"} {
		if !strings.Contains(ciało, chcę) {
			t.Fatalf("brakuje %q w %s", chcę, ciało)
		}
	}
	// Webhook jest sekretem i nie ma prawa tu być, nawet zamaskowany —
	// od niego jest /api/notify.
	if strings.Contains(ciało, "webhook") {
		t.Fatal("webhook nie należy do listy instancji")
	}
}
```

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: `OpisOpcji undefined`, `s.instancja undefined`, `s.handleInstance undefined`.

- [ ] **Krok 3: Dodaj typ, pole i handler**

W `internal/server/server.go`:

```go
// OpisOpcji — jedna opcja instancji na potrzeby zakładki „Instancja".
//
// ! Tu trafia wyłącznie to, czego ujawnienie jest bezpieczne. Webhook, choć
// też pochodzi z flagi, tu nie wchodzi — od niego jest /api/notify, które go
// maskuje.
type OpisOpcji struct {
	Klucz   string `json:"klucz"`
	Wartość string `json:"wartosc"`
	Źródło  string `json:"zrodlo"` // flaga | srodowisko | domyslna
}
```

Do `Opcje` dopisz `Instancja []OpisOpcji`, do `Server` pole `instancja []OpisOpcji`, ustaw w `New`.

W `internal/server/api.go`:

```go
func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	jsonOdp(w, http.StatusOK, map[string]any{"opcje": s.instancja})
}
```

Trasa: `chronione.HandleFunc("GET /api/instance", s.handleInstance)`.

- [ ] **Krok 4: Zbierz opcje w `main.go`**

W `cmd/shyg/main.go` dopisz funkcję i wywołaj ją przy budowaniu `Opcje`:

```go
// źródło mówi, skąd wzięła się wartość flagi. Flaga podana w linii poleceń
// bije zmienną środowiskową, bo tak działa envLub.
func źródło(fs *flag.FlagSet, nazwa, envKlucz string) string {
	ustawiona := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == nazwa {
			ustawiona = true
		}
	})
	if ustawiona {
		return "flaga"
	}
	if envKlucz != "" && os.Getenv(envKlucz) != "" {
		return "srodowisko"
	}
	return "domyslna"
}

func opisInstancji(fs *flag.FlagSet, o opcjeCLI) []server.OpisOpcji {
	return []server.OpisOpcji{
		{Klucz: "addr", Wartość: o.addr, Źródło: źródło(fs, "addr", "SHYG_ADDR")},
		{Klucz: "config", Wartość: o.cfgPath, Źródło: źródło(fs, "config", "SHYG_CONFIG")},
		{Klucz: "data", Wartość: o.dataPath, Źródło: źródło(fs, "data", "SHYG_DATA")},
		{Klucz: "cache", Wartość: o.cacheDir, Źródło: źródło(fs, "cache", "SHYG_CACHE")},
		{Klucz: "backgrounds", Wartość: o.tłaDir, Źródło: źródło(fs, "backgrounds", "SHYG_BACKGROUNDS")},
		{Klucz: "icons", Wartość: o.iconsZip, Źródło: źródło(fs, "icons", "SHYG_ICONS")},
		{Klucz: "docker", Wartość: o.dockerSock, Źródło: źródło(fs, "docker", "SHYG_DOCKER")},
		{Klucz: "trusted-proxy", Wartość: o.zaufaneProxy, Źródło: źródło(fs, "trusted-proxy", "SHYG_TRUSTED_PROXY")},
	}
}
```

`fs` trzeba przekazać z `run()` do `serve()` albo wywołać `opisInstancji` jeszcze w `run()` i wsadzić wynik do `opcjeCLI`. Wybierz drugie — `serve()` ma już dość argumentów.

- [ ] **Krok 5: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 6: Commit**

```bash
git add internal/ cmd/
git commit -m "feat(api): lista opcji instancji z podaniem źródła

Zamyka dziurę 'nie wiem, skąd panel to wziął', nie otwierając zmiany
rzeczy, które z panelu zmieniać się nie powinny. Webhook tu nie wchodzi
mimo że też jest flagą — jest sekretem, od niego jest /api/notify.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## FAZA 2 — system przycisków

### Task 7: Baza `.btn` i warianty

**Pliki:**
- Modify: `internal/server/assets/style.css`

**Interfejsy:**
- Produkuje: klasy `.btn`, `.btn--primary`, `.btn--quiet`, `.btn--danger`, `.btn--icon`, `.btn--tile`, `.btn--menu`, `.btn--sm`, `.btn--lg`; tokeny `--btn-bg`, `--btn-bg-hover`, `--btn-edge`, `--btn-ink`, `--btn-radius`, `--btn-ease` w `:root` i w bloku `:root[data-ton="light"]`.

To zadanie **tylko dodaje**. Żaden stary selektor nie znika — kasowanie jest w Tasku 8, żeby dało się odrzucić jedno bez drugiego.

- [ ] **Krok 1: Wyprowadź tokeny z projektu**

Otwórz `~/AI-Brain/Projects/shygarlic/design/Shy-Garlic-Glass.dc.html`. Styl siedzi w atrybutach `style`, nie w klasach, a narysowane są wyłącznie dwa ekrany: `Homelab panel` i `Service details`. Wyciągnij z nich: kolor tła przycisku, kolor obwódki, kolor tekstu, promień, cień, czas i krzywą przejścia. Ustawienia, picker i logowanie nie zostały zaprojektowane — tokeny trzeba na nie **rozszerzyć**, zachowując te same proporcje.

- [ ] **Krok 2: Dopisz tokeny i bazę**

W `:root` obok istniejących tokenów, oraz odpowiedniki w `:root[data-ton="light"]`:

```css
  --btn-bg:       rgba(255,255,255,.06);
  --btn-bg-hover: rgba(255,255,255,.12);
  --btn-edge:     var(--edge);
  --btn-ink:      var(--ink2);
  --btn-radius:   12px;
  --btn-ease:     .16s cubic-bezier(.2,.7,.3,1);
```

Wartości powyżej to punkt wyjścia z dzisiejszego `.przycisk` — podmień je na to, co wyszło z kroku 1.

```css
/* ── przyciski ────────────────────────────────────────────────────────────────
   Jedna baza, zamknięty zestaw wariantów. Stany toggle i tab jadą na
   atrybutach ARIA, nie na klasach: atrybut i tak musi być dla czytnika ekranu,
   a druga równoległa prawda w postaci klasy .wybrany się z nim rozjeżdża. */
.btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 8px;
  padding: 9px 14px;
  border: 1px solid var(--btn-edge);
  border-radius: var(--btn-radius);
  background: var(--btn-bg);
  color: var(--btn-ink);
  font: 500 13px/1 var(--font-ui);
  cursor: pointer;
  transition: background var(--btn-ease), color var(--btn-ease),
              border-color var(--btn-ease), transform var(--btn-ease);
}
.btn:hover { background: var(--btn-bg-hover); color: var(--ink); }
.btn:focus-visible { outline: 2px solid var(--akcent); outline-offset: 2px; }
.btn:disabled { opacity: .5; cursor: not-allowed; }
.btn:disabled:hover { background: var(--btn-bg); color: var(--btn-ink); }
.btn svg { width: 15px; height: 15px; flex: none; }

.btn--primary { background: var(--akcent); color: var(--akcent-ink); border-color: transparent; }
.btn--primary:hover { filter: brightness(1.06); background: var(--akcent); color: var(--akcent-ink); }

.btn--quiet { background: transparent; }

.btn--danger { border-color: var(--down-obwodka, rgba(229,72,77,.35)); }
.btn--danger:hover { background: rgba(229,72,77,.16); color: var(--down-tekst); }

.btn--icon { padding: 0; width: 34px; height: 34px; }
.btn--icon svg { width: 16px; height: 16px; }

.btn--tile {
  flex-direction: column; align-items: stretch; gap: 6px;
  padding: 10px; text-align: left;
}

.btn--menu {
  width: 100%; justify-content: space-between;
  border: 0; border-radius: 8px; background: transparent;
}

.btn--sm { padding: 6px 10px; font-size: 12px; }
.btn--sm.btn--icon { width: 28px; height: 28px; padding: 0; }
.btn--lg { padding: 12px 18px; font-size: 14px; }

.btn[aria-pressed="true"],
.btn[aria-selected="true"] { background: var(--btn-bg-hover); color: var(--ink); }
```

Jeśli `--akcent`, `--akcent-ink` albo `--font-ui` nie istnieją pod tymi nazwami, użyj tych, które są w pliku — nie dodawaj nowych.

- [ ] **Krok 3: Sprawdź, że nic się nie zepsuło**

Uruchom: `./x test && ./x podglad`
Oczekiwane: PASS, `bin/podglad/*.html` powstaje. Wygląd bez zmian — nic jeszcze nie używa nowych klas.

- [ ] **Krok 4: Zrób zrzuty „przed"**

```bash
cd /home/zatto/projects/Shy-Garlic
mkdir -p /tmp/shyg-przed
for p in index-grid-ciemny index-grid-jasny index-bento-ciemny \
         index-lista-ciemny index-lista-jasny index-detale-ciemny \
         index-picker-ciemny login-ciemny login-jasny; do
  google-chrome --headless --disable-gpu --force-prefers-reduced-motion \
    --hide-scrollbars --window-size=1440,900 \
    --blink-settings=primaryHoverType=2,availableHoverTypes=2 \
    --screenshot=/tmp/shyg-przed/$p.png "file://$PWD/bin/podglad/$p.html"
done
```

`--blink-settings` jest obowiązkowe: bez niego headless melduje ekran dotykowy, wchodzi `@media (hover: none)` i każda karta pokazuje akcje hoveru, więc porównanie jest bezwartościowe.

- [ ] **Krok 5: Commit**

```bash
git add internal/server/assets/style.css
git commit -m "feat(ui): baza .btn i warianty

Tylko dodaje. Stare selektory znikają osobnym commitem, żeby dało się
odrzucić jedno bez drugiego. Stany toggle na ARIA, nie na klasach.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Przepisanie trzynastu idiomów

**Pliki:**
- Modify: `internal/server/templates/index.html`, `internal/server/templates/login.html`, `internal/server/assets/app.js`, `internal/server/assets/style.css`
- Test: `internal/server/render_test.go`

**Interfejsy:**
- Konsumuje: klasy z Tasku 7.

Mapowanie, jeden do jednego:

| Stare | Nowe |
|---|---|
| `.przycisk` | `.btn` |
| `.przycisk.cichy`, `button.cichy` | `.btn .btn--quiet` |
| `.przycisk.grozna` | `.btn .btn--danger` |
| `.przycisk.maly` | `.btn .btn--sm` |
| `.ikonka` | `.btn .btn--icon` |
| `.ikonka.tekstowa` | `.btn .btn--icon` (napis zamiast SVG w środku) |
| `.grupa-akcja` | `.btn .btn--icon .btn--sm` |
| `.grupa-akcja.grozna` | `.btn .btn--icon .btn--sm .btn--danger` |
| `.karta-akcje > *` | `.btn .btn--icon .btn--sm` |
| `.edycja-akcje button` | `.btn .btn--icon .btn--sm` |
| `.wyloguj button` | `.btn .btn--quiet` |
| `.kafel-presetu`, `.kafel-tapety`, `.kafel-dodaj`, `.kafel-ikony` | `.btn .btn--tile` |
| `.menu-pozycja` | `.btn .btn--menu` |
| `.picker-podmien`, `.picker-domyslne`, `.picker-zapisz` | `.btn .btn--quiet .btn--sm` |
| `.tlo-usun.grozna` (`app.js:284`) | `.btn .btn--icon .btn--sm .btn--danger` |
| gołe `.grozna` (`app.js:1307`) | `.btn .btn--danger .btn--sm` |
| `<button type="submit">` (`login.html:53`) | `.btn .btn--primary .btn--lg` |

Klasy układu (`.karta-akcje`, `.edycja-akcje`, `.picker-siatka`, `.menu-panel`) **zostają** — opisują kontener, nie przycisk. Znika z nich tylko stylowanie samego przycisku.

Stany: `.wybrany` na `#ton-jasny` / `#ton-ciemny` / `.kafel-presetu` zastąp `aria-pressed`. Przełącznik widoku ma już `aria-selected` — zostaw i usuń dublującą klasę.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

Dopisz do `internal/server/render_test.go`:

```go
// Idiomy przycisku wracają same, jeśli ich nie pilnować. Test trzyma listę
// tego, co zostało skasowane — nawrót jest wtedy czerwonym testem, a nie
// uwagą w code review pół roku później.
func TestSzablonyBezSkasowanychIdiomów(t *testing.T) {
	zakazane := []string{
		`class="przycisk`, `class="ikonka`, `class="grupa-akcja`,
		`class="menu-pozycja`, `class="kafel-presetu`, `class="kafel-tapety`,
		`class="kafel-dodaj`, `class="picker-podmien`, `class="picker-domyslne`,
		`class="picker-zapisz`, `class="wybrany`,
	}
	for _, nazwa := range []string{"index.html", "login.html"} {
		treść, err := files.ReadFile("templates/" + nazwa)
		if err != nil {
			t.Fatalf("czytam %s: %v", nazwa, err)
		}
		for _, z := range zakazane {
			if bytes.Contains(treść, []byte(z)) {
				t.Errorf("%s: wrócił skasowany idiom %q", nazwa, z)
			}
		}
	}
}

// Każdy <button> w szablonie ma nieść .btn. Bezklasowy przycisk to dokładnie
// ten błąd, który siedział w login.html.
func TestKażdyPrzyciskMaBazę(t *testing.T) {
	re := regexp.MustCompile(`(?s)<button\b[^>]*>`)
	for _, nazwa := range []string{"index.html", "login.html"} {
		treść, _ := files.ReadFile("templates/" + nazwa)
		for _, tag := range re.FindAllString(string(treść), -1) {
			if !strings.Contains(tag, "btn") {
				t.Errorf("%s: przycisk bez klasy .btn: %s", nazwa, tag)
			}
		}
	}
}
```

Sprawdź importy `bytes`, `regexp`, `strings`. Jeśli zmienna z `embed.FS` nazywa się inaczej niż `files`, użyj tej z pliku.

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: FAIL, lista trafień na `class="przycisk`, `class="ikonka` i reszcie.

- [ ] **Krok 3: Przepisz szablony**

Przejdź `index.html` i `login.html` według tabeli. Przy akcjach grupy zamień znaki na SVG w stylu reszty panelu (`viewBox="0 0 24 24"`, `stroke="currentColor"`, `stroke-width="1.8"`, `fill="none"`): `+` na plus, `✎` na ołówek (ten sam co `#tryb-edycji`), `×` na krzyżyk.

- [ ] **Krok 4: Przepisz `app.js`**

Linie `284`, `1036`, `1307` — klasy według tabeli. Wszystkie `classList.toggle("wybrany", …)` zamień na `setAttribute("aria-pressed", String(…))`. Sprawdź, czy nic nie czyta `.wybrany` przez `querySelector`.

- [ ] **Krok 5: Usuń stare reguły ze `style.css`**

Skasuj reguły dla wszystkich selektorów z lewej kolumny tabeli. Zostaw klasy układu. Po skasowaniu upewnij się, że `.karta-akcje` dalej ustawia `opacity`/`pointer-events` dla hoveru — to zachowanie kontenera, nie przycisku.

- [ ] **Krok 6: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 7: Zrzuty „po" i porównanie**

```bash
./x podglad
mkdir -p /tmp/shyg-po
# ta sama pętla co w Tasku 7, katalog /tmp/shyg-po
```

Obejrzyj każdą parę przed/po. Różnice mają dotyczyć wyłącznie spójności hoveru, focusu i geometrii. Jeśli przycisk zniknął, zmienił rozmiar o więcej niż kilka pikseli albo stracił kolor — to regres, popraw przed commitem.

- [ ] **Krok 8: Commit**

```bash
git add internal/server/
git commit -m "refactor(ui): trzynaście idiomów przycisku na jedną bazę

Znikają style po potomku (.karta-akcje > *, .edycja-akcje button),
jednorazowe klasy pickera i bezklasowy submit w logowaniu. Stany toggle
przeniesione z klasy .wybrany na aria-pressed — atrybut i tak musiał
być, a dwie prawdy się rozjeżdżały. Akcje grupy dostają SVG zamiast
znaków + ✎ ×. Test pilnuje, żeby idiomy nie wróciły.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## FAZA 3 — powierzchnia konfiguracji

### Task 9: Ustawienia w zakładkach

**Pliki:**
- Modify: `internal/server/templates/index.html:422-475`, `internal/server/assets/app.js`, `internal/server/assets/style.css`, `internal/i18n/i18n.go`
- Test: `internal/server/render_test.go`

**Interfejsy:**
- Produkuje: `#okno-ustawien` z `role="tablist"` i siedmioma panelami o `id` `panel-wyglad`, `panel-uslugi`, `panel-powiadomienia`, `panel-konta`, `panel-historia`, `panel-plik`, `panel-instancja`.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

```go
func TestUstawieniaMająWszystkieZakładki(t *testing.T) {
	treść, _ := files.ReadFile("templates/index.html")
	for _, id := range []string{
		"panel-wyglad", "panel-uslugi", "panel-powiadomienia", "panel-konta",
		"panel-historia", "panel-plik", "panel-instancja",
	} {
		if !bytes.Contains(treść, []byte(`id="`+id+`"`)) {
			t.Errorf("brak panelu %q", id)
		}
	}
	if !bytes.Contains(treść, []byte(`role="tablist"`)) {
		t.Error("zakładki mają być tablistem, inaczej czytnik ekranu widzi rząd przycisków bez związku")
	}
}
```

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: FAIL — brak wszystkich siedmiu paneli.

- [ ] **Krok 3: Przebuduj okno**

Zamień zawartość `#okno-ustawien` na tablist plus siedem paneli. Istniejące sekcje przenieś bez zmiany logiki: konta → `panel-konta`, zmiana hasła → `panel-konta`, historia → `panel-historia`, surowy YAML → `panel-plik`. Powiadomienia rozbudujesz w Tasku 10, wygląd w Tasku 11, instancję w Tasku 12 — na razie mają być puste kontenery z nagłówkiem.

Zakładki: `<button class="btn btn--menu" role="tab" aria-selected="…" aria-controls="panel-…">`. Panele: `<section id="panel-…" role="tabpanel" hidden>`.

Napisy do i18n: `zakladka_wyglad`, `zakladka_uslugi`, `zakladka_powiadomienia`, `zakladka_konta`, `zakladka_historia`, `zakladka_plik`, `zakladka_instancja`.

- [ ] **Krok 4: Przełączanie w `app.js`**

```js
// Zakładki ustawień. Widoczność przez atrybut hidden, nie przez klasę —
// jedna prawda o tym, co widać, i ta sama, którą czyta czytnik ekranu.
function zakladki(korzen) {
  const taby = [...korzen.querySelectorAll('[role="tab"]')];
  const pokaz = (id) => {
    taby.forEach((t) => {
      const wybrany = t.getAttribute("aria-controls") === id;
      t.setAttribute("aria-selected", String(wybrany));
      korzen.querySelector("#" + t.getAttribute("aria-controls")).hidden = !wybrany;
    });
  };
  taby.forEach((t) => t.addEventListener("click", () => pokaz(t.getAttribute("aria-controls"))));
  if (taby.length) pokaz(taby[0].getAttribute("aria-controls"));
}
```

Wywołaj przy otwarciu okna ustawień.

- [ ] **Krok 5: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 6: Commit**

```bash
git add internal/server/ internal/i18n/
git commit -m "feat(ustawienia): siedem zakładek zamiast jednej długiej listy

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Zakładka Powiadomienia

**Pliki:**
- Modify: `internal/server/templates/index.html`, `internal/server/assets/app.js`, `internal/i18n/i18n.go`

**Interfejsy:**
- Konsumuje: `GET`/`PUT /api/notify` (Task 5).

- [ ] **Krok 1: Zbuduj panel**

W `#panel-powiadomienia`: pole `#webhook-adres` (`type="url"`, placeholder `https://…`), pole `#webhook-prog` (`type="number"`, `min="1"`, `max="10"`), przycisk `#webhook-zapisz` (`.btn .btn--primary`), przycisk `#powiad-test` (`.btn .btn--quiet`), akapit `#powiad-stan`.

Napisy do i18n: `webhook_adres`, `webhook_prog`, `webhook_zrodlo_srodowisko`, `webhook_zrodlo_brak`, `webhook_zapisany`.

- [ ] **Krok 2: Podepnij pod API**

```js
async function wczytajPowiadomienia() {
  const d = await api("GET", "/api/notify");
  const pole = $("#webhook-adres");
  pole.value = "";
  pole.placeholder = d.webhook || "https://…";
  // Adres ze środowiska jest własnością deployu — pole ma być martwe, a
  // użytkownik ma wiedzieć dlaczego, zamiast klikać w coś, co nie zadziała.
  const zeŚrodowiska = d.zrodlo === "srodowisko";
  pole.disabled = zeŚrodowiska;
  $("#webhook-zapisz").disabled = zeŚrodowiska;
  $("#powiad-stan").textContent = zeŚrodowiska
    ? tr("webhook_zrodlo_srodowisko")
    : d.wlaczony ? d.webhook : tr("webhook_zrodlo_brak");
  $("#webhook-prog").value = d.prog;
  $("#powiad-test").hidden = !d.wlaczony;
}
```

Zapis wysyła `PUT /api/notify` z `{webhook, prog}`, po sukcesie woła `wczytajPowiadomienia()` i pokazuje `tr("webhook_zapisany")`. Odpowiedź `409` pokaż jako komunikat błędu z ciała — serwer tłumaczy ją na język żądania.

- [ ] **Krok 3: Sprawdź w przeglądarce**

Uruchom: `./x test && ./x podglad`, zrób zrzut `index-*-ciemny.html`. Panel ma się renderować bez błędów w konsoli.

- [ ] **Krok 4: Commit**

```bash
git add internal/server/ internal/i18n/
git commit -m "feat(ustawienia): webhook ustawialny z panelu

Przycisk 'wyślij test' przestaje wisieć nad mechanizmem, którego z
panelu nie dało się skonfigurować.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Dwuwarstwowy picker i suwak przyciemnienia

**Pliki:**
- Modify: `internal/server/templates/index.html:120-186`, `internal/server/assets/app.js:50-320`, `internal/server/assets/style.css:340-350`, `internal/i18n/i18n.go`

**Interfejsy:**
- Konsumuje: `PUT /api/appearance` z polem `dim` (Task 4), `Background.Dim` (Task 3).

- [ ] **Krok 1: Podłącz `dim` do CSS**

W `style.css` zamień sztywny gradient `.tlo-przycmienie`:

```css
.tlo-przycmienie {
  background: linear-gradient(180deg,
    rgba(0,0,0,calc(var(--dim, .5) * .56)),
    rgba(0,0,0,var(--dim, .5)));
}
```

Odpowiednik dla `:root[data-preset="photo"][data-ton="light"]` przelicz na biel z tą samą proporcją.

W `app.js`, tam gdzie ustawiany jest `korzen.dataset.preset`, dopisz:

```js
korzen.style.setProperty("--dim", String((w.dim ?? 50) / 100));
```

- [ ] **Krok 2: Dodaj suwak**

W `index.html` obok `#suwak-rozmycia`, wewnątrz `.picker-tapeta`:

```html
<label class="picker-suwak">
  <span>{{.T.przyciemnienie}}</span>
  <input id="suwak-przyciemnienia" type="range" min="0" max="80" step="5" value="50"
         aria-label="{{.T.przyciemnienie}}">
  <span id="wartosc-przyciemnienia">50%</span>
</label>
```

i18n: `przyciemnienie` → `{EN: "Dimming", PL: "Przyciemnienie"}`.

- [ ] **Krok 3: Odsłoń wybór koloru tekstu**

W `app.js:169` zamień `wierszTonu.hidden = w.preset !== "photo"` na trwałe pokazanie przy własnej tapecie. Wiersz `#picker-ton` przenieś nad suwaki, żeby nie leżał na dnie panelu. Automatyczny pomiar luminancji przy wgraniu zostaje — ustawia wartość startową, nie blokuje zmiany.

- [ ] **Krok 4: Przebuduj przyciski pickera**

Zamiast `#picker-domyslne` i `#picker-zapisz`:

```html
<button class="btn btn--quiet btn--sm" id="picker-reset" type="button">{{.T.wyglad_przywroc_instancji}}</button>
<button class="btn btn--quiet btn--sm" id="picker-do-ustawien" type="button">{{.T.wyglad_dla_wszystkich}}</button>
```

i18n: `wyglad_przywroc_instancji` → `{EN: "Restore instance default", PL: "Przywróć domyślne instancji"}`, `wyglad_dla_wszystkich` → `{EN: "Set for everyone →", PL: "Ustaw dla wszystkich →"}`.

`#picker-reset` czyści klucz `localStorage` z wyglądem i przeładowuje wygląd z `window.WYGLAD`. `#picker-do-ustawien` zamyka picker, otwiera `#okno-ustawien` i przełącza na `panel-wyglad`.

! Picker od tej pory pisze **wyłącznie** do `localStorage`. Wszystkie wywołania `PUT /api/appearance` z pickera znikają — zostają tylko w `panel-wyglad`.

- [ ] **Krok 5: Wypełnij `panel-wyglad`**

Te same kontrolki co w pickerze (presety, tapeta, rozmycie, przyciemnienie, kolor tekstu, widok), ale zapisujące przez `PUT /api/appearance`. Nagłówek panelu mówi wprost, że to domyślne dla wszystkich; nagłówek pickera, że to tylko ta przeglądarka — napis `tlo_pamietane` już to robi.

- [ ] **Krok 6: Odpal testy i zrób zrzuty**

Uruchom: `./x test && ./x podglad`
Sprawdź na `index-picker-ciemny.html`: suwak przyciemnienia działa, wybór koloru tekstu widoczny, dwa nowe przyciski na miejscu.

- [ ] **Krok 7: Commit**

```bash
git add internal/server/ internal/i18n/
git commit -m "feat(wyglad): dwie warstwy zamiast trzech autorytetów

Picker pisze wyłącznie do localStorage, Ustawienia > Wygląd wyłącznie
do YAML-a, każdy mówi na kogo działa. Dochodzi suwak przyciemnienia:
na zdjęciu o dużym kontraście sam kolor tekstu czytelności nie ratuje,
bo część kafli leży na jasnym, a część na ciemnym tle.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: Zakładka Instancja

**Pliki:**
- Modify: `internal/server/templates/index.html`, `internal/server/assets/app.js`, `internal/i18n/i18n.go`

**Interfejsy:**
- Konsumuje: `GET /api/instance` (Task 6).

- [ ] **Krok 1: Zbuduj tabelę**

W `#panel-instancja`: akapit wyjaśniający, że to ustawienia startu i deployu, zmieniane flagą albo zmienną środowiskową, plus `<table id="tabela-instancji">` z kolumnami opcja / wartość / źródło.

i18n: `instancja_opis` → `{EN: "Set at startup with a flag or an environment variable. Shown here so you know where the panel got each value from.", PL: "Ustawiane przy starcie flagą albo zmienną środowiskową. Pokazane tutaj, żeby było wiadomo, skąd panel wziął każdą wartość."}`, plus `zrodlo_flaga`, `zrodlo_srodowisko`, `zrodlo_domyslna`.

- [ ] **Krok 2: Wypełnij z API**

```js
async function wczytajInstancje() {
  const d = await api("GET", "/api/instance");
  const tbody = $("#tabela-instancji tbody");
  tbody.replaceChildren();
  for (const o of d.opcje) {
    const tr_ = document.createElement("tr");
    for (const [tekst, klasa] of [
      [o.klucz, "instancja-klucz"],
      [o.wartosc || "—", "instancja-wartosc"],
      [tr("zrodlo_" + o.zrodlo), "instancja-zrodlo"],
    ]) {
      const td = document.createElement("td");
      td.className = klasa;
      td.textContent = tekst;
      tr_.append(td);
    }
    tbody.append(tr_);
  }
}
```

`textContent`, nie `innerHTML` — wartości pochodzą ze ścieżek plików i CIDR-ów podanych przez operatora.

- [ ] **Krok 3: Odpal testy**

Uruchom: `./x test && ./x podglad`
Oczekiwane: PASS, panel renderuje się bez błędów w konsoli.

- [ ] **Krok 4: Commit**

```bash
git add internal/server/ internal/i18n/
git commit -m "feat(ustawienia): zakładka instancji z podaniem źródła wartości

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: Zepnięcie przełącznika `discovery.docker`

**Pliki:**
- Modify: `internal/server/api.go` (`handleDiscover`, `handleInfo`), `internal/server/templates/index.html`, `internal/server/assets/app.js`
- Test: `internal/server/notify_api_test.go`

**Interfejsy:**
- Konsumuje: `config.Discovery.Docker *bool` (Task 3).

Decyzja: `docker: false` **chowa** pozycję „Znajdź usługi" z menu i każe `/api/discover` odpowiadać `403`. Wyszarzenie byłoby gorsze — user wyłączył funkcję świadomie, więc przypominanie mu o niej przy każdym otwarciu menu jest szumem. Brak klucza znaczy „jak dotąd": podpowiadaj, jeśli gniazdo jest dostępne.

- [ ] **Krok 1: Napisz test, który nie przechodzi**

```go
func TestDiscoverWyłączonyZwraca403(t *testing.T) {
	s := serwerTestowy(t)
	nie := false
	s.mu.Lock()
	s.cfg.Discovery.Docker = &nie
	s.mu.Unlock()

	rr := httptest.NewRecorder()
	s.handleDiscover(rr, httptest.NewRequest("GET", "/api/discover", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("chcę 403 przy wyłączonym podpowiadaniu, mam %d", rr.Code)
	}
}

// Brak klucza to nie to samo co false — ma się zachować jak dotąd.
func TestDiscoverBezKluczaDziała(t *testing.T) {
	s := serwerTestowy(t)
	rr := httptest.NewRecorder()
	s.handleDiscover(rr, httptest.NewRequest("GET", "/api/discover", nil))
	if rr.Code == http.StatusForbidden {
		t.Fatal("bez klucza podpowiadanie ma zostać włączone")
	}
}
```

- [ ] **Krok 2: Odpal i zobacz, że pada**

Uruchom: `./x test`
Oczekiwane: FAIL — pierwszy test dostaje `200` zamiast `403`.

- [ ] **Krok 3: Dodaj bramkę**

W `internal/server/api.go`:

```go
// dockerWłączony — brak klucza znaczy „jak dotąd", czyli podpowiadaj, jeśli
// gniazdo jest. Dopiero jawne false wyłącza.
func (s *Server) dockerWłączony() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil || s.cfg.Discovery.Docker == nil {
		return true
	}
	return *s.cfg.Discovery.Docker
}
```

Na początku `handleDiscover`:

```go
	if !s.dockerWłączony() {
		jsonBłąd(w, http.StatusForbidden, i18n.T(i18n.Wybierz(r), "docker_wylaczony"))
		return
	}
```

W `handleInfo` zeruj licznik kandydatów, gdy `!s.dockerWłączony()` — inaczej menu pokazałoby plakietkę z liczbą przy schowanej pozycji.

i18n: `docker_wylaczony` → `{EN: "Suggesting services from containers is turned off in the config file.", PL: "Podpowiadanie usług z kontenerów jest wyłączone w pliku konfiguracyjnym."}`.

- [ ] **Krok 4: Schowaj pozycję w menu**

W `app.js`, tam gdzie ustawiana jest plakietka `#menu-docker-badge`, dopisz chowanie całej pozycji `#menu-docker`, gdy `/api/info` zwróci `dockerWylaczony: true`. Dodaj to pole w `handleInfo`.

- [ ] **Krok 5: Odpal testy**

Uruchom: `./x test`
Oczekiwane: PASS.

- [ ] **Krok 6: Commit**

```bash
git add internal/server/ internal/i18n/
git commit -m "feat(config): przełącznik podpowiadania usług z kontenerów

docker: false chowa pozycję z menu i zamyka /api/discover. Wyszarzenie
byłoby szumem — user wyłączył funkcję świadomie. Brak klucza zachowuje
się jak dotąd.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 14: Dokumentacja i zrzuty

**Pliki:**
- Modify: `README.md`, `README.pl.md`, `examples/homelab.yaml`, `docs/img/`

- [ ] **Krok 1: Opisz warstwy w README**

W obu README dodaj sekcję o czterech warstwach: co jest flagą, co w pliku, co w bazie, co w przeglądarce — i dlaczego webhook nie jest w pliku. To jest pytanie, które zada każdy, kto zobaczy panel po raz pierwszy.

- [ ] **Krok 2: Rozszerz przykład**

W `examples/homelab.yaml` dopisz zakomentowane `notify.threshold`, `discovery.docker` i `background.dim` z jednozdaniowym opisem każdego.

- [ ] **Krok 3: Odśwież zrzuty**

```bash
./x podglad
# pętla jak w Tasku 7, wynik do docs/img/
```

Podmień zrzuty, na których widać przyciski i ustawienia.

- [ ] **Krok 4: Odpal pełną weryfikację**

```bash
./x test && ./x vet && ./x build
```
Oczekiwane: wszystko zielone, `bin/shyg` się buduje.

- [ ] **Krok 5: Commit**

```bash
git add README.md README.pl.md examples/ docs/
git commit -m "docs: cztery warstwy konfiguracji i odświeżone zrzuty

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Samokontrola planu

**Pokrycie specu.** Cztery warstwy → Taski 1, 3, 5, 6, 11. Precedencja webhooka → Task 5. Precedencja wyglądu → Task 11. `dim` → Taski 3, 4, 11. Kolor tekstu odsłonięty → Task 11. `discovery.docker` → Taski 3, 13. Trzynaście idiomów → Taski 7, 8. Zakładki → Task 9. Instancja → Taski 6, 12. Kontrakt API → Taski 4, 5, 6. Weryfikacja → Taski 8, 14. Faza 0 (ADR) leży poza tym repozytorium i poza tym planem.

**Spójność nazw.** `Ustawienie`/`UstawUstawienie` (Task 1) używane w Taskach 5, 6. `UstawWebhook`/`Webhook`/`UstawPróg`/`Próg` (Task 2) w Task 5. `Notify.Threshold`, `Background.Dim`, `Discovery.Docker` (Task 3) w Taskach 4, 5, 11, 13. `OpisOpcji` (Task 6) w Task 12. Klasy `.btn*` (Task 7) w Taskach 8–13.

**Kolejność jest wiążąca w trzech miejscach.** Task 5 nie ruszy bez Tasków 1 i 2 (baza i zmienny webhook). Task 8 nie ruszy bez Tasku 7 (kasuje stare klasy, więc nowe muszą już istnieć). Taski 9–13 zakładają bazę `.btn`, bo piszą nowy HTML. Poza tym zadania w obrębie fazy są niezależne.
