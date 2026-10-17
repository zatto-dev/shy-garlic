#!/usr/bin/env bash
# Shy Garlic — the one way to build and test.
#
# Runs Go right here unless a build host is configured. With SHYG_BUILD_HOST
# set and this machine being a different host, it syncs the tree to the build
# host over ssh and runs there; go.mod/go.sum changes come back so the local
# tree does not drift. Settings come from the environment or from an
# untracked .x.local next to this script:
#   SHYG_BUILD_HOST  hostname of the machine that runs Go
#   SHYG_REMOTE      its ssh target (user@host)
#   SHYG_RDIR        the directory on it to sync into
set -eu
cd "$(dirname "$0")"
if [ -f .x.local ]; then . ./.x.local; fi

if [ -n "${SHYG_BUILD_HOST:-}" ] && [ "$(hostname)" != "$SHYG_BUILD_HOST" ]; then
  REMOTE=${SHYG_REMOTE:?set SHYG_REMOTE (user@host) in .x.local}
  # ! a separate RDIR per clone: rsync runs with --delete, so two clones
  # syncing into one directory would wipe each other's tree
  RDIR=${SHYG_RDIR:?set SHYG_RDIR in .x.local}
  rsync -a --delete \
    --exclude '.git/' --exclude 'bin/' --exclude 'web/node_modules/' --exclude '.x.local' \
    ./ "$REMOTE:$RDIR/"
  set +e
  ssh "$REMOTE" "cd $RDIR && ./x $*"
  rc=$?
  set -e
  rsync -a "$REMOTE:$RDIR/go.mod" "$REMOTE:$RDIR/go.sum" ./ 2>/dev/null || true
  exit $rc
fi

export PATH=/usr/local/go/bin:$PATH

case "${1:-test}" in
  test)  go test ./... ;;
  # page preview for screenshots: bin/podglad/*.html (assets + icons alongside)
  podglad) go test ./internal/server -run TestPreviewPages -podglad -count=1 -v ;;
  vet)   go vet ./... ;;
  # races: -race needs cgo, so gcc is required on the sandbox
  race)  CGO_ENABLED=1 go test -race -count=1 ./internal/... ;;
  build) go build -trimpath -ldflags="-s -w" -o bin/shyg ./cmd/shyg && ls -lh bin/shyg ;;
  tidy)  go mod tidy ;;
  run)   shift; go run ./cmd/shyg "$@" ;;
  *)     echo "usage: ./x [test|race|vet|build|tidy|run|podglad]"; exit 1 ;;
esac
