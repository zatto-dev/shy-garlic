# Contributing

Thanks for helping out. This page covers how to build, what a good change looks
like, and how to sign your work.

## Licence

Shy Garlic is licensed under the **GNU Affero General Public License v3.0 or later**.
Contributions are accepted under the same terms. AGPL was chosen deliberately: if
somebody runs a modified Shy Garlic as a service, the people using it get the source.

## Sign your work: the Developer Certificate of Origin

There is no CLA. Instead we use the [Developer Certificate of Origin](https://developercertificate.org/) (DCO):
add a `Signed-off-by` line to every commit, which `git commit -s` does for you.

```
Signed-off-by: Your Name <you@example.com>
```

That line certifies that you wrote the patch or otherwise have the right to submit
it under the project's licence. Nothing more is asked of you, and copyright stays
yours. A CI check fails pull requests that contain unsigned commits. To fix
them, run `git rebase --signoff origin/main` and force-push your branch.

## Building and testing

You need **Go 1.27**. The plain Go tooling works:

```bash
go test ./...
go vet ./...
```

`./x` is a thin wrapper that runs the same commands:

```bash
./x test      # go test ./...
./x race      # go test -race (needs cgo, so a C compiler)
./x vet       # go vet
./x build     # bin/shyg
./x tidy      # go mod tidy
./x run ...   # go run ./cmd/shyg ...
./x podglad   # renders the pages to bin/podglad/ for offline screenshots
```

By default `./x` runs Go on your machine. If you prefer to build elsewhere, set a
build host in an untracked `.x.local` next to the script; `./x` then rsyncs the
tree there over ssh and runs Go on that machine:

```bash
SHYG_BUILD_HOST=buildbox          # hostname of the machine that runs Go
SHYG_REMOTE=you@buildbox          # its ssh target
SHYG_RDIR=/home/you/shyg-build    # directory to sync into (one per clone: rsync uses --delete)
```

`./x podglad` runs one test behind the `-podglad` flag: it renders `index.html`
and `login.html` from `examples/homelab.yaml` into `bin/podglad/*.html` together
with the assets and the icons, with every `/assets/...` and `/icon/...` path rewritten
to a relative one. Open the files straight from disk, with no server and no sign-in.

## What a good change looks like

- **Tests are required.** A change is done when `go test ./...` is green and the
  change is covered by a test. If you touch the config engine, add a test that
  proves the write still leaves untouched lines untouched; that is the whole
  point of this project.
- **English in the code.** Code, identifiers, comments and commit messages are in
  English.
- **UI text goes through i18n.** Never hard-code user-facing strings. Add the key
  to `internal/i18n` with both an English and a Polish translation.
- Keep pull requests focused: one concern per PR, with a clear description.

## Security issues

Do not open a public issue for a vulnerability. Follow [SECURITY.md](SECURITY.md)
to report it privately.

## For maintainers

### Releasing

1. Open a PR that bumps the version in `CHANGELOG.md`, `charts/shy-garlic/Chart.yaml`
   (`appVersion`) and the image tag and `ref` in `deploy/kubernetes/kustomization.yaml`,
   and merge it once CI is green.
2. Tag the merge commit and push the tag:

   ```bash
   git tag vX.Y.Z && git push origin vX.Y.Z
   ```

3. Pushing a `v*` tag runs `.github/workflows/release.yml`: binaries, the GitHub
   Release, the `ghcr.io/zatto-dev/shy-garlic` image and the Helm chart at
   `oci://ghcr.io/zatto-dev/charts/shy-garlic`. The chart version and appVersion
   are taken from the tag.

**The GHCR packages must be public.** A package first published by a workflow in
an organisation is created *private*, and GitHub has no API to change that. Once
per package (the image and the chart): Package settings -> Danger Zone -> Change
visibility -> Public. The release jobs pull anonymously and fail with a clear
message if a package is still private.
