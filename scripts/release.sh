#!/bin/sh
# Cuts a release: tags the tip of main with an annotated tag whose message is
# that commit's message, verbatim, pushes the tag, and follows the release
# workflow it starts.
#
#   scripts/release.sh [--dry-run] vX.Y.Z
#
# Bump `version` in plugins/*/.claude-plugin/plugin.json in a merged pull
# request first; release-check fails otherwise. Only an organization admin can
# push a v* tag (the release-tags ruleset). --dry-run runs every check and
# prints the tag message without tagging or pushing.
#
# Needs git, go, and gh.
set -eu

die() { echo "release: $*" >&2; exit 1; }
usage() { echo "usage: $0 [--dry-run] vX.Y.Z" >&2; exit 2; }

dry_run=""
if [ "${1:-}" = "--dry-run" ]; then
  dry_run=1
  shift
fi
[ $# -eq 1 ] || usage
tag=$1
echo "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || usage

cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

for cmd in git go gh; do
  command -v "$cmd" >/dev/null 2>&1 || die "$cmd is not installed"
done

# The release workflow refuses a tag that is not on main; tagging main's tip
# from a clean checkout of it also means release-check below sees exactly
# the tree being released.
git fetch -q origin main
sha=$(git rev-parse origin/main)
short=$(git rev-parse --short "$sha")
[ "$(git rev-parse HEAD)" = "$sha" ] ||
  die "HEAD is not origin/main ($short); run: git switch main && git pull --ff-only"
[ -z "$(git status --porcelain)" ] || die "the working tree has uncommitted changes"

git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "$tag already exists locally"
if git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  die "$tag already exists on origin"
fi

go run ./cmd/release-check "$tag" || die "the manifests do not match $tag; bump plugins/*/.claude-plugin/plugin.json first"

ci=$(gh run list --workflow ci.yml --commit "$sha" --limit 1 --json status,conclusion \
  --jq '.[0] | "\(.status)/\(.conclusion)"')
[ "$ci" = "completed/success" ] || die "CI on $short is not green (${ci:-no run found})"

message=$(git log -1 --format=%B "$sha")
echo "Tag $tag -> $short, with message:"
echo "----"
printf '%s\n' "$message"
echo "----"

if [ -n "$dry_run" ]; then
  echo "Dry run: not tagging or pushing."
  exit 0
fi

printf 'Push %s to origin and start the release? [y/N] ' "$tag"
read -r answer </dev/tty
case $answer in y | Y | yes) ;; *) die "aborted" ;; esac

printf '%s\n' "$message" | git tag -a "$tag" --cleanup=verbatim -F - "$sha"
if ! git push origin "refs/tags/$tag"; then
  git tag -d "$tag" >/dev/null
  die "push failed; deleted the local tag"
fi

# The run takes a few seconds to appear after the push.
run=""
i=0
while [ -z "$run" ] && [ $i -lt 20 ]; do
  sleep 3
  run=$(gh run list --workflow release.yml --branch "$tag" --limit 1 --json databaseId --jq '.[0].databaseId // empty')
  i=$((i + 1))
done
[ -n "$run" ] || die "pushed $tag, but no release run appeared; check the Actions tab"
gh run watch "$run" --exit-status || die "the release workflow failed: $(gh run view "$run" --json url --jq .url)"

echo "Released $tag. Unless this release's marketplace pin is a no-op, approve and merge the \"Pin the marketplace to $tag\" pull request."
