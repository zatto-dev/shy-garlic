# Security policy

## Reporting a vulnerability

Report it privately through
[GitHub Security Advisories](https://github.com/zatto-dev/shy-garlic/security/advisories/new).
That keeps the report between us until there is something to upgrade to.

Please do not open a public issue for a vulnerability.

What helps: the version (`shyg version`), how the panel is exposed (directly,
behind a reverse proxy, behind SSO), and the smallest set of steps that shows
the problem. A proof of concept is welcome but not required — a clear
description of the flaw is enough to start.

Expect a first reply within 72 hours. If a fix is warranted you will be
credited in the release notes, unless you would rather not be.

## Supported versions

Only the latest release is supported. The project is below `1.0.0` and has not
yet been through an external security review; fixes land in a new version
rather than in patches to old ones.

## What the panel assumes about its environment

Knowing where the boundary is drawn saves everyone time.

- **The panel is not a public-internet application.** It is meant for a home
  network or behind a reverse proxy you control. Sign-in exists so a browser
  left open is not an open door, not so the panel can be exposed to the world.
- **Whoever can sign in can edit the config file.** That is the product: the
  UI is an editor for that file. There are no per-user permissions, and adding
  an account grants the same rights as the account that created it.
- **`--trusted-proxy` decides who may set `X-Forwarded-For`.** Leave it empty
  unless a proxy really sits in front of the panel. Setting it too widely lets
  a client forge its own address, which affects both the log and the sign-in
  rate limit. It is deliberately a start-up flag and not something the panel
  can change about itself.
- **API tokens are read-only and live in the database.** A token (`shyg_…`) is
  honoured only for `GET` on `/metrics`, `/api/status` and `/api/history`; every
  other route ignores it and requires a session. The database stores a SHA-256
  hash, never the token. `--metrics-allow` lets listed networks read `/metrics`
  without a token — use it only for networks you trust, and set
  `--trusted-proxy` when a proxy sits in front (the address is then the
  right-most `X-Forwarded-For` entry that is not a trusted proxy; a request
  with proxy headers from an unlisted peer is refused).
- **The Docker socket is read-only by design.** The bundled compose file puts
  a socket proxy in front of it with `POST` disabled, so discovery can list
  containers and nothing else. Handing the panel the bare socket gives it the
  ability to control the host; do that only if you understand the trade.
- **Icons never reach your browser from a CDN.** The server fetches and caches
  them, so no third party learns which services you run. `--icons icons.zip`
  removes even the server's outbound requests.
- **The config file is not a place for secrets.** The panel displays it and
  commits it to git when it lives in a repository. The webhook address is
  therefore kept in the database instead; anything else you would not want in
  a commit should stay out of that file too.

## What is known and not yet done

- No external security review has been carried out. That is the reason the
  version number still starts with a zero.
- There is no rate limit on the API beyond sign-in and failed API tokens.
- Sessions are not invalidated when a password changes.
