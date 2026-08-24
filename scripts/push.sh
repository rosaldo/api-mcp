#!/usr/bin/env bash
# push.sh — ships the work: the commits and this version's tag.
#
# Usage:
#   ./push.sh              push the commits and the tag; CI builds and publishes the Release
#   ./push.sh --full       ALSO build the binaries here and upload them (-f works too)
#
# What CLOSES a version is ./commit.sh (bump, changelog, tag). This script only ships it.
#
# By default nothing is built here. Pushing the tag starts .github/workflows/release.yml, which
# cross-compiles and publishes — and on a public repository that costs nothing, so there is no
# reason to spend a workstation's time on it or to make the artefact depend on whichever Go
# version that machine has.
#
# `--full` is the escape hatch: it builds locally and uploads, for when the workflow cannot run
# (GitHub Actions down, or a release that has to go out from here). The tag still triggers the
# workflow, which will overwrite the assets with its own build — same inputs, same output.
#
# Requires: `gh` authenticated with write access to the repo (only with --full)
#   gh auth login
set -euo pipefail

ROOT="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()  { printf '    \033[0;32m✓\033[0m %s\n' "$*"; }
die() { printf '\n\033[0;31m!! %s\033[0m\n' "$*" >&2; exit 1; }

FULL=0
for arg in "$@"; do
  case "$arg" in
    -f|--full) FULL=1 ;;
    *) die "unknown argument: $arg (usage: $0 [--full])" ;;
  esac
done

TAG_VERSION="$(cat "$ROOT/VERSION" 2>/dev/null)" && [[ -n "$TAG_VERSION" ]] \
  || die "VERSION missing in $ROOT"
TAG="v${TAG_VERSION}"

# --- 0. checks, all of them before anything is pushed -----------------------

# A dirty tree means the tag would not match what gets published.
[[ -z "$(git -C "$ROOT" status --porcelain)" ]] \
  || die "uncommitted changes — close the version with ./commit.sh before publishing"

git -C "$ROOT" rev-parse "$TAG" >/dev/null 2>&1 \
  || die "tag ${TAG} does not exist — ./commit.sh creates it alongside the release commit"

if [[ $FULL -eq 1 ]]; then
  command -v gh >/dev/null 2>&1 || die "gh (GitHub CLI) is not installed — https://cli.github.com"
  command -v go >/dev/null 2>&1 || die "go is not installed — --full builds the binaries here"

  # A REAL credential check: it asks for WRITE permission, not just whether the repo can be read.
  #
  # Reading is not the question — the repository is public, so any account can do it, including
  # one that cannot publish a thing. Checking only for read is how commits and tag go up and the
  # Release does not: `gh` authenticated as another account, and the failure surfacing at the
  # last step with everything already public.
  #
  # The error `gh` reports in that case blames the "workflow" scope, which sends you off
  # refreshing a token that was never the problem. Hence the explicit message here.
  log "checking GitHub credentials"
  GH_USER="$(gh api user --jq .login 2>/dev/null)" \
    || die "not authenticated with gh.
    Authenticate with:
        gh auth login"
  # owner/repo out of the remote URL, whatever its shape: https://github.com/owner/repo.git,
  # git@github.com:owner/repo.git, or an SSH host alias (git@github-work:owner/repo).
  REMOTE_URL="$(git -C "$ROOT" remote get-url origin)"
  REPO="$(printf '%s\n' "${REMOTE_URL%.git}" | awk -F'[:/]' '{print $(NF-1)"/"$NF}')"
  gh api "repos/${REPO}" --jq '.permissions.push' 2>/dev/null | grep -q true \
    || die "the account '${GH_USER}' has no write access to ${REPO}.
    You are probably authenticated as the wrong account. Check with:
        gh auth status
    and switch with:
        gh auth switch --user <account>"
  ok "credentials ok (${GH_USER} can write to ${REPO})"
fi

# --- 1. the binaries, only with --full --------------------------------------
if [[ $FULL -eq 1 ]]; then
  BUILD_DIR="$(mktemp -d)"
  # The binaries are disposable: they are the Release's artifact, not the repository's. They go
  # even if publishing fails, so they never become stray files the next `git status` reports.
  trap 'rm -rf "$BUILD_DIR"' EXIT

  log "building binaries for ${TAG}"
  for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    os="${target%/*}"; arch="${target#*/}"
    out="${BUILD_DIR}/api-mcp_${TAG}_${os}_${arch}"
    [[ "$os" == "windows" ]] && out="${out}.exe"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
      -ldflags "-s -w -X main.version=${TAG_VERSION}" -o "$out" "$ROOT" \
      || die "build failed for ${target} — nothing was pushed"
    ok "$(basename "$out") ($(du -h "$out" | cut -f1))"
  done

  ( cd "$BUILD_DIR" && sha256sum api-mcp_* > SHA256SUMS )
  ok "SHA256SUMS"
fi

# --- 2. git -----------------------------------------------------------------
log "git push"
git -C "$ROOT" push

# ONLY this version's tag, never `--tags`.
#
# `--tags` pushes every pending tag at once, and each one starts a workflow run. With two going
# up together the runs happen in parallel and the "Latest" label lands on whichever finishes
# last — which may well be the LOWER version. One tag per release, in order.
log "git push of tag ${TAG}"
git -C "$ROOT" push origin "$TAG"

# --- 3. the Release ---------------------------------------------------------
if [[ $FULL -eq 0 ]]; then
  log "done"
  ok "$(git -C "$ROOT" rev-parse --short HEAD) · ${TAG}"
  ok "CI is building and publishing the Release — watch it with: gh run watch"
  exit 0
fi

# Idempotent: it exists → update the notes; it does not → create it.
#
# `--latest` is EXPLICIT on both paths. Without it GitHub decides by DATE, and that decision is
# wrong whenever two releases go out together — the same accident described above.
NOTES="$(git -C "$ROOT" tag -l "$TAG" --format='%(contents)')"
log "publishing Release ${TAG}"
if gh release view "$TAG" >/dev/null 2>&1; then
  # `--clobber`: without it the Release would keep the previous run's binaries, with no error.
  gh release upload "$TAG" "$BUILD_DIR"/* --clobber || die "could not upload the binaries to ${TAG}"
  gh release edit "$TAG" --notes "$NOTES" --latest >/dev/null \
    || die "could not update Release ${TAG}"
  ok "Release ${TAG} updated and marked Latest"
else
  gh release create "$TAG" "$BUILD_DIR"/* --title "$TAG" --notes "$NOTES" --latest >/dev/null \
    || die "could not create Release ${TAG}"
  ok "Release ${TAG} published and marked Latest"
fi

log "done"
ok "$(git -C "$ROOT" rev-parse --short HEAD) · ${TAG}"
