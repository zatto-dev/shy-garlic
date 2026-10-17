<p align="center">
  <img src="docs/img/banner.png" alt="Shy Garlic" width="100%">
</p>

<p align="center">
  <b>Panel do homelabu, w którym plik konfiguracyjny i interfejs to jedno.</b><br>
  Klikasz w UI — zmienia się YAML. Edytujesz YAML — zmienia się UI.<br>
  I nikt po drodze nie kasuje Twoich komentarzy.
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.pl.md">Polski</a>
</p>

<p align="center">
  <a href="https://github.com/zatto-dev/shy-garlic/actions/workflows/ci.yml"><img src="https://github.com/zatto-dev/shy-garlic/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/zatto-dev/shy-garlic/releases/latest"><img src="https://img.shields.io/github/v/release/zatto-dev/shy-garlic" alt="Najnowsze wydanie"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/licencja-AGPL--3.0--or--later-blue" alt="Licencja: AGPL-3.0-or-later"></a>
</p>

<p align="center">
  <img src="docs/img/panel-dark.png" alt="Panel Shy Garlic — motyw ciemny" width="100%">
</p>

---

Shy Garlic to strona startowa homelabu: Twoje usługi w grupach, każda z
heartbeatem, który mówi, czy naprawdę działa. Cała instancja to jeden plik YAML,
który możesz trzymać w gicie — a panel jest pełnoprawnym edytorem tego samego
pliku.

- **Jedna statyczna binarka Go**, 14 MB, bez cgo. SQLite przez
  `modernc.org/sqlite`. Obraz kontenera ma około 16 MB.
- **Zero node w produkcie.** `html/template`, waniliowy JavaScript i SSE — żadnego
  kroku budowania frontendu i ani jednego zapytania z przeglądarki do CDN-a.
- **Zapis z UI jest round-trip-safe.** Komentarze, kotwice, wyrównanie i puste
  linie wychodzą z zapisu dokładnie takie, jakie do niego weszły.

## Dlaczego jeszcze jeden panel

Rynek podzielił się wzdłuż jednej osi i nikt nie przeszedł na drugą stronę:

|             | Config w pliku (git, diff) | Edycja w UI (klik, telefon)     |
| ----------- | -------------------------- | ------------------------------- |
| **Lekki**   | Homepage, Glance, Homer    | **← tu jesteśmy**               |
| **Ciężki**  | Dashy                      | Homarr, Heimdall, Flame, Fenrus |

Albo masz plik, który wersjonujesz w gicie, ale każda zmiana to wejście przez
SSH. Albo masz wygodne UI, ale konfiguracja siedzi w bazie, której nie
zdiffujesz, nie przejrzysz i nie skopiujesz na drugą maszynę.

**Shy Garlic robi jedno i drugie nad tym samym plikiem.** Zmiana zrobiona klikiem
zapisuje się jako splice po offsetach bajtowych w oryginalnym tekście — nie jako
ponowna serializacja sparsowanego dokumentu. I na tym polega cała różnica:
komentarze, puste linie, kotwice (`&defaults`), merge-keye (`<<: *defaults`),
styl cytowania i wyrównanie komentarzy końca linii zostają dokładnie tam, gdzie
były.

Dostatecznie często uchodzi to za niewykonalne, żeby powiedzieć to wprost. Home
Assistant zamknął zgłoszenie o zachowywanie komentarzy przy zapisie
(`frontend#6486`) słowami *„parsers are not comment aware, this has been brought
up many times, but not fixable"*. Da się to naprawić. Ten projekt jest dowodem.

Poniżej diff z prawdziwego pliku po dodaniu usługi klikiem w UI:

```diff
       - name: "Sonarr"          # cudzysłowy celowo, nie zdejmuj
-        url: http://sonarr.home.arpa:8989
+        url: http://sonarr.home.arpa:8990
         icon: sonarr
         <<: *defaults
@@
           expect: [200]
+      - name: AdGuard
+        url: http://adguard.home.arpa:3000
+        icon: adguard-home
 
 # Stopka: nic tu nie ma, ale komentarz ma zostać.
```

Jedna linia zmieniona, trzy dodane, zero szkód ubocznych.

## Widzisz zmianę, zanim się wydarzy

Edytor cudzego pliku działa tylko wtedy, gdy ktoś mu ufa. Dlatego panel nigdy nie
zapisuje niczego, czego wcześniej nie zobaczysz: każda akcja — dodanie usługi,
zmiana nazwy grupy, edycja surowego YAML-a, zmiana wyglądu instancji — pokazuje
dokładny diff i czeka na potwierdzenie. Wyjątek to dwa szybkie gesty: zmiana
tytułu w miejscu i przeciąganie w nową kolejność działają od razu, bo okno
potwierdzenia po każdym upuszczeniu czyniłoby je bezużytecznymi. Idą tym samym
silnikiem, trafiają do historii wersji i są o jedno kliknięcie od cofnięcia.

Pod spodem są jeszcze dwa zabezpieczenia:

- **Historia wersji.** Każdy zapis jest zachowany razem z diffem, autorem i
  czasem. Powrót do stanu sprzed godziny to jedno kliknięcie, a samo przywrócenie
  też trafia do historii, więc można cofnąć cofnięcie.
- **Commity w gicie.** Jeśli config leży w repozytorium, każdy zapis z UI staje
  się commitem: `shyg: dodano usługę "Radarr" do grupy "Infrastructure"`. Klikasz
  w przeglądarce, a historia jest w `git log`. Nie ma gita albo repo — nic się
  nie dzieje i nie ma błędu. Domyślny obraz jest distroless, więc nie ma w nim
  `gita`; kto chce commitów w kontenerze, buduje `--target git` (wariant na
  Alpine) albo uruchamia binarkę wprost na hoście z gitem.

## Edytor, który nie potrzebuje node

<p align="center">
  <img src="docs/img/edytor-pliku.png" alt="Edytor configu z kolorowaniem składni YAML" width="100%">
</p>

Zakładka z plikiem w ustawieniach to prawdziwy edytor: kolorowanie składni YAML —
komentarze, klucze, liczby, słowa kluczowe i kotwice, każde w swoim kolorze, w
obu motywach — oraz tryb pełnoekranowy na dłuższe pliki. Kolorowanie jest
napisane od zera, bo alternatywą był krok budowania albo CDN, a tego projekt nie
ma ani jednego, ani drugiego. Pole zostaje zwykłym `<textarea>`, więc cofanie,
wklejanie i zaznaczanie działają tak, jak chce przeglądarka.

Zapis stąd przechodzi przez ten sam podgląd diffa, tę samą historię wersji i ten
sam commit w gicie, co zmiana zrobiona klikiem.

## Heartbeat, który nie kłamie

Sprawdzanie idzie **po stronie serwera**, po HTTP albo TCP, i leci nawet wtedy,
gdy nikt nie ma otwartej karty w przeglądarce.

- **Lista akceptowanych kodów jest jawna** i domyślnie zawiera
  `200, 301, 302, 401, 403` — bo `401` i `403` znaczą „usługa żyje i chce, żebyś
  się zalogował", a nie „usługa padła".
- **„Nie da się połączyć" to inny stan niż „serwer odpowiedział czymś dziwnym".**
  Pierwsze jest czerwone, drugie pomarańczowe.
- **Stany są rozróżnialne kształtem, nie tylko kolorem** — koło, romb, kwadrat.
  Dla daltonistów sam kolor to za mało.
- `insecure: true` dla samopodpisanych certyfikatów, bo w homelabie stoją
  wszędzie, a Proxmox nie jest martwy tylko dlatego, że nikt nie kupił mu certu.
- **Strona, która odpowiada 200, nadal może być zła.** `check.contains` i
  `check.not_contains` zaglądają do odpowiedzi (pierwszy 1 MiB), a `check.path`
  pyta endpoint zdrowia zamiast strony głównej. Gdy kod jest dobry, a treść nie
  — domyślna strona nginxa zamiast aplikacji, baner „prace serwisowe" — usługa
  robi się **pomarańczowa (`degraded`)**, z powodem w oknie szczegółów: serwer
  żyje, tylko nie mówi tego, co trzeba.

## Każda usługa ma swoją historię

<p align="center">
  <img src="docs/img/detale.png" alt="Szczegóły usługi z dostępnością i czasami odpowiedzi" width="100%">
</p>

Kliknięcie w wykres na karcie otwiera szczegóły: dostępność z 24 godzin i 7 dni,
mediana i p95 czasu odpowiedzi, najwolniejsze sprawdzenie, wykres opóźnień oraz
dziennik momentów, w których usługa naprawdę zmieniła stan, razem z powodem.

## Ikony bez donoszenia na Ciebie

Typowe podejście wstawia w HTML `<img src="https://cdn.jsdelivr.net/...">`. To
znaczy, że **przeglądarka użytkownika odpytuje obcy CDN o nazwy usług, które ktoś
ma w domu** — z Twojego IP wychodzi żądanie o `vaultwarden.svg` i `proxmox.svg`.
Panel bez internetu traci przy okazji wszystkie ikony.

Tutaj przeglądarka pyta wyłącznie Twój panel, o `/icon/<nazwa>`, i o nic więcej.
Ikonę dociąga **serwer**, raz, i cache'uje ją na dysku.

Żeby było dokładnie, bo twierdzenia o prywatności zasługują na precyzję: CDN
nigdy nie widzi Twojej przeglądarki, nie dowiaduje się, ile osób korzysta z
panelu, i po pierwszym pobraniu nie dostaje już nic. Widzi natomiast, raz,
serwer proszący o plik. Jeśli i to jest za dużo — `--icons icons.zip` wozi
komplet w jednym pliku i sprowadza liczbę zapytań na zewnątrz do zera.

Katalog ma około 4200 pozycji, wyszukiwanych po nazwie i po aliasie.

## Umie zajrzeć do Twoich kontenerów

Panel potrafi zaproponować gotowe wpisy na podstawie kontenerów działających na
hoście — ze zgadniętą nazwą, zgadniętą ikoną i adresem zbudowanym z wystawionego
portu. Zaznaczasz, co ma trafić na panel, a cała paczka przechodzi przez ten sam
podgląd diffa, co każda inna zmiana.

Wskaż go na socket-proxy z wyłączonym `POST`, a nie na gołe gniazdo Dockera.
Zamontowanie samego gniazda — nawet tylko do odczytu — oddaje w praktyce władzę
nad Dockerem hosta każdemu, kto przejmie panel.

Kontener może też opisać się sam, co bije każde zgadywanie panelu. Etykiety
czytane są z prefiksem `shyg.`:

```yaml
labels:
  shyg.name: Jellyfin
  shyg.url: https://media.home.arpa
  shyg.icon: jellyfin
  shyg.group: Media
  shyg.enable: "false"   # ten ma nie trafiać do podpowiedzi
```

## Kiedy coś padnie

Ustaw webhooka — Discord, Slack, ntfy, Gotify, Telegram albo dowolny adres
przyjmujący JSON — i dostajesz jedną krótką wiadomość, gdy usługa padnie, i
jedną, gdy wróci. Nic poza tym.

To świadomie **nie jest** alerting: bez eskalacji, harmonogramów, reguł i okien
ciszy. Kto chce prawdziwego alertingu, niech postawi Uptime Kumę — nie zamierzamy
jej zastępować.

Dwa szczegóły, które mają znaczenie w praktyce:

- **Awaria musi się potwierdzić.** Domyślnie trzeba dwóch nieudanych sprawdzeń z
  rzędu, żeby cokolwiek wyszło — jedno mrugnięcie sieci nie obudzi nikogo w nocy.
  Powrót zgłaszany jest od razu. Liczbę zmienia `notify.threshold`.
- **Adres webhooka mieszka w bazie, nie w pliku konfiguracyjnym.** Plik jest
  pokazywany w panelu i commitowany do gita, a URL webhooka jest poświadczeniem.
  Ustawisz go w Ustawienia → Powiadomienia albo zmienną `SHYG_WEBHOOK`, jeśli
  deploy zarządza swoimi sekretami sam — wartość ze środowiska wygrywa, żeby
  restart kontenera nie cofał po cichu zmiany zrobionej w panelu.
- **Wiadomości idą w tle**, po jednej kolejce na usługę, więc wolny albo martwy
  webhook nigdy nie wstrzymuje historii sprawdzeń. Wiadomość, której nie udało się
  dostarczyć (trzy próby), nie jest uznana za zgłoszoną i wychodzi ponownie przy
  następnym sprawdzeniu.

### Formaty wiadomości

Format wybierasz w Ustawienia → Powiadomienia albo zmienną `SHYG_WEBHOOK_FORMAT` /
flagą `--webhook-format` (środowisko wygrywa z panelem, dokładnie jak adres).
Format leży w bazie obok adresu.

| Format     | Adres, który wpisujesz                                           | Co wychodzi                                                                  |
| ---------- | ---------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `discord`  | `https://discord.com/api/webhooks/<id>/<token>`                  | embed z kolorowym paskiem (domyślny)                                         |
| `slack`    | `https://hooks.slack.com/services/…` (także Mattermost, Rocket.Chat) | `{"text": "…"}`                                                          |
| `ntfy`     | `https://ntfy.sh/<temat>` albo Twój serwer                       | ciało tekstowe, nagłówki `Title`, `Priority` (`high` dla awarii), `Tags`, `Click` |
| `gotify`   | `https://<host>/message?token=<TOKEN_APLIKACJI>`                 | `{"title", "message", "priority"}` (awaria = 8)                              |
| `telegram` | `https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<CHAT_ID>` | `{"text": "…"}`, bez parsowania Markdowna; id czatu jedzie w adresie       |
| `generic`  | dowolny adres https                                              | JSON poniżej                                                                 |

`generic` to stabilny kontrakt — pola nigdy nie zmieniają nazw:

```json
{"event": "down", "service": "Jellyfin", "group": "Media", "state": "down",
 "previous": "up", "reason": "connection refused",
 "at": "2026-10-16T12:30:00Z", "url": "http://192.168.1.30:8096"}
```

`event` to `down`, `degraded`, `recovered` albo `test`; `state` to nowy stan, a
`previous` — ostatni zgłoszony (`up`, `degraded` lub `down`); `reason` jest pusty
dla `recovered`. Dane logowania do ntfy mogą siedzieć w adresie
(`https://user:hasło@ntfy.example/temat`).

Przycisk **Wyślij wiadomość próbną** działa w każdym formacie. Adresy są
maskowane w API i w panelu (tylko schemat i host), a nieudany test nigdy nie
odsyła adresu w komunikacie — siedzą w nim tokeny botów. Panel przyjmuje wyłącznie
adresy `https://`; jeśli Twój ntfy albo Gotify stoi na zwykłym `http` w LAN-ie,
podaj adres przez deploy: `SHYG_WEBHOOK=http://ntfy.home.arpa/alerty`
`SHYG_WEBHOOK_FORMAT=ntfy` — tam wybór należy do tego, kto uruchamia.

## Instalacja

### Docker Compose

```yaml
services:
  shyg:
    image: ghcr.io/zatto-dev/shy-garlic:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      SHYG_CONFIG: /etc/shygarlic/shygarlic.yaml
      SHYG_DATA: /data/shygarlic.db
      SHYG_DOCKER: http://docker-proxy:2375   # usuń tę linię, jeśli nie chcesz podpowiedzi z kontenerów
      TZ: Europe/Warsaw
    volumes:
      - ./shygarlic.yaml:/etc/shygarlic/shygarlic.yaml   # Twój config, zapisywalny
      - shyg-data:/data                                  # konta, historia, cache ikon, tapety
    healthcheck:
      test: ["CMD", "/shyg", "version"]
      interval: 30s
      timeout: 5s
      retries: 3
    depends_on:
      - docker-proxy

  # Widok na gniazdo Dockera tylko do odczytu: kontenery można wylistować,
  # niczego nie da się uruchomić, zatrzymać ani utworzyć.
  docker-proxy:
    image: ghcr.io/tecnativa/docker-socket-proxy:latest
    restart: unless-stopped
    environment:
      CONTAINERS: 1
      POST: 0
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro

volumes:
  shyg-data:
```

Wejdź na `http://localhost:8080`.

Obraz działa jako uid **65532**. Nazwany wolumen dziedziczy właściciela z obrazu,
ale bind-mount już nie — nadaj go plikowi konfiguracyjnemu sam, LICZBĄ, bo host
nie zna nazwy `nonroot`:

```bash
chown 65532:65532 ./shygarlic.yaml
```

### Skrypt instalacyjny

```bash
curl -fsSL https://raw.githubusercontent.com/zatto-dev/shy-garlic/main/install.sh | sh
```

Pobiera binarkę wydania dla Twojej platformy i weryfikuje jej sumę SHA-256 wobec
pliku sum opublikowanego w tym samym wydaniu; przy niezgodności przerywa
instalację. `SHYG_VERSION` przypina wersję, `SHYG_BIN_DIR` wybiera katalog
docelowy.

```bash
shyg serve -config shygarlic.yaml -data shygarlic.db -addr :8080
```

### Ze źródeł

```bash
go install github.com/zatto-dev/shy-garlic/cmd/shyg@latest   # Go 1.27
```

### Kubernetes / Talos Linux

Potrzebna StorageClass z wolumenem `ReadWriteOnce`. Shy Garlic trzyma SQLite i
własny plik YAML na jednym wolumenie, więc zawsze działa jako pojedyncza replika.
Pod spełnia profil Pod Security **restricted** (non-root uid 65532, wszystkie
capabilities zdjęte, seccomp `RuntimeDefault`), więc przejdzie w namespace'ach
z domyślnym dla Talosa `baseline` i w ostrzejszych.

**Helm** (chart jako artefakt OCI):

```bash
helm install shy-garlic oci://ghcr.io/zatto-dev/charts/shy-garlic \
  --namespace shy-garlic --create-namespace
```

**Zwykły kubectl** (kustomize, przypięty do taga wydania):

```bash
kubectl apply -k "github.com/zatto-dev/shy-garlic//deploy/kubernetes?ref=v0.13.1"
```

Potem wejdź do panelu i załóż administratora przy pierwszej wizycie (zob.
*Pierwsze uruchomienie*):

```bash
kubectl -n shy-garlic port-forward svc/shy-garlic 8080:80    # http://localhost:8080
```

Na stały adres włącz w values `ingress` albo `httpRoute` (Gateway API) i ustaw
`trustedProxy` na CIDR proxy. Wszystkie opcje są opisane w
[`charts/shy-garlic/values.yaml`](charts/shy-garlic/values.yaml). Żeby pominąć
stronę `/setup`, wskaż `admin.existingSecret` na Secret z `SHYG_ADMIN_USER`
i `SHYG_ADMIN_PASSWORD`.

Świeży klaster Talos **nie ma domyślnej StorageClass**, więc claim zostaje
`Pending`, a z nim pod. Zainstaluj ją najpierw, np.
[local-path-provisioner](https://github.com/rancher/local-path-provisioner#installation)
(dane na dysku węzła; aktualny manifest jest w jego README). Talos domyślnie
egzekwuje Pod Security `baseline`, a pody pomocnicze provisionera używają
`hostPath`, którego `baseline` zabrania — więc oznacz jego namespace i ustaw
klasę jako domyślną:

```bash
kubectl create namespace local-path-storage
kubectl label namespace local-path-storage pod-security.kubernetes.io/enforce=privileged
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.37/deploy/local-path-storage.yaml
kubectl patch storageclass local-path -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
```

Jeśli woluminy nadal się nie tworzą, zajrzyj do
[przewodnika Talosa o lokalnym storage](https://www.talos.dev/latest/kubernetes-guides/configuration/local-storage/).

`helm uninstall` celowo zostawia wolumen, żeby dane przeżyły pomyłkę; żeby
zainstalować ponownie na nim, dodaj `--set persistence.existingClaim=shy-garlic`.

Dwie rzeczy różnią się od Dockera. Config leży na wolumenie
(`/data/config/shygarlic.yaml`): przy pierwszym starcie powstaje z wbudowanego
przykładu, potem edytujesz go w panelu, a aktualizacja go nie nadpisuje. I nie
ma gniazda Dockera, więc odkrywanie kontenerów jest po prostu wyłączone —
usługi bierze się z pliku configu.

### Pierwsze uruchomienie

Pierwsze wejście prowadzi na `/setup`, gdzie zakładasz konto administratora.
Dopóki baza jest pusta, panel nie wpuszcza nikogo dalej; gdy konto już istnieje,
`/setup` jest zamknięty.

Założenie konta wymaga **tokenu instalacji**, który dowodzi, że to Ty prowadzisz
instancję, a nie ten, kto pierwszy trafił na port. Panel wypisuje go w logu przy
starcie:

```bash
docker logs shyg 2>&1 | grep setup_token                      # Docker
kubectl -n shy-garlic logs deploy/shy-garlic | grep setup_token # Kubernetes
```

```text
level=WARN msg="first run: no account exists yet. Open /setup and enter this setup token …" setup_token=3f9c01ab-5d27e4c1-9a0b7d33-c1e8f642
```

Wklej go w pole *Token instalacji*. Token żyje tylko w pamięci: restart przed
założeniem konta wypisze nowy, a po założeniu konta jest wyrzucany. Ustawienie
`SHYG_ADMIN_USER` i `SHYG_ADMIN_PASSWORD` zakłada konto przy starcie i pomija
`/setup` (razem z tokenem).

Żeby panel odpowiadał tylko na nazwę, pod którą go otwierasz, ustaw
`--allowed-host` / `SHYG_ALLOWED_HOSTS` na listę nazw hostów rozdzielonych
przecinkami (bez portów), np. `SHYG_ALLOWED_HOSTS=dash.example.org,dash.lan`.
Żądanie z innym `Host` dostaje `421 Misdirected Request`; to zatrzymuje
złośliwą stronę internetową, która próbuje dobić się do panelu w Twojej sieci
przez DNS rebinding. `localhost`, `127.0.0.1` i `::1` działają zawsze. Pusta
lista (domyślnie) oznacza brak kontroli. Za reverse proxy lista musi zawierać
nazwę, którą proxy przekazuje w `Host`; scrapery otwierające `/metrics` po
adresie IP potrzebują tego adresu na liście (albo muszą wysyłać dozwolony
nagłówek `Host`). `/healthz` jest wyłączony z kontroli, więc sondy kontenera i
Kubernetesa działają.

Za reverse proxy (Traefik, Caddy, Pangolin) ustaw `--trusted-proxy` /
`SHYG_TRUSTED_PROXY` na jego sieć, np. `10.0.0.0/8`. Bez tego `X-Forwarded-For` i
`X-Forwarded-Proto` są ignorowane — inaczej jeden nagłówek na żądanie
wystarczyłby, żeby obejść limiter prób logowania. Proxy musi też przekazywać
oryginalny nagłówek `Host`: żądania zapisujące są odrzucane, gdy `Origin` z
przeglądarki się z nim nie zgadza.

## Panel

<table>
  <tr>
    <td width="50%"><img src="docs/img/panel-light.png" alt="Motyw jasny"></td>
    <td width="50%"><img src="docs/img/tryb-edycji.png" alt="Tryb edycji"></td>
  </tr>
  <tr>
    <td align="center"><sub>Motyw jasny i ciemny</sub></td>
    <td align="center"><sub>Tryb edycji — dodawanie, edycja i usuwanie z UI</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/img/panel-list-dark.png" alt="Widok listy"></td>
    <td width="50%"><img src="docs/img/panel-bento-dark.png" alt="Widok bento"></td>
  </tr>
  <tr>
    <td align="center"><sub>Widok listy — na maszynę z sześćdziesięcioma usługami</sub></td>
    <td align="center"><sub>Widok bento — kafle w trzech rozmiarach</sub></td>
  </tr>
</table>

Tło to albo preset (aurora, nebula, graphite, dawn, fog), albo Twoja własna
tapeta, z rozmyciem i przyciemnieniem, które ustawiasz sam — przyciemnieniem, bo
na zdjęciu o dużym kontraście wewnętrznym żaden wybór koloru tekstu nie ratuje
czytelności.

<p align="center">
  <img src="docs/img/background-picker-dark.png" alt="Wybór tła" width="100%">
</p>

Przełącznik w górnym pasku zmienia wygląd **tylko w Twojej** przeglądarce.
Ustawienia → Wygląd zmieniają domyślny wygląd instancji, w pliku, przez zwykły
diff. Interfejs mówi po angielsku i po polsku, przełączasz w nagłówku.

### Skróty klawiszowe

| Klawisz     | Działanie                       |
| ----------- | ------------------------------- |
| `/`         | wejdź w wyszukiwarkę            |
| `E`         | włącz i wyłącz tryb edycji      |
| `N`         | nowa usługa                     |
| `B`         | wybór tła                       |
| `1` `2` `3` | widok grid, bento, lista        |
| `Esc`       | zamknij, potem wyczyść filtr    |

W trybie edycji przeciągnij uchwyt na karcie albo nagłówku grupy, żeby zmienić
kolejność (mysz lub dotyk; `Esc` anuluje). Z klawiatury: fokus na uchwycie i
`Alt`+`↑`/`↓` — nową pozycję odczyta czytnik ekranu. Ołówek przy tytule edytuje
go w miejscu: `Enter` albo wyjście z pola zapisuje, `Esc` anuluje.

## Konfiguracja

```yaml
# Ten komentarz przeżyje każdą edycję z UI.
title: Homelab

# Wspólne ustawienia — zmieniasz raz, działa wszędzie.
defaults: &defaults
  check:
    interval: 30s
    timeout: 6s
    expect: [200, 301, 302, 401, 403]

# Domyślny wygląd instancji. Każda przeglądarka może go u siebie nadpisać;
# ten blok to jest to, co widzi, zanim cokolwiek sama wybierze.
appearance:
  background:
    preset: aurora    # aurora | nebula | graphite | dawn | fog | photo
    blur: 10          # 0..30 px, dotyczy własnej tapety
    tone: dark        # dark | light — jasność tekstu
  view: grid          # grid | bento | list

groups:
  - name: Media
    services:
      - name: Jellyfin
        url: http://jellyfin.home.arpa:8096
        icon: jellyfin
        size: lg      # sm | md | lg — rozmiar kafla w widoku bento
        description: Filmy i muzyka     # pod nazwą, też wyszukiwany
        tags: [media, rodzina]          # lista; wyszukiwana, chipy w widoku listy
        <<: *defaults

  - name: Zarządzanie
    services:
      - name: Proxmox
        url: https://proxmox.home.arpa:8006
        icon: proxmox
        check:
          interval: 30s
          expect: [200, 401]
          insecure: true    # samopodpisany certyfikat, tak ma być

      - name: Grafana
        url: http://grafana.home.arpa:3000
        check:
          path: /api/health               # pytane zamiast strony głównej
          contains: '"database": "ok"'    # inna treść → degraded
```

Kontrola treści działa na odpowiedziach, których kod jest w `expect`; zły kod to
`HTTP <kod>` bez czytania ciała. Przekierowania nie są śledzone, więc wskaż w
`path` adres docelowy. Przeszukiwany jest tylko pierwszy 1 MiB. `check.path` nigdy
nie zmienia hosta: podmienia ścieżkę i query z `url`, a `url` zostaje tym, do
czego prowadzi karta.

| Pole                           | Znaczenie                                              | Domyślnie                    |
| ------------------------------ | ------------------------------------------------------ | ---------------------------- |
| `title`                        | nagłówek panelu                                         | `Shy Garlic`                 |
| `groups[].name`                | nazwa sekcji                                            | —                            |
| `services[].name`              | nazwa usługi                                            | wymagane                     |
| `services[].url`               | adres; `http://`, `https://` albo `tcp://host:port`     | wymagane                     |
| `services[].icon`              | nazwa z katalogu ikon albo pełny adres URL              | litera z nazwy               |
| `services[].size`              | rozmiar kafla w widoku bento: `sm`, `md`, `lg`          | `md`                         |
| `services[].description`       | jedna linia pod nazwą; też wyszukiwana (maks. 200 znaków) | —                          |
| `services[].tags`              | lista etykiet, wyszukiwana; chipy w widoku listy (maks. 12×32) | —                     |
| `appearance.background.preset` | `aurora`, `nebula`, `graphite`, `dawn`, `fog`, `photo`  | `aurora`                     |
| `appearance.background.image`  | nazwa pliku z `/bg/`, tylko przy `preset: photo`        | —                            |
| `appearance.background.blur`   | rozmycie tapety w px, `0`–`30`                          | `10`                         |
| `appearance.background.dim`    | przyciemnienie pod treścią w %, `0`–`80`                | `50`                         |
| `appearance.background.tone`   | jasność tekstu: `dark` albo `light`                     | `dark`                       |
| `appearance.view`              | `grid`, `bento` albo `list`                             | `grid`                       |
| `check.interval`               | co ile sprawdzać; od 1 s do 24 h, inna wartość wraca do domyślnej i jest zgłoszona w banerze | `30s`                        |
| `check.timeout`                | ile czekać na odpowiedź; od 1 s do 24 h, ta sama zasada | `5s`                         |
| `check.expect`                 | kody uznawane za „żyje"                                 | `[200, 301, 302, 401, 403]`  |
| `check.insecure`               | ignoruj błędy certyfikatu                               | `false`                      |
| `check.path`                   | ścieżka (i query) pytana zamiast tej z `url`; zaczyna się od `/`, ten sam host | —                    |
| `check.contains`               | tekst, który musi być w pierwszym 1 MiB odpowiedzi; inaczej `degraded` | —                   |
| `check.not_contains`           | tekst, którego nie może tam być; inaczej `degraded`     | —                            |
| `notify.threshold`             | ile nieudanych sprawdzeń z rzędu, zanim powiadomimy     | `2`                          |
| `discovery.docker`             | podpowiadaj usługi z kontenerów                         | włączone, jeśli jest gniazdo |

### Gdzie co mieszka

Konfiguracja stoi w czterech warstwach. Granica nie przebiega po ważności, tylko
po tym, **kiedy wartość musi być znana** i **czy wolno jej trafić do gita**.

| Warstwa                      | Co                                                                                                  | Dlaczego akurat tu                                                                                                                                                            |
| ---------------------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Flagi i zmienne środowiskowe | `--addr`, `--config`, `--seed-config`, `--data`, `--cache`, `--backgrounds`, `--icons`, `--docker`, `--trusted-proxy`, `--metrics-allow`, `--allowed-host`, `--webhook`, `--webhook-format` | musi być znane, zanim wstanie serwer, albo jest własnością deployu — a `--trusted-proxy` rozstrzyga, komu wolno ustawiać `X-Forwarded-For`, więc nie może być klikalne z panelu |
| Ten plik                     | `title`, `appearance`, `notify.threshold`, `discovery.docker`, `groups`                             | treść produktu: jawna, czytelna, bezpieczna w commicie                                                                                                                        |
| Baza SQLite                  | adres i format webhooka, tokeny API (skrót), konta, sesje, historia                                                              | **poświadczenia i stan** — baza nie jest commitowana                                                                                                                           |
| Przeglądarka                 | Twoje nadpisanie wyglądu                                                                            | dotyczy jednej przeglądarki i nikogo więcej                                                                                                                                    |

Każda flaga ma odpowiednik w zmiennej środowiskowej z przedrostkiem `SHYG_`
(`SHYG_ADDR`, `SHYG_CONFIG`, …). Ustawienia → Instancja pokazują je wszystkie
razem ze źródłem każdej wartości: flaga, zmienna czy wartość domyślna.

`--seed-config` (`SHYG_SEED_CONFIG`) przy pierwszym starcie kopiuje config
startowy pod `--config`, tylko gdy configu jeszcze nie ma — chart Helm
i manifesty Kubernetes zasilają tak wolumen z ConfigMapy. Istniejącego configu
nigdy nie nadpisuje.

! **Adresu webhooka nie ma w pliku i nie będzie.** Panel ten plik pokazuje i
commituje, a URL jest poświadczeniem.

## Logowanie

<p align="center">
  <img src="docs/img/login.png" alt="Ekran logowania" width="70%">
</p>

Konta mieszkają w SQLite, hasła haszowane bcryptem. Sesja to ciasteczko
`HttpOnly`, `SameSite=Strict` ważne 30 dni, trasy zapisujące niosą token CSRF, a
pięć nieudanych prób z jednego adresu blokuje logowanie na pięć minut. Zmiana
hasła ma taki sam limit prób. API bez sesji odpowiada `401`. Za HTTPS
(bezpośrednio albo przez zaufane proxy) panel wysyła też
`Strict-Transport-Security`.

Nie ma uprawnień per użytkownik: każde konto może wszystko. Przed czym panel się
broni, a przed czym nie, i jak zakłada, że go wdrożysz, spisane jest w
[SECURITY.md](SECURITY.md) — przeczytaj, zanim wystawisz to szerzej niż na LAN.

## Metryki i tokeny API tylko do odczytu

Shy Garlic potrafi karmić Twój istniejący monitoring. **Ustawienia → Instancja →
Tokeny API** tworzy token (`shyg_…`, 256 bitów losowości) z wybraną nazwą.
Pokazujemy go raz, z przyciskiem „Kopiuj", a w bazie leży tylko jego skrót
SHA-256 — jeśli go zgubisz, odwołaj go i utwórz nowy.

Token jest **tylko do odczytu z konstrukcji**: nagłówek `Authorization: Bearer
shyg_…` jest honorowany wyłącznie dla `GET` na trzech trasach — `/metrics`,
`/api/status` i `/api/history`. Wszędzie indziej jest ignorowany i trasa żąda
sesji, więc token wyciekły z Prometheusa nie zmieni configu, nie założy konta i
nie wystawi kolejnego tokenu. Żądanie bez ważnego tokenu dostaje `401`, nigdy
przekierowanie na logowanie. Nieudane próby są limitowane per adres, a w logu
pojawiają się raz na minutę na adres.

```sh
# szybki podgląd; token w pliku, nie w historii powłoki
curl -sS -H "Authorization: Bearer $(cat /etc/prometheus/shyg.token)" \
  https://garlic.home.arpa/metrics
```

### Prometheus

```yaml
scrape_configs:
  - job_name: shy-garlic
    scrape_interval: 30s
    scheme: https
    authorization:                                # Prometheus 2.26+
      type: Bearer
      credentials_file: /etc/prometheus/shyg.token
    # starsze wersje: bearer_token_file: /etc/prometheus/shyg.token
    static_configs:
      - targets: ["garlic.home.arpa"]
```

| Metryka                                        | Znaczenie                                                                 |
| ---------------------------------------------- | ------------------------------------------------------------------------- |
| `shyg_service_up`                              | `1`, gdy ostatnie sprawdzenie się udało; `0` dla degraded i down          |
| `shyg_service_degraded`                        | `1`, gdy odpowiada, ale złym kodem albo złą treścią                       |
| `shyg_service_response_seconds`                | czas odpowiedzi ostatniego sprawdzenia; brak, gdy nic nie odpowiedziało   |
| `shyg_service_availability_ratio{window="24h"}` | odsetek udanych sprawdzeń w 24 godzinach, `0`–`1`                         |
| `shyg_service_last_check_timestamp_seconds`    | czas Unix ostatniego sprawdzenia                                          |
| `shyg_build_info{version}`                     | zawsze `1`                                                                |

Wszystkie metryki usług mają etykiety `group` i `service`. Usługa, której jeszcze
nie sprawdzono (albo bez adresu), **nie ma żadnej serii** — `0` czytałoby się
jako „leży" i budziło bez powodu. Alert, który odpala się tylko przy prawdziwej
awarii: `shyg_service_up == 0 and shyg_service_degraded == 0`.

### Dashboard w Grafanie

Liczniki up/degraded/down, co nie działa teraz i od kiedy, każda usługa z
dostępnością z 24 godzin, czasy odpowiedzi i oś stanów per usługa:

![Dashboard Shy Garlic w Grafanie](docs/img/grafana-dashboard.png)

1. W Grafanie: **Dashboards → New → Import**.
2. Wgraj [`deploy/grafana/shy-garlic.json`](deploy/grafana/shy-garlic.json)
   albo wklej jego treść.
3. Wskaż swoje źródło Prometheus, a w polu **Prometheus job** wpisz
   `job_name` swojego scrape configu, jeśli to nie `shy-garlic` (ten z
   przykładu wyżej). Zmiana później: **Settings → Variables → job** w
   ustawieniach dashboardu.

Dashboard używa wyłącznie wbudowanych paneli (stat, table, bar gauge, state
timeline, time series, text) — nie trzeba instalować żadnych pluginów.
Wymaga Grafany 11.0 lub nowszej. Opisy paneli są po angielsku.

### Bez tokenu, z własnej sieci

Domyślnie `/metrics` wymaga tokenu. Jeśli Prometheus stoi w zaufanej sieci i
wolisz nie zarządzać tokenem, wymień tę sieć:

```sh
shyg serve --metrics-allow 192.168.0.0/24          # albo SHYG_METRICS_ALLOW=192.168.0.0/24,fd00::/8
```

To otwiera **wyłącznie** `GET /metrics` i tylko dla tych adresów. Za reverse
proxy ustaw też `--trusted-proxy`: adres klienta jest wtedy brany z
`X-Forwarded-For` jako skrajnie prawy wpis, który nie jest zaufanym proxy
(wpisy z lewej pisze klient). Żądanie z nagłówkami proxy od peera, którego
**nie** wpisano jako zaufane proxy, jest odrzucane — inaczej proxy stojące w
Twoim LAN-ie pozwalałoby czytać metryki całemu internetowi. Z tego samego
powodu zaufane proxy, które w ogóle **nie** wysyła `X-Forwarded-For`, jest przez
`--metrics-allow` odrzucane (każde żądanie wyglądałoby jak adres samego proxy):
nie używaj `--metrics-allow` za forwarderami L4 ani proxy, które nie dopisuje
`X-Forwarded-For` (np. nginx skonfigurowany tylko z `X-Real-IP`). Tam używaj
tokenu; tokeny działają dalej.

### Home Assistant

```yaml
# configuration.yaml — w secrets.yaml:  shyg_authorization: "Bearer shyg_…"
rest:
  - resource: https://garlic.home.arpa/api/status
    scan_interval: 60
    headers:
      Authorization: !secret shyg_authorization
    sensor:
      - name: "Garlic: usługi nie-up"
        value_template: "{{ value_json.values() | rejectattr('state', 'equalto', 'up') | list | length }}"
    binary_sensor:
      - name: "Jellyfin działa"
        device_class: connectivity
        value_template: "{{ value_json['Media/Jellyfin'].state == 'up' }}"
```

## API

Wszystko za sesją — z wyjątkiem `/api/status`, `/api/history` i `/metrics`, które
przyjmują też [token tylko do odczytu](#metryki-i-tokeny-api-tylko-do-odczytu).
Panel używa dokładnie tych samych endpointów co Ty.

| Metoda   | Ścieżka                          | Co robi                                                |
| -------- | -------------------------------- | ------------------------------------------------------ |
| `GET`    | `/api/status`                    | aktualny stan wszystkich usług (sesja albo token API)   |
| `GET`    | `/events`                        | strumień SSE ze zmianami stanu                          |
| `GET`    | `/api/history?key=Grupa/Usługa`  | historia sprawdzeń i dostępność 24 h (sesja albo token) |
| `GET`    | `/api/service?key=Grupa/Usługa`  | wszystko o jednej usłudze: staty, wykres, zmiany stanu  |
| `POST`   | `/api/check?key=Grupa/Usługa`    | sprawdź tę usługę natychmiast                           |
| `GET`    | `/api/info`                      | co ta instancja ma włączone                             |
| `GET`    | `/api/instance`                  | opcje startowe i źródło każdej wartości                 |
| `GET`    | `/api/discover`                  | usługi zaproponowane z działających kontenerów          |
| `GET`    | `/api/icons?q=jelly`             | wyszukiwarka ikon                                       |
| `GET`    | `/api/config`                    | surowy YAML                                             |
| `PUT`    | `/api/config`                    | zapis surowego YAML-a (najpierw walidacja)              |
| `POST`   | `/api/services`                  | dodaj usługę                                            |
| `PATCH`  | `/api/services`                  | zmień usługę                                            |
| `DELETE` | `/api/services?group=…&name=…`   | usuń usługę                                             |
| `POST`   | `/api/services/move`             | przenieś usługę na pozycję, w grupie lub między grupami |
| `POST`   | `/api/groups/move`               | przenieś grupę na pozycję                               |
| `PUT`    | `/api/title`                     | zmień tytuł panelu                                      |
| `POST`   | `/api/services/bulk`             | dodaj wiele usług jako jedną zmianę                     |
| `POST`   | `/api/groups`                    | dodaj grupę                                             |
| `PATCH`  | `/api/groups`                    | zmień nazwę grupy                                       |
| `DELETE` | `/api/groups?name=…`             | usuń grupę                                              |
| `GET`    | `/api/versions`                  | historia zmian                                          |
| `POST`   | `/api/versions/{id}/restore`     | przywróć plik do tej wersji                             |
| `GET`    | `/api/appearance`                | domyślny wygląd instancji                               |
| `PUT`    | `/api/appearance`                | zmiana wyglądu (przez diff, jak każda edycja)           |
| `GET`    | `/api/notify`                    | ustawienia powiadomień, bez adresu                      |
| `PUT`    | `/api/notify`                    | ustaw adres webhooka, format wiadomości i próg           |
| `POST`   | `/api/notify/test`               | wyślij wiadomość testową                                |
| `POST`   | `/api/background`                | wgraj tapetę (multipart `file`)                         |
| `GET`    | `/api/background`                | lista wgranych tapet                                    |
| `DELETE` | `/api/background/{name}`        | usuń tapetę (`409`, gdy jest w użyciu)                  |
| `GET`    | `/bg/{name}`                    | serwowanie tapety — tylko dla zalogowanych              |
| `GET`    | `/metrics`                       | metryki Prometheusa — sesja albo token API              |
| `GET`    | `/api/tokens`                    | lista tokenów API (nigdy sam sekret)                    |
| `POST`   | `/api/tokens`                    | utwórz token; sekret tylko w tej jednej odpowiedzi      |
| `DELETE` | `/api/tokens/{id}`               | odwołaj token                                           |
| `GET`    | `/healthz`                       | liveness, jedyna trasa bez sesji                        |

Zarządzanie kontami (`/api/users`, `/api/password`) działa na tych samych
zasadach.

Dopisz `?dry=1` do dowolnego wywołania zapisującego, a nic się nie zapisze —
dostaniesz diff tego, co by się zmieniło. Dokładnie tego używa panel do podglądu.

Jeśli ktoś zmienił plik na dysku między odczytem a zapisem, dostajesz `409`
zamiast cichego nadpisania cudzej pracy.

## Jak to jest zbudowane

```
cmd/shyg            wejście CLI: serve, check, version
internal/config     silnik konfiguracji — parsowanie i round-trip-safe zapis
internal/health     sprawdzanie usług: scheduler, semantyka kodów, stany
internal/i18n       teksty interfejsu, angielski i polski
internal/server     HTTP, SSE, szablony, ikony, API edytora, logowanie
internal/store      SQLite: konta, sesje, historia sprawdzeń
```

Kluczowa decyzja jest w `internal/config`: **AST służy wyłącznie do nawigacji**, a
zapis podmienia zakres bajtów w oryginalnym tekście. Wynika to z pomiaru, nie z
gustu — `gopkg.in/yaml.v3` przy zapisie kasuje puste linie i przepisuje
`<<: *defaults` na `!!merge <<: *defaults`, a nawet `goccy/go-yaml`, który radzi
sobie z tym najlepiej, zbija wyrównanie komentarzy końca linii. Skoro sam
odczyt-i-zapis rusza linie, których użytkownik nie tknął, to nie jest round-trip.
Stąd splice po offsetach.

Frontend to `html/template` plus waniliowy JavaScript: żadnego builda, żadnych
`node_modules`, ani jednego pakietu z npm w produkcie.

## Stan projektu

Wersje zostają w `0.x`, dopóki projekt nie przejdzie audytu bezpieczeństwa. Do
tego czasu API HTTP i format pliku konfiguracyjnego mogą się jeszcze zmienić;
każda zmiana, która psuje coś, na czym polegałeś, jest zapisana w
[CHANGELOG.md](CHANGELOG.md).

Rzeczy, których nie ma świadomie, a nie przez zapomnienie:

- **Nie ma alertingu.** Jedna wiadomość, gdy padnie, jedna, gdy wróci. Uptime
  Kuma istnieje.
- **Nie ma uprawnień per użytkownik.** Każde konto może wszystko.
- **Nie ma sprawdzania aktualizacji.** Panel nie dzwoni do domu, nawet po to, by
  zapytać, czy jest nowsza wersja — cichy ruch na zewnątrz podważałby jedyną
  rzecz, którą ten projekt obiecuje.

## Wkład

Zgłoszenia błędów, patche i tłumaczenia są mile widziane. Nie ma CLA; podpisuj
commity przez [DCO](CONTRIBUTING.md) (`git commit -s`, dodaje linię
`Signed-off-by`). Jak budować i testować, opisuje
[CONTRIBUTING.md](CONTRIBUTING.md); podatności zgłasza się drogą z
[SECURITY.md](SECURITY.md), a nie w publicznym issue.

## Licencja

**GNU Affero General Public License v3.0 lub nowsza** — patrz [LICENSE](LICENSE).

Wybrana świadomie: jeśli ktoś uruchomi zmienionego Shy Garlica jako usługę, jego
użytkownicy dostają źródła. Nie zamyka żadnego kanału dystrybucji, na którym nam
zależy — nixpkgs, aplikacje TrueNAS, Unraid Community Applications i sklep
Umbrela rozprowadzają dziś oprogramowanie na AGPL.
