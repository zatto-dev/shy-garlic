#!/bin/sh
# Prints the CHANGELOG.md section of one version (without its heading) to stdout.
# Usage: changelog-notes.sh X.Y.Z [CHANGELOG.md]
# Fails (non-zero) when there is no `## [X.Y.Z]` section or it is empty, so a
# release cannot go out without notes. The link-reference block at the end of
# the file ([X.Y.Z]: https://...) is not part of any section and is dropped.
set -eu
version="${1:?usage: changelog-notes.sh X.Y.Z [CHANGELOG.md]}"
file="${2:-CHANGELOG.md}"

notes=$(awk -v v="$version" '
  /^## \[/ {
    if (on) exit
    if (index($0, "## [" v "]") == 1) { on = 1; next }
  }
  on && /^\[[^]]+\]: https?:\/\// { next }
  on { print }
' "$file")

# blank-only (or missing) section = not a release note
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "::error file=${file}::no non-empty section \"## [${version}]\" in ${file}" >&2
  exit 1
fi
printf '%s\n' "$notes"
