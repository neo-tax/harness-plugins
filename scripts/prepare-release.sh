#!/bin/sh
# Prepares a release: on a release branch off main's tip, commits one bump of
# the plugin manifests to the next version, the stable marketplace's pin
# included, tags that commit with an annotated tag carrying its message,
# pushes both, and follows the release workflow the tag starts, which opens
# the release pull request. scripts/finish-release.sh then fast-forwards main
# to the commit, which is what rolls the release out.
#
#   scripts/prepare-release.sh [--dry-run] [major|minor|patch]
#
# Asks for the release type when it is not given. Only an organization admin
# can run it to the end: the v* tag is guarded by the release-tags ruleset,
# which admins bypass. --dry-run runs every check and shows the bump without
# committing, tagging, or pushing.
#
# Needs git, go, and gh.
#
# @spec: test-and-release#release-bumps-version
set -eu

die() { echo "prepare-release: $*" >&2; exit 1; }
usage() { echo "usage: $0 [--dry-run] [major|minor|patch]" >&2; exit 2; }

dry_run=""
if [ "${1:-}" = "--dry-run" ]; then
  dry_run=1
  shift
fi
[ $# -le 1 ] || usage
kind=${1:-}

cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

for cmd in git go gh; do
  command -v "$cmd" >/dev/null 2>&1 || die "$cmd is not installed"
done

# The release workflow refuses a tag whose commit is not a version bump on
# top of main, and finish-release.sh can only fast-forward main to it while
# main has not moved, so the release starts from a clean checkout of main's
# tip.
git fetch -q --tags origin main
sha=$(git rev-parse origin/main)
short=$(git rev-parse --short "$sha")
[ "$(git rev-parse HEAD)" = "$sha" ] ||
  die "HEAD is not origin/main ($short); run: git switch main && git pull --ff-only"
[ -z "$(git status --porcelain)" ] || die "the working tree has uncommitted changes"

# The bump commit changes only versions, so CI on the commit it sits on
# stands in for CI on the release; the release workflow runs the tests again,
# and pushing the release branch runs CI on the release commit.
ci=$(gh run list --workflow ci.yml --commit "$sha" --limit 1 --json status,conclusion \
  --jq '.[0] | "\(.status)/\(.conclusion)"')
[ "$ci" = "completed/success" ] || die "CI on $short is not green (${ci:-no run found})"

if [ -z "$kind" ]; then
  echo "The previous release was $(git tag -l 'v*' --sort=-v:refname | head -n 1)."
  printf 'What type of release is this: major, minor, or patch? '
  read -r kind </dev/tty
fi
case $kind in major | minor | patch) ;; *) usage ;; esac

if ! tag=$(go run ./cmd/release-check -bump "$kind"); then
  git checkout -q -- .
  die "could not bump the manifests"
fi
version=${tag#v}
branch="release/$tag"

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null ||
  git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  git checkout -q -- .
  die "$tag already exists"
fi
if git ls-remote --exit-code --heads origin "refs/heads/$branch" >/dev/null 2>&1; then
  git checkout -q -- .
  die "$branch already exists on origin"
fi

message="Version bump to $version"
echo "Bump $short to $tag on $branch and tag it \"$message\":"
git --no-pager diff --stat
echo

if [ -n "$dry_run" ]; then
  git checkout -q -- .
  echo "Dry run: reverted the bump; not committing, tagging, or pushing."
  exit 0
fi

printf 'Push %s and %s and start the release? [y/N] ' "$branch" "$tag"
read -r answer </dev/tty
case $answer in
  y | Y | yes) ;;
  *)
    git checkout -q -- .
    die "aborted; reverted the bump"
    ;;
esac

# Undoes everything after the bump, so a failed release leaves the checkout
# on main where it started and a rerun passes the clean-checkout checks.
undo() {
  git tag -d "$tag" >/dev/null 2>&1 || true
  git checkout -q -f main
  git reset -q --hard "$sha"
  git branch -q -D "$branch" >/dev/null 2>&1 || true
  die "$1; reverted the bump"
}

# A hook or a signing failure can stop the commit or the tag.
git switch -q -c "$branch" || undo "could not create $branch"
git commit -q -am "$message" || undo "could not commit the bump"
git tag -a "$tag" -m "$message" || undo "could not create $tag"
# Atomic, so the tag never exists without its branch or the reverse.
git push --atomic origin "refs/heads/$branch" "refs/tags/$tag" || undo "push failed"

# The branch and the tag are published and the release workflow has started,
# so nothing below undoes them.
git switch -q main
git branch -q -D "$branch"

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

pr=$(gh pr list --head "$branch" --base main --state open --json url --jq '.[0].url // empty')
echo "Released $tag${pr:+ and opened $pr}. When CI on it passes, run: scripts/finish-release.sh $tag"
