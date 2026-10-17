<p align="center">
  <img src="docs/img/banner.png" alt="Shy Garlic" width="100%">
</p>

<p align="center">
  <b>A homelab dashboard where the config file and the interface are the same thing.</b><br>
  Click in the UI — the YAML changes. Edit the YAML — the UI changes.<br>
  And nobody eats your comments along the way.
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.pl.md">Polski</a>
</p>

<p align="center">
  <a href="https://github.com/zatto-dev/shy-garlic/actions/workflows/ci.yml"><img src="https://github.com/zatto-dev/shy-garlic/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/zatto-dev/shy-garlic/releases/latest"><img src="https://img.shields.io/github/v/release/zatto-dev/shy-garlic" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/licence-AGPL--3.0--or--later-blue" alt="Licence: AGPL-3.0-or-later"></a>
</p>

<p align="center">
  <img src="docs/img/panel-dark.png" alt="Shy Garlic dashboard — dark theme" width="100%">
</p>

---

Shy Garlic is a start page for a homelab: your services in groups, each with a
heartbeat that says whether it is actually up. The whole instance is one YAML
file you can keep in git — and the panel is a full editor of that same file.

- **One static Go binary**, 14 MB, no cgo. SQLite through `modernc.org/sqlite`.
  The container image is about 16 MB.
- **No node in the product.** `html/template`, vanilla JavaScript and SSE — no
  build step for the frontend, and not one CDN request from your browser.
- **Writes from the UI are round-trip-safe.** Comments, anchors, alignment and
  blank lines come out of a save exactly as they went in.

## Why another dashboard

The market split along one axis and nobody crossed to the other side:

|           | Config in a file (git, diff) | Editing in the UI (click, phone) |
| --------- | ---------------------------- | -------------------------------- |
| **Light** | Homepage, Glance, Homer      | **← we are here**                |
| **Heavy** | Dashy                        | Homarr, Heimdall, Flame, Fenrus  |

Either you get a file you can version in git, but every change means an SSH
session. Or you get a comfortable UI while the configuration lives in a database
you cannot diff, review, or copy to another machine.

**Shy Garlic does both, over the same file.** A change made by clicking is
written back as a splice at byte offsets in the original text — not as a
re-serialisation of a parsed document. That is the whole difference: comments,
blank lines, anchors (`&defaults`), merge keys (`<<: *defaults`), quoting style
and the alignment of end-of-line comments stay exactly where they were.

This is treated as impossible often enough to be worth stating plainly. Home
Assistant closed a request for comment-preserving writes (`frontend#6486`) with
*"parsers are not comment aware, this has been brought up many times, but not
fixable"*. It is fixable. This project is the proof.

Here is a diff of a real file after adding a service by clicking in the UI:

```diff
       - name: "Sonarr"          # quotes on purpose, don't strip them
-        url: http://sonarr.home.arpa:8989
+        url: http://sonarr.home.arpa:8990
         icon: sonarr
         <<: *defaults
@@
           expect: [200]
+      - name: AdGuard
+        url: http://adguard.home.arpa:3000
+        icon: adguard-home
 
 # Footer: nothing here, but the comment must survive.
```

One line changed, three added, no collateral damage.

## You see the change before it happens

An editor over somebody else's file only works if they trust it. So the panel
never writes anything you have not seen first: every action — adding a service,
renaming a group, editing raw YAML, changing the instance appearance — shows the
exact diff and waits for your confirmation. The two quick gestures are the
exception: renaming the dashboard title inline and dragging things into a new
order apply at once, because a confirmation dialog on every drop would make them
unusable. They still go through the same engine, land in the version history,
and are one click from being undone.

Behind that there are two more safety nets:

- **Version history.** Every save is kept with its diff, its author and its
  timestamp. Going back to how the file looked an hour ago is one click, and the
  restore itself becomes a history entry, so you can undo the undo.
- **Git commits.** If the config lives inside a git repository, every save from
  the UI becomes a commit: `shyg: added service "Radarr" to group "Infrastructure"`.
  You click in a browser, and the history shows up in `git log`. No git, no
  repository — nothing happens, no error. The default image is distroless and
  therefore has no `git` binary; build `--target git` for an Alpine-based image
  that does, or run the binary on a host that has git.

## An editor that doesn't need node

<p align="center">
  <img src="docs/img/edytor-pliku.png" alt="Config editor with YAML syntax highlighting" width="100%">
</p>

The file tab in settings is a real editor: YAML syntax highlighting — comments,
keys, numbers, keywords and anchors each in their own colour, in both themes —
and a full-screen mode for longer files. The highlighter is written from
scratch, because the alternative was a build step or a CDN, and this project has
neither. The field stays a plain `<textarea>`, so undo, paste and selection
behave the way the browser intends.

Saving from here goes through the same diff preview, the same version history
and the same git commit as a change made by clicking.

## A heartbeat that doesn't lie

Checks run **on the server**, over HTTP or TCP, and keep running with no browser
tab open.

- **The list of accepted status codes is explicit** and defaults to
  `200, 301, 302, 401, 403` — because `401` and `403` mean "the service is up
  and wants you to log in", not "the service is down".
- **"Cannot connect" is a different state than "the server answered something
  unexpected."** The first is red, the second amber.
- **States are told apart by shape, not only colour** — circle, diamond, square.
  Colour alone is not enough for colour-blind users.
- `insecure: true` for self-signed certificates, because in a homelab they are
  everywhere and a Proxmox box is not down just because you never bought a cert.
- **A page that answers 200 can still be wrong.** `check.contains` and
  `check.not_contains` look inside the answer (the first 1 MiB), and
  `check.path` asks a health endpoint instead of the front page. When the code
  is fine but the content is not — nginx's default page instead of the app, a
  "maintenance" banner — the service goes **amber (`degraded`)**, with the reason
  in the details window: the server is alive, it just is not saying the right thing.

## Every service has a story

<p align="center">
  <img src="docs/img/detale.png" alt="Service details with uptime and response times" width="100%">
</p>

Clicking the chart on a card opens the details: uptime over 24 hours and 7 days,
median and p95 response time, the slowest check, a latency chart, and the log of
the moments the service actually changed state, with the reason.

## Icons that don't report on you

The usual approach puts `<img src="https://cdn.jsdelivr.net/...">` straight into
the HTML. That means **the user's browser asks a third-party CDN about the names
of the services running in their home** — a request for `vaultwarden.svg` and
`proxmox.svg` leaves your IP. As a bonus, the dashboard loses every icon when
the internet is down.

Here the browser only ever asks your own panel, for `/icon/<name>`, and nothing
else. **The server** fetches an icon once and caches it on disk.

To be exact about what that buys you, because privacy claims deserve precision:
the CDN never sees your browser, never learns how many people use the panel,
and gets nothing at all after the first fetch. What it does see, once, is a
server asking for a file. If even that is too much, `--icons icons.zip` ships
the complete set in one file and makes the number of outbound requests zero.

The catalogue holds about 4200 icons, searchable by name and by alias.

## It can read your containers

The panel can propose ready-made entries from the containers running on the
host — with a guessed name, a guessed icon and an address built from the
published port. You tick what belongs on the dashboard, and the whole batch goes
through the same diff preview as any other change.

Point it at a socket proxy with `POST` disabled rather than at the raw Docker
socket. Mounting the socket itself — even read-only — effectively hands whoever
compromises the panel control of the host's Docker.

A container can also describe itself, which beats any guess the panel could
make. Labels are read under the `shyg.` prefix:

```yaml
labels:
  shyg.name: Jellyfin
  shyg.url: https://media.home.arpa
  shyg.icon: jellyfin
  shyg.group: Media
  shyg.enable: "false"   # keep this one out of the suggestions
```

## When something breaks

Set a webhook — Discord, Slack, ntfy, Gotify, Telegram or any URL that accepts
JSON — and you get one short message when a service goes down and one when it
comes back. Nothing else.

This is deliberately **not** alerting: no escalation, no schedules, no rules, no
silence windows. If you want real alerting, run Uptime Kuma — we are not trying
to replace it.

Two details that matter in practice:

- **A failure has to confirm itself.** By default two consecutive failed checks
  are needed before anything is sent, so a single network hiccup does not wake
  you at night. Recovery is reported immediately. `notify.threshold` changes the
  count.
- **The webhook address lives in the database, not in the config file.** The
  file is shown in the UI and committed to git; a webhook URL is a credential.
  Set it in Settings → Notifications, or with `SHYG_WEBHOOK` for a deployment
  that manages its own secrets — the environment wins, so restarting the
  container never quietly undoes a change made in the UI.
- **Messages are sent in the background**, one queue per service, so a slow or
  dead webhook never holds up the check history. A message that could not be
  delivered (three tries) is not marked as reported and is retried on the next
  check.

### Message formats

Pick the format in Settings → Notifications, or with `SHYG_WEBHOOK_FORMAT` /
`--webhook-format` (the environment wins over the panel, exactly like the
address). The format is stored next to the address in the database.

| Format     | Address you enter                                                | What is sent                                                                 |
| ---------- | ---------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `discord`  | `https://discord.com/api/webhooks/<id>/<token>`                  | an embed with a coloured bar (the default)                                   |
| `slack`    | `https://hooks.slack.com/services/…` (Mattermost, Rocket.Chat too) | `{"text": "…"}`                                                              |
| `ntfy`     | `https://ntfy.sh/<topic>` or your own server                     | plain text body, headers `Title`, `Priority` (`high` for down), `Tags`, `Click` |
| `gotify`   | `https://<host>/message?token=<APP_TOKEN>`                       | `{"title", "message", "priority"}` (down = 8)                                |
| `telegram` | `https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<CHAT_ID>` | `{"text": "…"}`, no Markdown parsing; the chat id travels in the address     |
| `generic`  | any https URL                                                    | the JSON below                                                               |

`generic` is a stable contract — fields are never renamed:

```json
{"event": "down", "service": "Jellyfin", "group": "Media", "state": "down",
 "previous": "up", "reason": "connection refused",
 "at": "2026-10-16T12:30:00Z", "url": "http://192.168.1.30:8096"}
```

`event` is `down`, `degraded`, `recovered` or `test`; `state` is the new state
and `previous` the last one that was reported (`up`, `degraded` or `down`);
`reason` is empty for `recovered`. Credentials for ntfy can sit in the address
(`https://user:password@ntfy.example/topic`).

The **Send a test message** button works in every format. Addresses are masked
in the API and the UI (scheme and host only), and a failed test never echoes the
address back — bot tokens live in it. The panel only accepts `https://` addresses;
if your ntfy or Gotify runs on plain `http` inside your LAN, give the address to
the deployment instead: `SHYG_WEBHOOK=http://ntfy.home.arpa/alerts`
`SHYG_WEBHOOK_FORMAT=ntfy` — there the choice belongs to whoever runs it.

## Install

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
      SHYG_DOCKER: http://docker-proxy:2375   # drop this line if you don't want discovery
      TZ: Europe/Warsaw
    volumes:
      - ./shygarlic.yaml:/etc/shygarlic/shygarlic.yaml   # your config, writable
      - shyg-data:/data                                  # accounts, history, icon cache, wallpapers
    healthcheck:
      test: ["CMD", "/shyg", "version"]
      interval: 30s
      timeout: 5s
      retries: 3
    depends_on:
      - docker-proxy

  # Read-only view of the Docker socket: containers can be listed, nothing can
  # be started, stopped or created.
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

Open `http://localhost:8080`.

The image runs as uid **65532**. A named volume inherits the right owner from
the image; a bind mount does not, so give the config file that owner yourself —
with the number, because the host does not know the name `nonroot`:

```bash
chown 65532:65532 ./shygarlic.yaml
```

### Install script

```bash
curl -fsSL https://raw.githubusercontent.com/zatto-dev/shy-garlic/main/install.sh | sh
```

It downloads the release binary for your platform and verifies its SHA-256
against the checksum file published with that same release; a mismatch aborts
the install. `SHYG_VERSION` pins a version, `SHYG_BIN_DIR` picks the target
directory.

```bash
shyg serve -config shygarlic.yaml -data shygarlic.db -addr :8080
```

### From source

```bash
go install github.com/zatto-dev/shy-garlic/cmd/shyg@latest   # Go 1.27
```

### Kubernetes / Talos Linux

Needs a StorageClass that provides a `ReadWriteOnce` volume. Shy Garlic keeps
SQLite and its own YAML config on one volume, so it always runs as a single
replica. The pod meets the Pod Security **restricted** profile (non-root uid
65532, all capabilities dropped, seccomp `RuntimeDefault`), so it is admitted on
Talos's default `baseline` namespaces and on stricter ones.

**Helm** (chart published as an OCI artifact):

```bash
helm install shy-garlic oci://ghcr.io/zatto-dev/charts/shy-garlic \
  --namespace shy-garlic --create-namespace
```

**Plain kubectl** (kustomize, pinned to a release tag):

```bash
kubectl apply -k "github.com/zatto-dev/shy-garlic//deploy/kubernetes?ref=v0.13.1"
```

Then reach the panel and create the administrator on the first visit (see
*First run*):

```bash
kubectl -n shy-garlic port-forward svc/shy-garlic 8080:80    # http://localhost:8080
```

For a permanent address, enable `ingress` or `httpRoute` (Gateway API) in the
chart values and set `trustedProxy` to the proxy's CIDR. All options are
commented in [`charts/shy-garlic/values.yaml`](charts/shy-garlic/values.yaml).
To skip the `/setup` page, point `admin.existingSecret` at a Secret with
`SHYG_ADMIN_USER` and `SHYG_ADMIN_PASSWORD`.

A fresh Talos cluster has **no default StorageClass**, so the volume claim stays
`Pending` and so does the pod. Install one first, for example
[local-path-provisioner](https://github.com/rancher/local-path-provisioner#installation)
(storage on the node's disk; its README has the current manifest). Talos
enforces `baseline` Pod Security by default and the provisioner's helper pods
use `hostPath`, which `baseline` forbids, so label its namespace and make the
class the default:

```bash
kubectl create namespace local-path-storage
kubectl label namespace local-path-storage pod-security.kubernetes.io/enforce=privileged
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.37/deploy/local-path-storage.yaml
kubectl patch storageclass local-path -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
```

If volumes still do not get created on your nodes, see Talos's own
[local storage guide](https://www.talos.dev/latest/kubernetes-guides/configuration/local-storage/).

`helm uninstall` keeps the volume on purpose, so your data survives an accident;
to reinstall onto it, add `--set persistence.existingClaim=shy-garlic`.

Two things differ from Docker. The config lives on the volume
(`/data/config/shygarlic.yaml`): it is seeded from the bundled example on first
start and edited in the panel afterwards, so upgrading never overwrites it. And
there is no Docker socket, so container discovery is simply off; services come
from the config file.

### First run

The first visit lands on `/setup`, where you create the administrator account.
While the database is empty nobody gets any further; once an account exists,
`/setup` is closed.

Creating the account needs the **setup token**, which proves you run the
instance rather than whoever reached the port first. The panel prints it in its
log when it starts:

```bash
docker logs shyg 2>&1 | grep setup_token                      # Docker
kubectl -n shy-garlic logs deploy/shy-garlic | grep setup_token # Kubernetes
```

```text
level=WARN msg="first run: no account exists yet. Open /setup and enter this setup token …" setup_token=3f9c01ab-5d27e4c1-9a0b7d33-c1e8f642
```

Paste it into the *Setup token* field. It lives in memory only: a restart before
the account exists prints a new one, and it is discarded when the account is
created. Setting `SHYG_ADMIN_USER` and `SHYG_ADMIN_PASSWORD` creates the account
at start and skips `/setup` (and the token) altogether.

To make sure the panel only answers to the name you open it by, set
`--allowed-host` / `SHYG_ALLOWED_HOSTS` to a comma-separated list of host names
(no ports), e.g. `SHYG_ALLOWED_HOSTS=dash.example.org,dash.lan`. A request for
any other `Host` gets `421 Misdirected Request`; this stops a malicious web page
from reaching a panel on your network through DNS rebinding. Requests to
`localhost`, `127.0.0.1` and `::1` always work. Empty (the default) means no
check. Behind a reverse proxy the list has to contain the name the proxy passes
on in `Host`; scrapers that open `/metrics` by IP address need that address in
the list too (or must send an allowed `Host` header). `/healthz` is exempt, so
container and Kubernetes probes keep working.

Behind a reverse proxy (Traefik, Caddy, Pangolin) set `--trusted-proxy` /
`SHYG_TRUSTED_PROXY` to its network, e.g. `10.0.0.0/8`. Without it
`X-Forwarded-For` and `X-Forwarded-Proto` are ignored — otherwise one header per
request would be enough to walk around the sign-in rate limiter. The proxy must
also pass the original `Host` header: writing calls are rejected when the
browser's `Origin` does not match it.

## The dashboard

<table>
  <tr>
    <td width="50%"><img src="docs/img/panel-light.png" alt="Light theme"></td>
    <td width="50%"><img src="docs/img/tryb-edycji.png" alt="Edit mode"></td>
  </tr>
  <tr>
    <td align="center"><sub>Light and dark theme</sub></td>
    <td align="center"><sub>Edit mode — add, edit and delete from the UI</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/img/panel-list-dark.png" alt="List view"></td>
    <td width="50%"><img src="docs/img/panel-bento-dark.png" alt="Bento view"></td>
  </tr>
  <tr>
    <td align="center"><sub>List view — for the machine with sixty services</sub></td>
    <td align="center"><sub>Bento view — tiles in three sizes</sub></td>
  </tr>
</table>

The background is either a preset (aurora, nebula, graphite, dawn, fog) or your
own wallpaper, with blur and dimming you set yourself — dimming, because on a
photo with strong internal contrast no choice of text colour rescues legibility.

<p align="center">
  <img src="docs/img/background-picker-dark.png" alt="Background picker" width="100%">
</p>

The picker in the top bar changes **your** browser only. Settings → Appearance
changes the instance default, in the file, through the usual diff. The interface
speaks English and Polish, switched in the header.

### Keyboard

| Key       | Action                          |
| --------- | ------------------------------- |
| `/`       | jump to search                  |
| `E`       | toggle edit mode                |
| `N`       | new service                     |
| `B`       | background picker               |
| `1` `2` `3` | grid, bento, list view        |
| `Esc`     | close, then clear the filter    |

In edit mode, drag the grip on a card or a group header to reorder (mouse or
touch; `Esc` cancels). From the keyboard, focus the grip and press
`Alt`+`↑`/`↓` — the new position is announced to screen readers. The pencil next
to the title edits it in place: `Enter` or leaving the field saves, `Esc` cancels.

## Configuration

```yaml
# This comment survives every edit made through the UI.
title: Homelab

# Shared settings — change once, applies everywhere.
defaults: &defaults
  check:
    interval: 30s
    timeout: 6s
    expect: [200, 301, 302, 401, 403]

# How the instance looks by default. Every browser may override it locally;
# this block is what a browser sees before it decides anything of its own.
appearance:
  background:
    preset: aurora    # aurora | nebula | graphite | dawn | fog | photo
    blur: 10          # 0..30 px, applies to your own wallpaper
    tone: dark        # dark | light — text brightness
  view: grid          # grid | bento | list

groups:
  - name: Media
    services:
      - name: Jellyfin
        url: http://jellyfin.home.arpa:8096
        icon: jellyfin
        size: lg      # sm | md | lg — tile size in the bento view
        description: Movies and music   # shown under the name, and searched
        tags: [media, family]           # a list; searched, chips in list view
        <<: *defaults

  - name: Infrastructure
    services:
      - name: Proxmox
        url: https://proxmox.home.arpa:8006
        icon: proxmox
        check:
          interval: 30s
          expect: [200, 401]
          insecure: true    # self-signed certificate, on purpose

      - name: Grafana
        url: http://grafana.home.arpa:3000
        check:
          path: /api/health               # asked instead of the front page
          contains: '"database": "ok"'    # a body that says otherwise → degraded
```

Content checks run on answers whose status is in `expect`; a wrong status is
reported as `HTTP <code>` without reading the body. Redirects are not followed,
so point `path` at the final address. Only the first 1 MiB is searched.
`check.path` never changes the host: it replaces the path and query of `url`, so
`url` stays what the card links to.

| Field                          | Meaning                                                | Default                     |
| ------------------------------ | ------------------------------------------------------ | --------------------------- |
| `title`                        | dashboard heading                                       | `Shy Garlic`                |
| `groups[].name`                | section name                                            | —                           |
| `services[].name`              | service name                                            | required                    |
| `services[].url`               | address; `http://`, `https://` or `tcp://host:port`     | required                    |
| `services[].icon`              | catalogue icon name, or a full URL                      | first letter of the name    |
| `services[].size`              | tile size in the bento view: `sm`, `md`, `lg`           | `md`                        |
| `services[].description`       | one line under the name; also searched (max 200 chars)  | —                           |
| `services[].tags`              | list of labels, searched; chips in list view (max 12×32) | —                          |
| `appearance.background.preset` | `aurora`, `nebula`, `graphite`, `dawn`, `fog`, `photo`  | `aurora`                    |
| `appearance.background.image`  | file name from `/bg/`, only with `preset: photo`        | —                           |
| `appearance.background.blur`   | wallpaper blur in px, `0`–`30`                          | `10`                        |
| `appearance.background.dim`    | dimming under the content in %, `0`–`80`                | `50`                        |
| `appearance.background.tone`   | text brightness: `dark` or `light`                      | `dark`                      |
| `appearance.view`              | `grid`, `bento` or `list`                               | `grid`                      |
| `check.interval`               | how often to check; 1 s to 24 h, anything else falls back to the default and is flagged in the banner | `30s`                       |
| `check.timeout`                | how long to wait for an answer; 1 s to 24 h, same rule  | `5s`                        |
| `check.expect`                 | status codes that mean "alive"                          | `[200, 301, 302, 401, 403]` |
| `check.insecure`               | ignore certificate errors                               | `false`                     |
| `check.path`                   | path (and query) asked instead of the one in `url`; starts with `/`, same host | —                    |
| `check.contains`               | text that must be in the first 1 MiB of the answer; otherwise `degraded` | —                  |
| `check.not_contains`           | text that must not be there; otherwise `degraded`       | —                           |
| `notify.threshold`             | failed checks in a row before we notify                 | `2`                         |
| `discovery.docker`             | suggest services from containers                        | on when a socket is there   |

### Where each setting lives

Configuration sits in four layers. The line is not drawn by importance but by
**when a value has to be known** and **whether it may end up in git**.

| Layer                 | What                                                                                                | Why here                                                                                                                                                                             |
| --------------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Flags and environment | `--addr`, `--config`, `--seed-config`, `--data`, `--cache`, `--backgrounds`, `--icons`, `--docker`, `--trusted-proxy`, `--metrics-allow`, `--allowed-host`, `--webhook`, `--webhook-format` | has to be known before the server starts, or belongs to the deployment — and `--trusted-proxy` decides who may set `X-Forwarded-For`, so it must not be clickable from the dashboard |
| This file             | `title`, `appearance`, `notify.threshold`, `discovery.docker`, `groups`                             | the product's content: readable, and safe in a commit                                                                                                                                |
| The SQLite database   | webhook address and format, API tokens (hashed), accounts, sessions, history                                                        | **credentials and state** — the database is never committed                                                                                                                          |
| Your browser          | your own appearance override                                                                        | it concerns one browser and nobody else                                                                                                                                              |

Every flag has an `SHYG_`-prefixed environment variable (`SHYG_ADDR`,
`SHYG_CONFIG`, …). Settings → Instance lists all of them together with where
each value came from: a flag, an environment variable, or the built-in default.

`--seed-config` (`SHYG_SEED_CONFIG`) copies a starting config to `--config`
on first start, only when there is no config yet — the Helm chart and the
Kubernetes manifests use it to seed a volume from a ConfigMap. An existing config
is never overwritten.

! **The webhook address is not in the file and never will be.** The dashboard
shows that file and commits it; the URL is a credential.

## Sign-in

<p align="center">
  <img src="docs/img/login.png" alt="Sign-in screen" width="70%">
</p>

Accounts live in SQLite with bcrypt-hashed passwords. The session is an
`HttpOnly`, `SameSite=Strict` cookie valid for 30 days, mutating routes carry a
CSRF token, and five failed attempts from one address lock sign-in for five
minutes. Changing a password is rate-limited the same way. The API answers `401`
without a session. Behind HTTPS (directly or through a trusted proxy) the panel
also sends `Strict-Transport-Security`.

There are no per-user permissions: every account can do everything. What the
panel does and does not defend against, and how it expects to be deployed, is
written down in [SECURITY.md](SECURITY.md) — read it before you expose this to
anything wider than a LAN.

## Metrics and read-only API tokens

Shy Garlic can feed your existing monitoring. **Settings → Instance → API
tokens** creates a token (`shyg_…`, 256 bits of randomness) with a name of your
choice. It is shown once, with a copy button, and stored only as a SHA-256 hash
in the database — if you lose it, revoke it and create another.

A token is **read-only by construction**: the `Authorization: Bearer shyg_…`
header is honoured only for `GET` on three routes — `/metrics`, `/api/status`
and `/api/history`. Everywhere else it is ignored and the route wants a session,
so a token that leaks from your Prometheus cannot change the config, create an
account or mint another token. Requests without a valid token get `401`, never a
redirect to the sign-in page. Failed attempts are rate limited per address and
logged once a minute per address.

```sh
# a quick look, token kept in a file rather than in your shell history
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
    # older versions: bearer_token_file: /etc/prometheus/shyg.token
    static_configs:
      - targets: ["garlic.home.arpa"]
```

| Metric                                         | Meaning                                                                  |
| ---------------------------------------------- | ------------------------------------------------------------------------ |
| `shyg_service_up`                              | `1` when the last check succeeded, `0` when the service is degraded or down |
| `shyg_service_degraded`                        | `1` when it answers, but with the wrong status code or content          |
| `shyg_service_response_seconds`                | response time of the last check; absent when nothing answered           |
| `shyg_service_availability_ratio{window="24h"}` | share of successful checks in 24 hours, `0`–`1`                          |
| `shyg_service_last_check_timestamp_seconds`    | Unix time of the last check                                              |
| `shyg_build_info{version}`                     | always `1`                                                               |

All service metrics carry the labels `group` and `service`. A service that has
not been checked yet (or has no address) has **no series at all** — a `0` would
read as "down" and wake you up for nothing. An alert that fires only on a real
outage: `shyg_service_up == 0 and shyg_service_degraded == 0`.

### Grafana dashboard

Up/degraded/down counters, what is broken right now and since when, every
service with its 24-hour availability, response times and a state timeline per
service:

![Shy Garlic dashboard in Grafana](docs/img/grafana-dashboard.png)

1. In Grafana: **Dashboards → New → Import**.
2. Upload [`deploy/grafana/shy-garlic.json`](deploy/grafana/shy-garlic.json)
   or paste its contents.
3. Pick your Prometheus data source, and in **Prometheus job** enter the
   `job_name` of your scrape config if it is not `shy-garlic` (the one in the
   example above). To change it later: dashboard **Settings → Variables →
   job**.

It uses only built-in panels (stat, table, bar gauge, state timeline, time
series, text) — no plugins to install. Needs Grafana 11.0 or newer.

### Without a token, from your own network

By default `/metrics` needs a token. If Prometheus runs on a trusted network and
you would rather not manage a token, list that network:

```sh
shyg serve --metrics-allow 192.168.0.0/24          # or SHYG_METRICS_ALLOW=192.168.0.0/24,fd00::/8
```

This opens **only** `GET /metrics`, and only for those addresses. Behind a
reverse proxy set `--trusted-proxy` as well: the client address is then taken
from `X-Forwarded-For` as the right-most entry that is not a trusted proxy (the
left-hand entries are written by the client). A request that carries proxy
headers from a peer you did **not** list as a trusted proxy is refused —
otherwise a proxy standing in your LAN would let the whole internet read the
metrics. For the same reason a trusted proxy that sends **no** `X-Forwarded-For`
at all is refused by `--metrics-allow` (every request would look like the
proxy's own address): do not use `--metrics-allow` behind L4 forwarders or
proxies that do not append `X-Forwarded-For` (for example nginx configured with
only `X-Real-IP`). Use a token there; tokens keep working.

### Home Assistant

```yaml
# configuration.yaml — secrets.yaml holds:  shyg_authorization: "Bearer shyg_…"
rest:
  - resource: https://garlic.home.arpa/api/status
    scan_interval: 60
    headers:
      Authorization: !secret shyg_authorization
    sensor:
      - name: "Garlic: services not up"
        value_template: "{{ value_json.values() | rejectattr('state', 'equalto', 'up') | list | length }}"
    binary_sensor:
      - name: "Jellyfin up"
        device_class: connectivity
        value_template: "{{ value_json['Media/Jellyfin'].state == 'up' }}"
```

## API

Everything sits behind the session — except that `/api/status`, `/api/history`
and `/metrics` also accept a [read-only token](#metrics-and-read-only-api-tokens).
The dashboard uses exactly the same endpoints you would.

| Method   | Path                             | What it does                                              |
| -------- | -------------------------------- | --------------------------------------------------------- |
| `GET`    | `/api/status`                    | current state of every service (session or API token)      |
| `GET`    | `/events`                        | SSE stream of state changes                                |
| `GET`    | `/api/history?key=Group/Service` | check history and 24-hour uptime (session or API token)    |
| `GET`    | `/api/service?key=Group/Service` | everything about one service: stats, chart, state changes  |
| `POST`   | `/api/check?key=Group/Service`   | check that service right now                               |
| `GET`    | `/api/info`                      | what this instance has enabled                             |
| `GET`    | `/api/instance`                  | start-up options and where each value came from            |
| `GET`    | `/api/discover`                  | services proposed from running containers                  |
| `GET`    | `/api/icons?q=jelly`             | icon search                                                |
| `GET`    | `/api/config`                    | raw YAML                                                   |
| `PUT`    | `/api/config`                    | write raw YAML (validated first)                           |
| `POST`   | `/api/services`                  | add a service                                              |
| `PATCH`  | `/api/services`                  | change a service                                           |
| `DELETE` | `/api/services?group=…&name=…`   | delete a service                                           |
| `POST`   | `/api/services/move`             | move a service to a position, in or across groups          |
| `POST`   | `/api/groups/move`               | move a group to a position                                 |
| `PUT`    | `/api/title`                     | change the dashboard title                                 |
| `POST`   | `/api/services/bulk`             | add many services as one change                            |
| `POST`   | `/api/groups`                    | add a group                                                |
| `PATCH`  | `/api/groups`                    | rename a group                                             |
| `DELETE` | `/api/groups?name=…`             | delete a group                                             |
| `GET`    | `/api/versions`                  | change history                                             |
| `POST`   | `/api/versions/{id}/restore`     | restore the file to that version                           |
| `GET`    | `/api/appearance`                | the instance default look                                  |
| `PUT`    | `/api/appearance`                | change it (goes through the diff, like any edit)           |
| `GET`    | `/api/notify`                    | notification settings, without the address                 |
| `PUT`    | `/api/notify`                    | set the webhook address, message format and threshold       |
| `POST`   | `/api/notify/test`               | send a test message                                        |
| `POST`   | `/api/background`                | upload a wallpaper (multipart `file`)                      |
| `GET`    | `/api/background`                | list uploaded wallpapers                                   |
| `DELETE` | `/api/background/{name}`         | delete one (`409` while it is in use)                      |
| `GET`    | `/bg/{name}`                     | serve a wallpaper — signed-in only                         |
| `GET`    | `/metrics`                       | Prometheus metrics — session or API token                  |
| `GET`    | `/api/tokens`                    | list API tokens (never the secret itself)                  |
| `POST`   | `/api/tokens`                    | create a token; the secret is in this one response only    |
| `DELETE` | `/api/tokens/{id}`               | revoke a token                                             |
| `GET`    | `/healthz`                       | liveness, the one route that needs no session              |

Account management (`/api/users`, `/api/password`) follows the same rules.

Add `?dry=1` to any writing call and nothing is saved — you get the diff of what
would have changed, which is exactly what the UI uses for its preview.

If somebody changed the file on disk between the read and the write, you get a
`409` instead of silently overwriting their work.

## How it is built

```
cmd/shyg            CLI entry point: serve, check, version
internal/config     config engine — parsing and round-trip-safe writing
internal/health     service checks: scheduler, status semantics, states
internal/i18n       interface texts, English and Polish
internal/server     HTTP, SSE, templates, icons, editor API, sign-in
internal/store      SQLite: accounts, sessions, check history
```

The key decision lives in `internal/config`: **the AST is used for navigation
only**, and writing splices bytes into the original text. That comes from
measurement, not taste — `gopkg.in/yaml.v3` drops blank lines on write and
rewrites `<<: *defaults` into `!!merge <<: *defaults`, and even `goccy/go-yaml`,
which handles this best, collapses the alignment of end-of-line comments. If a
plain read-then-write already touches lines the user never edited, it is not a
round trip. Hence the byte-offset splice.

The frontend is `html/template` plus vanilla JavaScript: no build step, no
`node_modules`, not a single npm package in the product.

## Project status

Versions stay in `0.x` until the project has been through a security review.
Until then the HTTP API and the config file format may still change; every
change that breaks something you relied on is written down in
[CHANGELOG.md](CHANGELOG.md).

Things that are absent on purpose, not by omission:

- **No alerting.** One message down, one message up. Uptime Kuma exists.
- **No per-user permissions.** Every account can do everything.
- **No update check.** The panel never phones home, not even to ask whether a
  newer version exists — silent outbound traffic would contradict the one thing
  this project promises.

## Contributing

Bug reports, patches and translations are welcome. There is no CLA; sign your
commits with the [DCO](CONTRIBUTING.md) (`git commit -s`, which adds a
`Signed-off-by` line). Build and test instructions are in
[CONTRIBUTING.md](CONTRIBUTING.md); security issues go the way
[SECURITY.md](SECURITY.md) describes, not into a public issue.

## Licence

**GNU Affero General Public License v3.0 or later** — see [LICENSE](LICENSE).

Chosen deliberately: if somebody runs a modified Shy Garlic as a service, the
people using it get the source. It does not close any distribution channel we
care about — nixpkgs, TrueNAS apps, Unraid Community Applications and the Umbrel
store all carry AGPL software today.
