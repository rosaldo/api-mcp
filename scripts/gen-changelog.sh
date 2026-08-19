#!/usr/bin/env bash
# scripts/gen-changelog.sh — builds CHANGELOG.md from git.
#
# Source: the release commits made by commit.sh, whose subject is `type(vX.Y.Z): message`.
# Do NOT edit CHANGELOG.md by hand — run this.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/CHANGELOG.md"

{
  echo "# Changelog"
  echo
  echo "> Generated from \`git\` by \`scripts/gen-changelog.sh\` — do not edit by hand."
  echo
  # The `|| true` covers a repository with no releases yet: without it a grep that matches
  # nothing trips `pipefail`, and the project's first commit.sh fails on the changelog.
  { git -C "$ROOT" log --pretty=$'%cs\t%s' \
    | grep -E $'\t(feat|fix|refactor|docs|chore|major)\\(v[0-9]' || true; } \
    | while IFS=$'\t' read -r date subject; do
        type="${subject%%(*}"
        ver="${subject#*\(}"; ver="${ver%%)*}"
        msg="${subject#*): }"
        printf -- '- **%s** (%s) · `%s` — %s\n' "$ver" "$date" "$type" "$msg"
      done
} > "$OUT"

echo "CHANGELOG.md written: $(grep -c '^- ' "$OUT" || true) entries."
