# Changelog

All notable changes to Shy Garlic are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/).

Versions stay below `1.0.0` until the project has been through a security
review. Until then a minor bump may still change behaviour you relied on;
the entries below say when it does.

## [Unreleased]

### Security

- **A bad interval or timeout no longer crashes the panel.** A negative,
  sub-second, absurdly large or non-numeric `check.interval` / `check.timeout`
  in the file used to take the whole process down on every start, so a
  container restarted in a loop. Values outside 1 s to 24 h are now replaced
  with the default and named in the config banner; the check loop also guards
  itself against a panic.
- **The first account can only be created by whoever runs the instance.**
  `/setup` now asks for a one-time token that the panel prints in its log at
  start, and the first account is created atomically, so parallel requests can
  no longer produce several administrators. `SHYG_ADMIN_USER` /
  `SHYG_ADMIN_PASSWORD` still skip setup.
- **New `--allowed-host` / `SHYG_ALLOWED_HOSTS`**: the panel answers only to the
  host names you list (everything else gets `421`), which blocks DNS rebinding.
  Empty keeps today's behaviour.
- **Health checks and the webhook can no longer reach link-local addresses**
  (`169.254.0.0/16`, `fe80::/10`, i.e. cloud metadata endpoints). Your own LAN
  stays reachable. "Send test message" no longer shows network error details;
  they go to the log.
- **Passwords inside a service `url:` no longer leak** into notifications, page
  source, the service dialog, `/api/service` and check errors; the banner warns
  about such addresses. Editing a service does not strip them from the file.
- **Expired sessions really expire.** Session expiry was compared as text and
  was off by the server's UTC offset, so in time zones east of UTC a session
  stayed valid for hours after it should have ended. Existing sessions are
  converted on start.
- **Group names with control characters can no longer corrupt the config file**
  when written through the editor, and the API refuses such names for groups and
  services. Adding a group is read back and undone if the file would not load.
- **Deeply nested YAML is rejected** (more than 64 levels) with a clear error
  instead of exhausting memory.
- **HTTP server limits:** header, read and write timeouts and a header size cap,
  at most 8 live-update streams per account, and the wallpaper upload keeps its
  own deadline.
- **The database and its `-wal` / `-shm` files are created private** (`0600`,
  data directory `0700`); existing files are tightened on start.
- **Changing a password is rate-limited** like sign-in.
- **Icon fetching blocks more non-public ranges:** NAT64, 6to4, Teredo,
  `198.18.0.0/15`, `192.0.0.0/24`, `240.0.0.0/4` and `0.0.0.0/8`.
- **Smaller hardening:** `Strict-Transport-Security` over HTTPS, a minimal
  `Permissions-Policy`, no directory listing under `/assets/`, and failed
  sign-ins are logged with a fingerprint of the login instead of the text typed.

### Changed

- **Setup now needs the setup token** from the log (see Security). If you
  script first-run setup against `/setup`, add the `setup_token` field or use
  `SHYG_ADMIN_USER` / `SHYG_ADMIN_PASSWORD`.
- **Go 1.27.** The module, the Docker build and CI use Go 1.27.2; direct
  dependencies are updated (`golang.org/x/crypto` 0.58, `modernc.org/sqlite`
  1.60).
- **The service form and API accept `check.interval` / `check.timeout` between
  1 s and 24 h** (the upper bound is new).

## [0.13.2] — 2026-10-16

### Fixed

- **A malformed config no longer crashes the server.** One line such as
  `expect: !x` made the YAML library panic while loading; it is now an
  ordinary load error shown in the banner.
- **The editor no longer writes into the wrong place.** Every edit is read back
  before it is kept; if the file does not show the new value (unusual layouts,
  lone CR line endings), nothing is saved and the dialog says to edit the file.
  Values that YAML would read as something else, such as `0x10` or `.inf`, are
  now quoted. Description spaces are collapsed on save the same way they are
  on load.
- **Adding services validates the Checking section** like editing does: an
  invalid or sub-second interval/timeout, or a code outside 100–599, returns
  `400` instead of being dropped or saved; a bulk add with one bad entry saves
  nothing.
- **Icon picker requested broken URLs** for icons with dark/light variants
  (two 404s per icon); variants now load from `/icon/…`.

- **Editing a service now saves the "Checking" section** (#6). Ticking "Ignore
  certificate errors", changing accepted codes, interval or timeout on an
  existing service was dropped and the preview said nothing would change; the
  only way out was deleting and re-adding the service. The edit form now loads
  the current values, sends only the fields you changed, and the file gets
  exactly those keys: set in place, added, or removed when you go back to the
  default (an emptied `check:` block disappears). Comments in the block stay.
  A `check` block inherited through `<<: *defaults` is refused with a message,
  as for description and tags. `PATCH /api/services` without these fields
  leaves them untouched; invalid durations, durations under 1s and codes
  outside 100–599 return `400`, and the form rejects a mistyped code instead
  of silently falling back to the defaults. Saving the dialog no longer adds
  `size: md` to a service whose size you did not change.
- **Durations with a trailing comment** such as `interval: 1m  # slow box` were
  read as `1s`. They now read as written.

### Added

- Fuzz tests for the config editor (`FuzzLoadParse`, `FuzzEdit`,
  `FuzzEditValue`) and a short fuzz pass in CI, so a change in the YAML library
  that breaks editing is caught before release.

## [0.13.1] — 2026-10-16

### Changed

- **New Grafana dashboard** (`deploy/grafana/shy-garlic.json`): status tiles,
  "what is wrong now" table, slowest services, every service with 24-hour
  availability, state history and response-time percentiles. Import-ready —
  Grafana asks for the Prometheus datasource and the scrape job name. Built-in
  panels only, Grafana 11.0 or newer. See "Grafana dashboard" in the README.

## [0.13.0] — 2026-10-16

### Added

- **Edit the dashboard title in place.** In edit mode a pencil next to the title
  turns it into a text field: `Enter` or leaving the field saves, `Esc` cancels.
  Empty titles, titles over 80 characters and control characters are rejected
  with a message next to the field. Only the `title` line of the file changes —
  its quoting style and trailing comment stay; when the key is missing it is
  added above `groups:`. Other open tabs pick up the new title live.
- **Reorder by drag and drop.** In edit mode every card and group header has a
  grip: drag a service within its group, into another group at a chosen
  position, or drag a whole group. Works with mouse and touch in grid, bento and
  list views, scrolls the page near the edges, `Esc` cancels. Keyboard:
  `Alt`+arrow keys on the grip, with the new position announced to screen
  readers. The moved block carries the comment above it and any fields the panel
  does not know about; the rest of the file is not touched. A failed save puts
  the card back. Other open tabs reload when the order changes.
  New endpoints: `POST /api/services/move`, `POST /api/groups/move`,
  `PUT /api/title`. Like any edit they appear in the version history.
  You can grab the whole card (or the whole group header), not only the grip:
  with a mouse after ~6 px of movement, with a finger after a 300 ms long press
  so the page still scrolls normally. A plain click still opens the service, and
  the click that ends a drag is swallowed. The drop position follows the pointer:
  left half of a card = before it, right half = after it. Keyboard focus rings
  no longer stay on a card after a mouse or touch interaction.
- **A broken config no longer takes the panel down.** If the file has a YAML
  error at start the panel now comes up anyway (no more restart loop in Docker)
  and shows a banner with the message, line and column. It runs on the newest
  version from the history that still parses, or empty if there is none; the
  file on disk is never touched. A file broken while the panel is running keeps
  the previous configuration, the error is logged once instead of every two
  seconds, and the banner disappears as soon as the file is fixed (live, over
  the event stream). The raw YAML editor and "restore version" work on a broken
  file, so it can be repaired from the UI.
- **Typos in the config are visible.** Unknown keys (`chek:` instead of `check:`)
  used to be ignored without a word. They are now listed in the banner with the
  line number and a "did you mean" hint, and the file still loads. `defaults:`
  and `x-*` top-level keys (anchor holders), anchors and `<<:` merge keys are
  understood. Service addresses that cannot be used are listed too.
- **Description and tags on a service.** Two optional keys, `description:` (one
  line, up to 200 characters) and `tags:` (a list, up to 12 tags of 32
  characters; a single `tags: media` is accepted too). The description shows as
  a subtle line under the host on every card (truncated, full text in the
  tooltip); tags are small chips in list view, on large bento tiles and in the
  details window — grid cards stay uncluttered. The filter box searches both,
  and now ignores diacritics ("zdjecia" finds "zdjęcia"). Both are editable in
  the service dialog; the edit is a splice that adds, replaces or removes only
  those lines, keeping the quoting style, trailing comments and the file's own
  tag style (`[a, b]` or a block list). A value inherited through
  `<<: *defaults` cannot be overridden locally (the YAML parser rejects a
  duplicate key), so the dialog says so instead of writing a broken file.
  `POST`/`PATCH /api/services` take `description` and `tags`; an absent field
  is left alone, an empty one removes it.
- **Content checks.** `check.contains` and `check.not_contains` look for text in
  the first 1 MiB of the answer, and `check.path` asks another path (and query)
  of the same host instead of the one in `url` — e.g. `/api/health` rather than
  the front page. A content mismatch makes the service `degraded` (amber), not
  `down`: the server answers with an accepted status, only the content is wrong.
  The reason (`content: missing "…"` / `content: found "…"`) appears on the
  card tooltip and in the details window, which also shows the rule. A status
  that is not in `expect` still wins and is reported as `HTTP <code>` without
  reading the body. Redirects are not followed. `path` can never change the
  host, and `path: health` (no leading slash) is corrected with a log line.
  Config-only for now: the service dialog does not edit these keys.
- **Prometheus metrics and read-only API tokens.** Settings → Instance → API
  tokens creates a named token (`shyg_` + 256 random bits), shown once with a
  copy button and stored only as a SHA-256 hash, with creation and last-use
  time (written at most once a minute); tokens can be revoked. A token is
  honoured only for `GET /metrics`, `GET /api/status` and `GET /api/history` —
  every other route ignores it and wants a session — and an unauthenticated
  request to those routes gets `401`, never a redirect to sign-in. Failed
  attempts are rate limited per address (60 in 5 minutes, separate from the
  sign-in limit) and logged once a minute per address. `GET /metrics` is hand
  written Prometheus text (`shyg_service_up`, `_degraded`, `_response_seconds`,
  `_availability_ratio{window="24h"}`, `_last_check_timestamp_seconds`,
  `shyg_build_info`, labels `group` and `service`, label values escaped); a
  service that was never checked has no series instead of a misleading `0`.
  `--metrics-allow` / `SHYG_METRICS_ALLOW` lists networks that may read
  `/metrics` without a token (default: none). Behind a proxy the client address
  is the right-most `X-Forwarded-For` entry that is not a trusted proxy, and a
  request with proxy headers from a peer that is not a trusted proxy is refused.
  `deploy/grafana/shy-garlic.json` is a ready dashboard; the README has
  Prometheus, curl and Home Assistant examples. New endpoints: `GET /metrics`,
  `GET`/`POST /api/tokens`, `DELETE /api/tokens/{id}`.
- **Webhook formats.** Besides Discord, notifications can go to Slack
  (Mattermost, Rocket.Chat), ntfy (plain text with `Title`, `Priority`, `Tags`
  and `Click` headers), Gotify, Telegram or any URL as a stable JSON document
  (`{event, service, group, state, previous, reason, at, url}`). The format is
  chosen in Settings → Notifications, stored next to the address in the
  database, and can be fixed by `SHYG_WEBHOOK_FORMAT` / `--webhook-format`,
  which wins over the panel exactly like `SHYG_WEBHOOK` (the panel then answers
  `409`). The address is checked against the format when saved (Telegram needs
  `…/bot<TOKEN>/sendMessage?chat_id=<ID>`, Gotify `…/message?token=<TOKEN>`),
  the test button works in every format, and addresses stay masked in the API —
  including Telegram bot tokens. The panel still accepts `https://` only; plain
  `http` on a LAN goes through `SHYG_WEBHOOK`.
- `./x race` runs the tests with the race detector.

### Fixed

- **Health history survives renames and moves.** Renaming a service, dragging it
  to another group or renaming a group used to orphan its history — the card
  came back with an empty 24-hour bar, and a running outage alerted again. The
  history rows, the monitor's last result and the notifier state now move to the
  new key together with the edit (in one transaction; a failed migration is
  logged and never rolls back the saved YAML).
- **A slow or dead webhook no longer stalls the history writer.** Notifications
  used to be sent from the loop that stores check results, so a failing webhook
  could hold it for about 2.5 seconds per alert. Messages now go out in the
  background, one queue per service (order preserved, at most three requests in
  flight, cancelled on shutdown), with the same "marked as reported only after a
  successful send" rule: a failed alert is retried on the next check, and the
  same alert is never queued twice while one is on its way.
- **A failed test notification no longer shows the webhook address.** The error
  from the HTTP client carries the full URL — and with it the Discord token, the
  Telegram bot token or the Gotify token — into the API response and the log.
- **A failed "down" alert is retried.** The notification state was marked as
  reported before sending, so one failed webhook call lost the alert for good.
  It is now marked after a successful send; each send is tried three times with
  a short back-off and otherwise retried on the next check. Also removes a
  latent deadlock on the `unknown` state.
- **Security hardening.** Service addresses must be `http://`, `https://` or
  `tcp://` (rejected with a message otherwise); only http(s) addresses become
  links, so a `javascript:` URL in the file can no longer run on click. The
  `next=` redirect after login and language switch rejects `/\evil.com`,
  encoded backslashes, control characters and foreign hosts (the language
  switch also stops following a foreign `Referer`). JSON request bodies are
  capped at 1 MiB (2 MiB for the raw YAML editor), forms at 64 KiB. Changing a
  password signs out every other session of that user. Session tokens are
  stored as SHA-256 hashes; existing sessions are migrated, nobody is signed
  out. Editing a `tcp://` service no longer writes `#ZgotmplZ` back to the file.
- **The sign-in rate limiter could be walked around behind a trusted proxy.** It
  keyed on the first (left-most) `X-Forwarded-For` entry, which the client
  writes itself; it now uses the right-most entry that is not a trusted proxy.
- **Races.** The file watcher could reload the config in the middle of a save
  from the panel, and concurrent reloads raced on the health monitor's
  start/stop state (orphaned check loops). Reloads are serialised now.
- **Saving works in more setups.** A config mounted as a single file
  (bind mount, `EBUSY`), in a directory the panel cannot write to (`EACCES`) or
  on another filesystem (`EXDEV`) used to make every edit fail; the panel falls
  back to writing in place after re-checking that the file did not change. A
  symlinked config stays a symlink.
- `examples/homelab.yaml`: the OPNsense service merged the shared defaults one
  level too deep, so they were silently not applied.

## [0.12.0] — 2026-10-15

### Added

- **Kubernetes install.** A Helm chart (`charts/shy-garlic`, published as an
  OCI artifact at `oci://ghcr.io/zatto-dev/charts/shy-garlic` with each
  release) and a kustomize base (`deploy/kubernetes`) for people without Helm.
  Single replica with a persistent volume for the database and the editable
  config, Pod Security "restricted"-compliant, optional Ingress and Gateway API
  HTTPRoute, tested on Talos Linux. See "Kubernetes / Talos Linux" in the README.

### Fixed

- **The container image is public.** `docker pull ghcr.io/zatto-dev/shy-garlic`
  returned `unauthorized` because the package had been published as private
  (#1). The release workflow now pulls the image and the chart anonymously as
  its last step and fails if either is not public.

## [0.11.0] — 2026-09-09

### Added

- **Syntax highlighting in the config editor.** Comments, keys, numbers,
  keywords and YAML anchors each get their own colour, in both themes.
  Written from scratch: the panel ships no JavaScript from a CDN and has no
  build step, so an off-the-shelf editor was never an option. The field stays
  a real `<textarea>`, which keeps undo, paste and selection working the way
  the browser intends.
- **Full-screen editor.** The button next to the file description expands the
  editor to fill the window. The first `Esc` leaves full screen; the second
  closes settings, so a stray keypress cannot discard unsaved edits.

### Changed

- The Go module path is now `github.com/zatto-dev/shy-garlic`, which makes
  `go install` work for everyone rather than only inside the author's network.
- The bundled example configuration uses reserved documentation names
  (`home.arpa`, RFC 8375) instead of one specific person's homelab.

## [0.10.0] — 2026-09-08

The release that made configuration make sense.

### Added

- **The webhook address can be set from the panel.** Until now notifications
  were configurable only through an environment variable, while the panel
  offered a "send a test message" button for a mechanism you could not reach —
  a button over a hole. The address lives in SQLite, never in the config file,
  because that file is displayed in the UI and committed to git, and a webhook
  URL is a credential.
- **Settings → Instance**, listing every start-up flag together with where its
  value came from: a flag, an environment variable, or the built-in default.
- **`appearance.background.dim`** (0–80%) controls the dimming layer under the
  content. On a photo with strong internal contrast, choosing a text colour
  cannot rescue legibility — some cards sit on light areas and some on dark.
  Dimming can.
- **`notify.threshold`** and **`discovery.docker`** in the config file.
- Settings are organised into tabs: Appearance, Notifications, Accounts,
  History, File, Instance.

### Changed

- **Appearance now has two clearly separated layers.** The picker in the top
  bar writes only to your browser; Settings → Appearance writes only to the
  config file. Both say which one they are. Previously three different paths
  wrote to the same block and none of them said who the change would affect.
- One button base replaced eighteen parallel idioms, so hover, focus and
  disabled states finally behave the same everywhere. Toggle state moved from
  a CSS class onto `aria-pressed`, where a screen reader can see it.
- `discovery.docker: false` hides container discovery from the menu, while a
  missing socket merely greys it out — two different causes, two different
  answers.

### Fixed

- Setting the failure threshold on a fresh install returned `400`, because the
  config writer could replace a key but not invent its missing parent.

## [0.9.0] — 2026-08

### Added

- Wallpapers and background presets, self-hosted fonts, CSRF protection on
  mutating routes, and a session cookie set to `SameSite=Strict`.
- Service details: uptime, percentiles, a latency chart and a log of state
  changes.
- Failure notifications through a webhook.
- Service discovery from running containers, with support for a Docker socket
  proxy rather than the bare socket.
- A change preview before saving, version history with one-click restore, and
  a git commit per change when the config file lives in a repository.

## [0.1.0] — 2026-08

First working version: the dashboard, the heartbeat, sign-in, and an editor
that writes YAML back through byte-level splicing, so comments, anchors and
formatting survive every edit made through the UI.

[0.13.2]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.13.2
[0.13.1]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.13.1
[0.13.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.13.0
[0.12.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.12.0
[0.11.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.11.0
[0.10.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.10.0
[0.9.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.9.0
[0.1.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.1.0
