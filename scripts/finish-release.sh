#!/bin/sh
# Finishes a release that scripts/prepare-release.sh started: once the
# release workflow has published the tag and installed it the way a fleet
# does, and CI on the release commit is green, fast-forwards main to the
# tagged commit. Engineers read the marketplace from main, so this push is
# what points every install and auto-update at the release. GitHub marks the
# release pull request merged when its head lands on main.
#
#   scripts/finish-release.sh [vX.Y.Z]
#
# Finishes the newest v* tag when none is given. Only an organization admin
# can run it to the end: main's ruleset takes changes only through a
# squash-merged pull request, which would leave the tag off main, and admins
# bypass it.
#
# Needs git and gh.
#
# @spec: test-and-release#rollout-by-fast-forward
set -eu

die() { echo "finish-release: $*" >&2; exit 1; }
[ $# -le 1 ] || { echo "usage: $0 [vX.Y.Z]" >&2; exit 2; }

cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

for cmd in git gh; do
  command -v "$cmd" >/dev/null 2>&1 || die "$cmd is not installed"
done

git fetch -q --tags origin main
tag=${1:-$(git tag -l 'v*' --sort=-v:refname | head -n 1)}
[ -n "$tag" ] || die "no v* tag found"
sha=$(git rev-parse -q --verify "refs/tags/$tag^{commit}") || die "$tag does not exist"
short=$(git rev-parse --short "$sha")
main=$(git rev-parse origin/main)

if git merge-base --is-ancestor "$sha" "$main"; then
  echo "main already contains $tag ($short); nothing to do."
  exit 0
fi

# A fast-forward is the only way the tag ends up on main. If main has moved
# since the release was prepared, this release cannot be rolled out: it stays
# published but unpinned, and the next release, which starts from this tag's
# version, supersedes it and closes its pull request.
[ "$(git rev-parse "$sha^")" = "$main" ] ||
  die "main has moved since $tag was prepared, so it can no longer be fast-forwarded to $short; cut the next release with scripts/prepare-release.sh, which supersedes $tag"

run=$(gh run list --workflow release.yml --branch "$tag" --limit 1 --json status,conclusion,url \
  --jq '.[0] | "\(.status)/\(.conclusion) \(.url)"')
[ "${run%% *}" = "completed/success" ] ||
  die "the release workflow for $tag has not succeeded (${run:-no run found})"

ci=$(gh run list --workflow ci.yml --commit "$sha" --limit 1 --json status,conclusion \
  --jq '.[0] | "\(.status)/\(.conclusion)"')
[ "$ci" = "completed/success" ] || die "CI on $short is not green (${ci:-no run found})"

pr=$(gh pr list --head "release/$tag" --base main --state open --json url --jq '.[0].url // empty')
echo "Fast-forward main from $(git rev-parse --short "$main") to $short ($tag)${pr:+, merging $pr}."
printf 'Roll %s out to every engineer? [y/N] ' "$tag"
read -r answer </dev/tty
case $answer in
  y | Y | yes) ;;
  *) die "aborted" ;;
esac

# Not --force: the push is refused unless it fast-forwards.
git push origin "$sha:refs/heads/main" || die "push failed"
git push -q origin --delete "release/$tag" 2>/dev/null || true

echo "Rolled out $tag. Engineers pick it up on their next marketplace refresh."
