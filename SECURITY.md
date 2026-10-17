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

Only the latest `0.x` release is supported. The project is below `1.0.0` and
has not yet been through an external security review; fixes land in a new
version rather than in patches to old ones, so please reproduce a problem on the
latest release before reporting it.

## What the panel assumes about its environment

Knowing where the boundary is drawn saves everyone time.

- **The panel is not a public-internet application.** It is meant for a home
  network or behind a reverse proxy you control. Sign-in exists so a browser
  left open is not an open door, not so the panel can be exposed to the world.
- **Whoever can sign in can edit the config file.** That is the product: the
  UI is an editor for that file. There are no per-user permissions, and adding
  an account grants the same rights as the account that created it.
- **First run is protected by a setup token.** A fresh instance has no
  accounts, and `/setup` would make whoever reached it first the administrator,
  and the administrator can edit the config file. So the panel prints a random,
  one-time setup token in its log at start and refuses `/setup` without it
  (wrong tokens count against the sign-in rate limit). The token is kept in
  memory only and is dropped when the account is created. `SHYG_ADMIN_USER` /
  `SHYG_ADMIN_PASSWORD` create the account at start and skip setup. The first
  account is created in one database statement, so simultaneous requests cannot
  produce several administrators.
- **`--allowed-host` guards against DNS rebinding.** A web page you visit can
  make your browser talk to a panel on your own network under the page's name.
  With `--allowed-host` / `SHYG_ALLOWED_HOSTS` set to the names you actually use,
  requests for any other `Host` get `421`. Empty means no check (the default).
  Loopback names always pass. If you expose the panel beyond the LAN, set it.
- **Whoever can sign in can make the panel request addresses on your network.**
  Health checks, `check.path`, `tcp://` targets and the webhook go wherever the
  config says, and private LAN addresses are the point of the product, so they
  are allowed. Link-local addresses (`169.254.0.0/16`, `fe80::/10`, which carry
  cloud metadata services) are blocked for health checks and the webhook, and
  icon fetching refuses every private, loopback, link-local and tunnelling range.
  With `HTTP_PROXY`/`HTTPS_PROXY` set, the webhook goes through that proxy and
  the guard sees the proxy's address, not the target's; that is deliberate (a
  corporate proxy filters on its own). The block also covers the OCI
  (`192.0.0.192`) and Azure (`168.63.129.16`) metadata addresses.
  The failure text of "send test message" is deliberately generic; the details
  go to the log. Treat a panel account like shell access to the network the
  panel sits in.
- **Credentials in a service `url:` (`http://user:pass@host`) are not hidden in
  the file**, because the file is shown and committed (see below). The panel
  strips them from notifications, from the page, from `/api/service` and from
  check errors, and the config banner warns about each one. The health check
  itself still uses them. Prefer a health endpoint that needs no
  credentials, and keep the file out of public repositories.
- **Writing requests must come from the panel's own origin.** `Sec-Fetch-Site`
  (or, for clients without it, `Origin` / `Referer`) has to say same-origin, and
  the session cookie is `HttpOnly` and `SameSite=Strict`. There is no CSRF token;
  a reverse proxy therefore has to pass the original `Host` header.
- **`--trusted-proxy` decides who may set `X-Forwarded-For`.** Leave it empty
  unless a proxy really sits in front of the panel. Setting it too widely lets
  a client forge its own address, which affects both the log and the sign-in
  rate limit. It is deliberately a start-up flag and not something the panel
  can change about itself.
- **API tokens are read-only and live in the database.** A token (`shyg_…`) is
  honored only for `GET` on `/metrics`, `/api/status` and `/api/history`; every
  other route ignores it and requires a session. The database stores a SHA-256
  hash, never the token. `--metrics-allow` lets listed networks read `/metrics`
  without a token — use it only for networks you trust, and set
  `--trusted-proxy` when a proxy sits in front (the address is then the
  right-most `X-Forwarded-For` entry that is not a trusted proxy; a request
  with proxy headers from an unlisted peer is refused, and so is a trusted
  proxy that sends no `X-Forwarded-For`). Do not use `--metrics-allow` behind
  L4 forwarders or proxies that do not append `X-Forwarded-For`; use a token.
- **The Docker socket is read-only by design.** The compose example in
  `deploy/compose/docker-compose.yml` puts a socket proxy in front of it with
  `POST` disabled, so discovery can list containers and nothing else. Handing the panel the bare socket gives it the
  ability to control the host; do that only if you understand the trade.
- **The container runs unprivileged.** Both image variants run as uid 65532; the
  default one is distroless (no shell, no `git`). The database and its `-wal` /
  `-shm` files are created `0600` in a `0700` directory. Commits of the config to
  git work only in the `-git` image or on a host that has git.
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
- There is no rate limit on the API beyond sign-in, password change and failed
  API tokens. Each account may hold at most 8 live-update (`/events`) streams
  open at once.
- Sign-in has a global cap of 30 failed attempts per minute on top of the
  per-address limit (IPv6 counted per /64). An attacker spread over many
  addresses can keep that cap tripped and so block new sign-ins, including the
  owner's; existing sessions and API tokens keep working.
- Changing a password signs out every other session of that account (the one
  you are using stays). API tokens are **not** revoked on a password change;
  revoke them under API tokens. Deleting an account does revoke the tokens it
  issued.
