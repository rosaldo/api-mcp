#!/usr/bin/env bash
# push.sh — ships the work: commits, this version's tag and the GitHub Release.
#
# Usage:  ./push.sh          (root shortcut → this file, same as ./commit.sh)
#
# What CLOSES a version is ./commit.sh (bump, changelog, tag). This script only
# publishes it. The expensive checks run BEFORE anything is pushed, so the script fails without
# leaving half the work out there.
#
# No binaries are attached: users install with `go install`, and publishing binaries would mean
# maintaining six targets (linux/mac/windows × amd64/arm64) nobody has asked for. The day
# somebody does, this is where they go.
#
# Requires: `gh` authenticated with write access to the repo
#   gh auth login
set -euo pipefail

ROOT="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()   { printf '    \033[0;32m✓\033[0m %s\n' "$*"; }
die()  { printf '\n\033[0;31m!! %s\033[0m\n' "$*" >&2; exit 1; }

TAG_VERSION="$(cat "$ROOT/VERSION" 2>/dev/null)" && [[ -n "$TAG_VERSION" ]] \
  || die "VERSION missing in $ROOT"
TAG="v${TAG_VERSION}"

# --- 0. checks, all of them before anything is pushed -----------------------

# A dirty tree means the tag would not match what gets published.
[[ -z "$(git -C "$ROOT" status --porcelain)" ]] \
  || die "uncommitted changes — close the version with ./commit.sh before publishing"

git -C "$ROOT" rev-parse "$TAG" >/dev/null 2>&1 \
  || die "tag ${TAG} does not exist — ./commit.sh creates it alongside the release commit"

command -v gh >/dev/null 2>&1 || die "gh (GitHub CLI) is not installed — https://cli.github.com"

# A REAL credential check: a query against the repo, not a grep in a config file.
log "checking GitHub credentials"
gh repo view --json name >/dev/null 2>&1 || die \
"no access to the repository through gh.
    Authenticate with:
        gh auth login"
ok "credentials ok"

# --- 1. git -----------------------------------------------------------------
log "git push"
git -C "$ROOT" push

# ONLY this version's tag, never `--tags`.
#
# `--tags` pushes every pending tag at once, and each one triggers a CI job. With two going up
# together the jobs run in parallel and the "Latest" label lands on whichever finishes last —
# which may well be the LOWER version. One tag per release, in order.
log "git push of tag ${TAG}"
git -C "$ROOT" push origin "$TAG"

# --- 2. the Release ---------------------------------------------------------
# Idempotent: it exists → update the notes; it does not → create it.
#
# `--latest` is EXPLICIT on both paths. Without it GitHub decides by DATE, and that decision is
# wrong whenever two releases go out together — the same accident described above.
NOTES="$(git -C "$ROOT" tag -l "$TAG" --format='%(contents)')"
log "publishing Release ${TAG}"
if gh release view "$TAG" >/dev/null 2>&1; then
  gh release edit "$TAG" --notes "$NOTES" --latest >/dev/null \
    || die "could not update Release ${TAG}"
  ok "Release ${TAG} updated and marked Latest"
else
  gh release create "$TAG" --title "$TAG" --notes "$NOTES" --latest >/dev/null \
    || die "could not create Release ${TAG}"
  ok "Release ${TAG} published and marked Latest"
fi

log "done"
ok "$(git -C "$ROOT" rev-parse --short HEAD) · ${TAG}"
