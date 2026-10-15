# Contributing

## Sign your work — the Developer Certificate of Origin

There is no CLA here. Instead we use the [Developer Certificate of Origin](https://developercertificate.org/):
add a `Signed-off-by` line to every commit, which `git commit -s` does for you.

```
Signed-off-by: Your Name <you@example.com>
```

That line means you wrote the patch or otherwise have the right to submit it under
the project's licence. Nothing more is asked of you, and copyright stays yours.

## Licence

Shy Garlic is licensed under the **GNU Affero General Public License v3.0 or later**.
Contributions are accepted under the same terms. AGPL was chosen deliberately: if
somebody runs a modified Shy Garlic as a service, the people using it get the source.

## Building

Everything builds and tests on the sandbox machine — `./x` refuses to run Go
anywhere else and rsyncs the tree over by itself.

```bash
./x test      # tests
./x vet       # go vet
./x build     # bin/shyg
./x podglad   # renders the pages to bin/podglad/ for offline screenshots
```

`./x podglad` runs one test behind the `-podglad` flag: it renders `index.html`
and `login.html` from `examples/homelab.yaml` into `bin/podglad/*.html` together
with the assets and the icons, with every `/assets/…` and `/icon/…` path rewritten
to a relative one. Open the files straight from disk — no server, no sign-in — or
shoot them headless:

```bash
google-chrome --headless --disable-gpu --force-prefers-reduced-motion \
  --hide-scrollbars --window-size=1440,900 \
  --blink-settings=primaryHoverType=2,availableHoverTypes=2 \
  --screenshot=panel.png "file://$PWD/bin/podglad/index-grid-ciemny.html"
```

The `--blink-settings` part matters: without it headless reports a touch screen,
`@media (hover: none)` kicks in and every card shows its hover actions. Headless
also refuses to lay out below 500 CSS px, so phone-width shots have to go through
an `<iframe>` of the wanted width.

A change is done when `./x test` is green. If you touch the config engine, add a
test that proves the write still leaves untouched lines untouched — that is the
whole point of this project.

## Releasing

Pushing a `v*` tag runs `.github/workflows/release.yml`: binaries, GitHub Release
and the `ghcr.io/zatto-dev/shy-garlic` image.

- **The GHCR package must be public.** A package first published by a workflow in
  an organisation is created *private*, and GitHub has no API to change that. Once,
  per package: Package settings → Danger Zone → Change visibility → Public. The
  last release job pulls the image anonymously and fails with a clear message if
  the package is still private.
