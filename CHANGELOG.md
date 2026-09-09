# Changelog

All notable changes to Shy Garlic are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/).

Versions stay below `1.0.0` until the project has been through a security
review. Until then a minor bump may still change behaviour you relied on;
the entries below say when it does.

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

[0.11.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.11.0
[0.10.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.10.0
[0.9.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.9.0
[0.1.0]: https://github.com/zatto-dev/shy-garlic/releases/tag/v0.1.0
