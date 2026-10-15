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
exact diff and waits for your confirmation.

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

Set a Discord webhook (or anything else that accepts JSON) and you get one
sentence when a service goes down and one when it comes back. Nothing else.

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
go install github.com/zatto-dev/shy-garlic/cmd/shyg@latest   # Go 1.25
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
kubectl apply -k "github.com/zatto-dev/shy-garlic//deploy/kubernetes?ref=v0.12.0"
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
```

| Field                          | Meaning                                                | Default                     |
| ------------------------------ | ------------------------------------------------------ | --------------------------- |
| `title`                        | dashboard heading                                       | `Shy Garlic`                |
| `groups[].name`                | section name                                            | —                           |
| `services[].name`              | service name                                            | required                    |
| `services[].url`               | address; `http://`, `https://` or `tcp://host:port`     | required                    |
| `services[].icon`              | catalogue icon name, or a full URL                      | first letter of the name    |
| `services[].size`              | tile size in the bento view: `sm`, `md`, `lg`           | `md`                        |
| `appearance.background.preset` | `aurora`, `nebula`, `graphite`, `dawn`, `fog`, `photo`  | `aurora`                    |
| `appearance.background.image`  | file name from `/bg/`, only with `preset: photo`        | —                           |
| `appearance.background.blur`   | wallpaper blur in px, `0`–`30`                          | `10`                        |
| `appearance.background.dim`    | dimming under the content in %, `0`–`80`                | `50`                        |
| `appearance.background.tone`   | text brightness: `dark` or `light`                      | `dark`                      |
| `appearance.view`              | `grid`, `bento` or `list`                               | `grid`                      |
| `check.interval`               | how often to check                                      | `30s`                       |
| `check.timeout`                | how long to wait for an answer                          | `5s`                        |
| `check.expect`                 | status codes that mean "alive"                          | `[200, 301, 302, 401, 403]` |
| `check.insecure`               | ignore certificate errors                               | `false`                     |
| `notify.threshold`             | failed checks in a row before we notify                 | `2`                         |
| `discovery.docker`             | suggest services from containers                        | on when a socket is there   |

### Where each setting lives

Configuration sits in four layers. The line is not drawn by importance but by
**when a value has to be known** and **whether it may end up in git**.

| Layer                 | What                                                                                                | Why here                                                                                                                                                                             |
| --------------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Flags and environment | `--addr`, `--config`, `--data`, `--cache`, `--backgrounds`, `--icons`, `--docker`, `--trusted-proxy` | has to be known before the server starts, or belongs to the deployment — and `--trusted-proxy` decides who may set `X-Forwarded-For`, so it must not be clickable from the dashboard |
| This file             | `title`, `appearance`, `notify.threshold`, `discovery.docker`, `groups`                             | the product's content: readable, and safe in a commit                                                                                                                                |
| The SQLite database   | webhook address, accounts, sessions, history                                                        | **credentials and state** — the database is never committed                                                                                                                          |
| Your browser          | your own appearance override                                                                        | it concerns one browser and nobody else                                                                                                                                              |

Every flag has an `SHYG_`-prefixed environment variable (`SHYG_ADDR`,
`SHYG_CONFIG`, …). Settings → Instance lists all of them together with where
each value came from: a flag, an environment variable, or the built-in default.

! **The webhook address is not in the file and never will be.** The dashboard
shows that file and commits it; the URL is a credential.

## Sign-in

<p align="center">
  <img src="docs/img/login.png" alt="Sign-in screen" width="70%">
</p>

Accounts live in SQLite with bcrypt-hashed passwords. The session is an
`HttpOnly`, `SameSite=Strict` cookie valid for 30 days, mutating routes carry a
CSRF token, and five failed attempts from one address lock sign-in for five
minutes. The API answers `401` without a session.

There are no per-user permissions: every account can do everything. What the
panel does and does not defend against, and how it expects to be deployed, is
written down in [SECURITY.md](SECURITY.md) — read it before you expose this to
anything wider than a LAN.

## API

Everything sits behind the session. The dashboard uses exactly the same
endpoints you would.

| Method   | Path                             | What it does                                              |
| -------- | -------------------------------- | --------------------------------------------------------- |
| `GET`    | `/api/status`                    | current state of every service                             |
| `GET`    | `/events`                        | SSE stream of state changes                                |
| `GET`    | `/api/history?key=Group/Service` | check history and 24-hour uptime                           |
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
| `POST`   | `/api/services/bulk`             | add many services as one change                            |
| `POST`   | `/api/groups`                    | add a group                                                |
| `PATCH`  | `/api/groups`                    | rename a group                                             |
| `DELETE` | `/api/groups?name=…`             | delete a group                                             |
| `GET`    | `/api/versions`                  | change history                                             |
| `POST`   | `/api/versions/{id}/restore`     | restore the file to that version                           |
| `GET`    | `/api/appearance`                | the instance default look                                  |
| `PUT`    | `/api/appearance`                | change it (goes through the diff, like any edit)           |
| `GET`    | `/api/notify`                    | notification settings, without the address                 |
| `PUT`    | `/api/notify`                    | set the webhook address                                    |
| `POST`   | `/api/notify/test`               | send a test message                                        |
| `POST`   | `/api/background`                | upload a wallpaper (multipart `file`)                      |
| `GET`    | `/api/background`                | list uploaded wallpapers                                   |
| `DELETE` | `/api/background/{name}`         | delete one (`409` while it is in use)                      |
| `GET`    | `/bg/{name}`                     | serve a wallpaper — signed-in only                         |
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
