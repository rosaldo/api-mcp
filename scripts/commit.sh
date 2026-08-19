#!/usr/bin/env bash
# scripts/commit.sh — commit with automatic version bump
#
# Usage:
#   ./commit.sh fix    "what the fix does"    (root shortcut → scripts/commit.sh)
#   ./commit.sh feat   "what the feature does"
#   ./commit.sh major  "what the major release does"
#   ./commit.sh docs   "description" (patch bump)
#   ./commit.sh chore  "description" (patch bump)
set -euo pipefail

# `readlink -f` because the root shortcut is a symlink (./commit.sh → scripts/commit.sh).
# Without it, `dirname` sees "." when called through the link and REPO_DIR lands one directory
# ABOVE the repository — reading the wrong VERSION and committing somewhere else entirely.
REPO_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
VERSION_FILE="${REPO_DIR}/VERSION"

usage() {
  echo "Usage: $0 <fix|feat|major|docs|chore> <message>"
  exit 1
}

# The gate runs BEFORE the bump: a broken commit never becomes a version, and a version never
# becomes a tag on GitHub. These are the three any Go project needs — formatting, static
# analysis and the suite. Fast enough to live in the commit path.
# Emergency escape: APIMCP_SKIP_GATE=1 ./scripts/commit.sh ...
gate() {
  if [[ "${APIMCP_SKIP_GATE:-}" == "1" ]]; then
    echo "⚠️  gate skipped (APIMCP_SKIP_GATE=1)"
    return
  fi

  cd "$REPO_DIR"
  echo "→ gate: gofmt"
  local unformatted
  unformatted="$(gofmt -l .)"
  if [[ -n "$unformatted" ]]; then
    echo "Files not gofmt'd:"
    echo "$unformatted"
    echo "Run: gofmt -w \$(gofmt -l .)"
    exit 1
  fi

  echo "→ gate: go vet"
  go vet ./... || exit 1

  echo "→ gate: go test"
  go test ./... || exit 1

  echo "✓ gate ok"
}

[[ $# -lt 2 ]] && usage

TYPE="$1"
MSG="$2"

VERSION=$(cat "$VERSION_FILE")
MAJOR=$(echo "$VERSION" | cut -d. -f1)
MINOR=$(echo "$VERSION" | cut -d. -f2)
PATCH=$(echo "$VERSION" | cut -d. -f3)

case "$TYPE" in
  fix|security|debug)
    PATCH=$((PATCH + 1)); PREFIX="fix" ;;
  feat|refactor)
    MINOR=$((MINOR + 1)); PATCH=0; PREFIX="$TYPE" ;;
  major)
    MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0; PREFIX="major" ;;
  docs|chore)
    PATCH=$((PATCH + 1)); PREFIX="$TYPE" ;;
  *)
    echo "Unknown type: '$TYPE'. Use: fix|feat|major|docs|chore"
    exit 1 ;;
esac

gate

NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
echo "$NEW_VERSION" > "$VERSION_FILE"
git -C "$REPO_DIR" add "$VERSION_FILE"
COMMIT_MSG="${PREFIX}(v${NEW_VERSION}): ${MSG}"

git -C "$REPO_DIR" commit -m "$COMMIT_MSG"

# Regenerate the CHANGELOG (it already includes this release, since it reads the commit above)
# and fold it into the SAME commit BEFORE tagging — so the tag points at a commit whose
# CHANGELOG.md is already up to date, without an extra commit.
"${REPO_DIR}/scripts/gen-changelog.sh"
git -C "$REPO_DIR" add CHANGELOG.md
git -C "$REPO_DIR" commit --amend --no-edit >/dev/null

git -C "$REPO_DIR" tag -a "v${NEW_VERSION}" HEAD -m "Release v${NEW_VERSION}"
echo "Tag created: v${NEW_VERSION}"
echo "Commit: ${COMMIT_MSG}"
