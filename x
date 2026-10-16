#!/usr/bin/env bash
# Shy Garlic — jedyna droga do buildu i testów.
#
# ! Go NIGDY nie wykonuje się na CT111. Uruchomiony gdziekolwiek indziej niż na
# CT101 (agent-worker), skrypt kopiuje drzewo na sandbox i odpala się tam.
# Zmiany w go.mod/go.sum wracają, żeby lokalne drzewo się nie rozjechało.
set -eu

# ! Nadpisywalne, żeby dwa równoległe klony nie kasowały sobie drzewa na
# sandboxie — rsync leci z --delete, więc bez tego druga sesja wywala pierwszą.
REMOTE=${SHYG_REMOTE:-zatto@192.168.0.178}
RDIR=${SHYG_RDIR:-/home/zatto/projects/Shy-Garlic}
cd "$(dirname "$0")"

if [ "$(hostname)" != "agent-worker" ]; then
  rsync -a --delete \
    --exclude '.git/' --exclude 'bin/' --exclude 'web/node_modules/' \
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
  # podgląd stron do zrzutów ekranu: bin/podglad/*.html (assety + ikony obok)
  podglad) go test ./internal/server -run TestPodgladStron -podglad -count=1 -v ;;
  vet)   go vet ./... ;;
  # wyścigi: -race wymaga cgo, więc potrzebny gcc na sandboxie
  race)  CGO_ENABLED=1 go test -race -count=1 ./internal/... ;;
  build) go build -trimpath -ldflags="-s -w" -o bin/shyg ./cmd/shyg && ls -lh bin/shyg ;;
  tidy)  go mod tidy ;;
  run)   shift; go run ./cmd/shyg "$@" ;;
  *)     echo "użycie: ./x [test|race|vet|build|tidy|run|podglad]"; exit 1 ;;
esac
