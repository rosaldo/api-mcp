#!/usr/bin/env bash
# push.sh — ships the work: commits, this version's tag and the GitHub Release.
#
# Usage:  ./push.sh          (root shortcut → this file, same as ./commit.sh)
#
# What CLOSES a version is ./commit.sh (bump, changelog, tag). This script only publishes it.
# The expensive checks run BEFORE anything is pushed, so the script fails without leaving half
# the work out there.
#
# Order: build the binaries → git push → git push of this version's tag → publish/update the
# Release with the binaries attached. Building comes first on purpose: a compile error must not
# be discovered after the commits are already public.
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
command -v go >/dev/null 2>&1 || die "go is not installed — the binaries are built here"

# A REAL credential check: it asks for WRITE permission, not just whether the repo can be read.
#
# Reading is not the question — the repository is public, so any account can do it, including
# one that cannot publish a thing. Checking only for read is how the commits and the tag went
# up and the Release did not: `gh` was authenticated as another account, and the failure only
# surfaced at the last step, with everything already public.
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

# --- 1. the binaries --------------------------------------------------------
# Cross-compiled here, not by CI: this repository has no workflow, and a release whose binaries
# depend on a machine nobody controls is a release that stops working without warning.
#
# CGO_ENABLED=0 gives a static binary that runs on any distribution — no glibc version to match.
# `-trimpath` keeps local paths out of the binary, and `-s -w` drops the debug tables (~30%).
# The version is embedded so `api-mcp` in the wild can say which build it is.
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

# Checksums travel with the binaries so anyone can verify what they downloaded is what was
# published. One file, the format `sha256sum -c` reads directly.
( cd "$BUILD_DIR" && sha256sum api-mcp_* > SHA256SUMS )
ok "SHA256SUMS"

# --- 2. git -----------------------------------------------------------------
log "git push"
git -C "$ROOT" push

# ONLY this version's tag, never `--tags`.
#
# `--tags` pushes every pending tag at once, and each one triggers a CI job. With two going up
# together the jobs run in parallel and the "Latest" label lands on whichever finishes last —
# which may well be the LOWER version. One tag per release, in order.
log "git push of tag ${TAG}"
git -C "$ROOT" push origin "$TAG"

# --- 3. the Release ---------------------------------------------------------
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
