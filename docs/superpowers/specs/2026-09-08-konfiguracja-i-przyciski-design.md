# Przebudowa powierzchni konfiguracji i systemu przycisków

Data: 2026-09-08
Punkt wyjścia: `6269cf4` (zielony baseline, `./x test` przechodzi)

## Problem

Dwa niezależne rozjazdy, które spotykają się w tych samych plikach.

**Konfiguracja jest rozsypana po siedmiu miejscach** i nic nie mówi, które z nich
wygrywa. Jedenaście opcji istnieje wyłącznie jako flagi CLI i nie ma po nich
śladu w UI — najostrzej widać to przy powiadomieniach, gdzie panel oferuje
przycisk „wyślij test" do mechanizmu, którego z panelu nie da się skonfigurować.
Blok `appearance` ma trzy niezależne drogi zapisu (picker tła, surowy edytor
YAML-a, `localStorage`) i żadna nie mówi użytkownikowi, na kogo jej zmiana
zadziała.

**Przyciski mają trzynaście równoległych idiomów** na 13 tysięcy linii. Część to
klasy (`.przycisk`, `.ikonka`, `.grupa-akcja`), część to style po potomku
(`.karta-akcje > *`, `.edycja-akcje button`), część to jednorazowe klasy nadane
pojedynczym przyciskom (`.picker-podmien`, `.picker-domyslne`, `.picker-zapisz`
— trzy klasy na trzy przyciski w jednym panelu), a część to gołe elementy bez
klasy (`login.html:53`). Akcje grupy używają znaków `+ ✎ ×` jako tekstu, gdy
cała reszta panelu rysuje SVG.

## Model konfiguracji: cztery warstwy

Granica przebiega tam, gdzie przebiega prawdziwa różnica — nie „ważne vs mniej
ważne", tylko **kiedy wartość musi być znana** i **czy wolno jej trafić do
gita**.

| Warstwa | Zawartość | Uzasadnienie granicy | Widok w UI |
|---|---|---|---|
| CLI / env | `config`, `addr`, `data`, `cache`, `backgrounds`, `icons`, `docker` (ścieżka gniazda), `trusted-proxy`, `debug` | musi być znane zanim wstanie serwer, albo jest własnością deployu (wolumen musi istnieć), albo decyduje o zaufaniu do proxy | read-only, zakładka **Instancja** |
| YAML | `title`, `appearance{}`, `notify{threshold}`, `discovery{docker: bool}`, `groups[]` | treść produktu: jawna, czytelna dla człowieka, bezpieczna w commicie | pełna edycja |
| SQLite | `notify.webhook` (obok istniejących kont, sesji, historii) | **poświadczenie** — plik configu jest commitowany, więc sekret w nim wyciekłby przy pierwszym `git push` | edycja, wartość maskowana |
| localStorage | nadpisanie wyglądu | dotyczy jednej przeglądarki i nikogo więcej | picker, opisany wprost |

### Dlaczego webhook nie może iść do YAML-a

`internal/notify/notify.go:3` stwierdza to wprost i ma rację: config jest
pokazywany w UI i commitowany przez `internal/gitrepo`, a URL webhooka Discorda
jest poświadczeniem — kto go ma, ten pisze na kanał. Przeniesienie go do YAML-a
wstawiłoby sekret do historii repozytorium.

Baza SQLite nie jest commitowana i już dziś trzyma hasła (bcrypt) oraz sesje,
więc jest właściwym miejscem. Zysk dla użytkownika jest realny: webhook staje
się ustawialny z panelu, a przycisk „wyślij test" przestaje wisieć nad
mechanizmem bez konfiguracji.

### Dlaczego `docker` rozszczepia się na dwie warstwy

Ścieżka gniazda zostaje w CLI, bo bez pasującego wolumenu zmiana z UI i tak nic
nie da — to własność deployu, nie produktu. Do YAML-a idzie sam przełącznik
`discovery.docker` („podpowiadaj usługi z kontenerów"), bo *czy* podpowiadać to
decyzja użytkownika, a *skąd* to decyzja tego, kto stawia kontener.

### Dlaczego `trusted-proxy` zostaje wyłącznie w CLI

Lista CIDR-ów rozstrzyga, komu wolno ustawiać `X-Forwarded-For`. Gdyby dało się
ją zmienić z panelu, każdy z dostępem do panelu mógłby rozszerzyć zaufanie do
nagłówków i podszyć się pod dowolny adres w logach i w limitach logowania.
Ustawienie o takim skutku nie może być klikalne.

## Precedencja

Dwie reguły, obie pokazane w interfejsie w miejscu, którego dotyczą.

**Webhook:** `--webhook` / `SHYG_WEBHOOK` **>** SQLite. Gdy wartość przyszła ze
środowiska, pole w UI jest nieedytowalne z podpisem „ustawione zmienną
środowiskową". Dzięki temu deploy dalej może wstrzyknąć sekret, a UI go nie
nadpisze — i użytkownik wie, dlaczego nie może tego kliknąć.

**Wygląd:** `localStorage` **>** YAML. Picker zapisuje wyłącznie do
`localStorage`, Ustawienia > Wygląd wyłącznie do YAML-a. Żadna z tych ścieżek nie
pisze tam, gdzie pisze druga.

Migracja plików jest zbędna. Brakujące klucze `notify` i `discovery` schodzą do
wartości domyślnych przy `Load()`, a zapis jest splice'em po offsetach — plików,
których nikt nie edytował, nic nie przepisuje.

## Wygląd: dwie warstwy zamiast trzech autorytetów

Picker w headerze zostaje jako podgląd na żywo działający na moją przeglądarkę.
Dostaje dwa jednoznaczne przyciski:

- **„Przywróć domyślne instancji"** — czyści `localStorage`, wraca do tego, co w
  YAML-u.
- **„Ustaw dla wszystkich →"** — otwiera Ustawienia > Wygląd, czyli edytor YAML-a.

Dzisiejsze `picker-zapisz` i `picker-domyslne` znikają razem ze swoją
dwuznacznością (żaden z nich nie mówił, której warstwy dotyczy).

### Czytelność na własnej tapecie

Wybór koloru tekstu już istnieje: `#picker-ton` daje „Jasny tekst" / „Ciemny
tekst", a przy wgraniu obrazu przeglądarka mierzy jego luminancję i ustawia ton
sama (serwer nigdy nie dekoduje obrazów, więc luminancję ma skąd wziąć tylko
przeglądarka). Problem jest gdzie indziej:

1. Wiersz wyboru jest `hidden` dla wszystkiego poza własną tapetą i leży na dnie
   pickera — gdy automat strzeli źle, użytkownik tego nie znajduje.
2. Warstwa przyciemniająca `.tlo-przycmienie` ma gradient wpisany na sztywno
   (`style.css:342`, `rgba(0,0,0,.28)` → `rgba(0,0,0,.5)`).

Punkt drugi jest istotniejszy. Na zdjęciu o dużym kontraście wewnętrznym — jasne
niebo u góry, ciemny las u dołu — żaden kolor tekstu nie ratuje czytelności, bo
część kafli leży na jasnym tle, a część na ciemnym. Ratuje dopiero mocniejsze
przyciemnienie.

Dlatego dochodzi **suwak przyciemnienia** obok istniejącego suwaka rozmycia, a
wybór koloru tekstu przestaje być chowany przy własnej tapecie. Pomiar
luminancji zostaje jako punkt startowy, nie jako wyrok.

Pole: `appearance.background.dim`, liczba całkowita 0–80, znaczy krycie dolnego
końca gradientu w procentach. Górny koniec to zawsze `dim × 0,56` — proporcja
wzięta z dzisiejszych wartości (`.28 / .5`), żeby przyciemnienie dalej narastało
ku dołowi, gdzie stoją kafle. `dim: 50` odtwarza dokładnie dzisiejszy wygląd i
jest wartością domyślną, więc plik bez tego klucza wygląda tak jak przed
zmianą. `dim: 0` wyłącza warstwę.

## System przycisków

Jedna baza i zamknięty zestaw wariantów:

```
.btn                            baza: geometria, focus-ring, przejścia, disabled
.btn--primary                   akcja główna
.btn--quiet                     dzisiejsze .cichy
.btn--danger                    dzisiejsze .grozna
.btn--icon                      dzisiejsze .ikonka, .karta-akcje > *, .grupa-akcja
.btn--tile                      dzisiejsze .kafel-presetu / -tapety / -dodaj / -ikony
.btn--menu                      dzisiejsze .menu-pozycja
.btn--sm | .btn--lg             rozmiary (dzisiejsze .maly)
[aria-pressed] | [aria-selected]  stany toggle i tab — bez osobnych klas
```

Stany toggle i tab jadą na atrybutach ARIA zamiast na klasach, bo atrybut i tak
musi być ustawiony dla czytników ekranu — druga, równoległa prawda w postaci
klasy `.wybrany` może się z nim rozjechać i dziś się rozjeżdża.

Do usunięcia, trzynaście idiomów:

| Idiom | Gdzie |
|---|---|
| `.grupa-akcja` | `style.css:745`, `index.html` |
| `.picker-podmien`, `.picker-domyslne`, `.picker-zapisz` | picker tła |
| `.karta-akcje > *` | `style.css:861` — styl po potomku |
| `.edycja-akcje button` | `style.css:1185` — styl po potomku |
| `.wyloguj button` | `index.html:274` |
| `button.cichy` bez `.przycisk` | `style.css:1109` |
| `.tlo-usun` | `app.js:284` |
| gołe `.grozna` na przycisku | `app.js:1307` |
| `.kafel-ikony` | `app.js:1036` |
| bezklasowy `<button type="submit">` | `login.html:53` |
| znaki `+ ✎ ×` jako tekst | akcje grupy w `index.html` |

Tokeny wizualne wychodzą z canvasu `Shy-Garlic-Glass.dc.html` do `:root`
(`--btn-bg`, `--btn-edge`, `--btn-ink`, `--btn-glow`, promienie, czasy przejść).

! Canvas narysował wyłącznie dwa ekrany — `Homelab panel` i `Service details` — a
styl trzyma w atrybutach `style`, nie w systemie klas. Ustawienia, picker, tryb
edycji i logowanie nie zostały w nim zaprojektowane. Tokeny trzeba więc z niego
**wyprowadzić i rozszerzyć** na powierzchnie, których nigdy nie pokrył. To praca
projektowa, nie przepisanie.

## Struktura UI

```
TOOLBAR:  [grid | bento | lista]   [+ Usługa]   [✎ edycja]   [☀ tło]   [⚙]
                                                               │
                              picker tła: podgląd na żywo, moja przeglądarka
                              ├ presety · tapeta · rozmycie · przyciemnienie · kolor tekstu
                              ├ „Przywróć domyślne instancji"
                              └ „Ustaw dla wszystkich →" ──→ ⚙ > Wygląd

⚙ USTAWIENIA (zakładki)
   Wygląd       — domyślne dla całej instancji (zapis do YAML-a)
   Usługi       — grupy i usługi
   Powiadomienia— webhook (maskowany), próg awarii, wyślij test
   Konta        — lista kont, dodanie, zmiana hasła
   Historia     — wersje configu, przywracanie
   Plik         — surowy YAML
   Instancja    — flagi CLI/env, read-only, ze wskazaniem źródła każdej wartości
```

Zakładka **Instancja** pokazuje dla każdej opcji trzy rzeczy: wartość, źródło
(flaga / zmienna środowiskowa / domyślna) i jednozdaniowy opis. To zamyka dziurę
„nie wiem, skąd panel to wziął", nie otwierając możliwości zmiany rzeczy, które
zmieniać się z panelu nie powinny.

## Kontrakt API

| Trasa | Zmiana |
|---|---|
| `GET /api/instance` | nowa — flagi CLI/env, wartość + źródło, wyłącznie odczyt |
| `GET /api/notify` | nowa — stan powiadomień: czy włączone, webhook zamaskowany, źródło, próg |
| `PUT /api/notify` | nowa — zapis webhooka do SQLite i progu do YAML-a; przy webhooku ze środowiska zwraca `409` z powodem |
| `PUT /api/appearance` | rozszerzona o `dim` |
| `GET`/`PUT /api/config` | bez zmian |

`PUT /api/notify` pisze do dwóch warstw naraz (sekret do bazy, próg do YAML-a) i
jest jedynym miejscem, gdzie taki podział przecieka do API. Alternatywa — dwie
trasy — zmuszałaby UI do dwóch zapisów przy jednym kliknięciu „Zapisz" i do
obsługi połowicznego niepowodzenia.

## Weryfikacja

- `render_test.go` dostaje asercję, że żaden szablon nie zawiera skasowanych
  klas. Nawrót idiomu wraca wtedy jako czerwony test, a nie jako uwaga w review.
- Test precedencji webhooka: środowisko ustawione → baza ignorowana, `PUT`
  odrzucony z `409`; środowisko puste → baza rządzi.
- Test precedencji wyglądu: `localStorage` nadpisuje YAML, „Przywróć domyślne
  instancji" czyści nadpisanie.
- `./x podglad` plus headless Chrome, zrzuty przed/po: grid × bento × lista ×
  (ciemny, jasny), login, ustawienia, picker. Przy zrzutach obowiązuje
  `--blink-settings=primaryHoverType=2,availableHoverTypes=2`, inaczej headless
  melduje ekran dotykowy, wchodzi `@media (hover: none)` i każda karta pokazuje
  akcje hoveru.
- Go wykonuje się wyłącznie przez `./x`, czyli na CT101. Nigdy na CT111.

## Fazy

| Faza | Zakres | Obsada |
|---|---|---|
| 0 | ADR na czterowarstwowy model konfiguracji — ląduje w `AI-Brain/Decisions/`, nie w tym repozytorium | atlas |
| 1 | Backend: schemat YAML, webhook w SQLite, precedencja, `/api/instance`, `/api/notify` | borys → rena |
| 2 | System `.btn`, tokeny Glass, przepisanie trzynastu idiomów | pixel → borys → teo |
| 3 | Ustawienia z zakładkami, dwuwarstwowy picker, suwak przyciemnienia, zakładka Instancja | pixel → borys → teo → rena |

Faza 2 wyprzedza fazę 3, bo faza 3 pisze dużo nowego HTML-a. Odwrotna kolejność
znaczyłaby napisanie go w starych idiomach i przepisanie tydzień później.

## Poza zakresem

- Zmiana ścieżki modułu Go z `git.zatto.pl/Software/Shy-Garlic` na
  `github.com/zatto-dev/shy-garlic`. Konieczna przed otwarciem repozytorium
  (bez niej `go install` nie zadziała), ale niezależna od tej przebudowy.
- Rozbudowa powiadomień w stronę alertingu. `notify.go:7` odrzuca to świadomie:
  Uptime Kuma robi to lepiej i nie zamierzamy go zastępować.
- Powiadamianie o nowej wersji. Odrzucone przez CEO 2026-09-07 — cichy ruch na
  zewnątrz podważa rdzeń obietnicy produktu.
