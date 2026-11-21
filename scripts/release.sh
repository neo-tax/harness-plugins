#!/bin/sh
# Cuts a release: bumps the plugin manifests on main to the next version,
# commits the bump, tags it with an annotated tag carrying that commit's
# message, pushes both, and follows the release workflow the tag starts.
#
#   scripts/release.sh [--dry-run] [major|minor|patch]
#
# Asks for the release type when it is not given. Only an organization admin
# can run it to the end: the bump commit goes straight to main and the v* tag
# is guarded by the release-tags ruleset, and admins bypass both. --dry-run
# runs every check and shows the bump without committing, tagging, or pushing.
#
# Needs git, go, and gh.
set -eu

die() { echo "release: $*" >&2; exit 1; }
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

# The release workflow refuses a tag that is not on main, and the bump commit
# must fast-forward main, so the release starts from a clean checkout of
# main's tip.
git fetch -q --tags origin main
sha=$(git rev-parse origin/main)
short=$(git rev-parse --short "$sha")
[ "$(git rev-parse HEAD)" = "$sha" ] ||
  die "HEAD is not origin/main ($short); run: git switch main && git pull --ff-only"
[ -z "$(git status --porcelain)" ] || die "the working tree has uncommitted changes"

# The bump commit changes only versions, so CI on the commit it sits on
# stands in for CI on the release; the release workflow runs the tests again.
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

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null ||
  git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  git checkout -q -- .
  die "$tag already exists"
fi

message="Version bump to $version"
echo "Bump $short to $tag and tag it \"$message\":"
git --no-pager diff --stat
echo

if [ -n "$dry_run" ]; then
  git checkout -q -- .
  echo "Dry run: reverted the bump; not committing, tagging, or pushing."
  exit 0
fi

printf 'Push the bump and %s to main and start the release? [y/N] ' "$tag"
read -r answer </dev/tty
case $answer in
  y | Y | yes) ;;
  *)
    git checkout -q -- .
    die "aborted; reverted the bump"
    ;;
esac

git commit -q -am "$message"
git tag -a "$tag" -m "$message"
# Atomic, so main never carries a bump without its tag or the reverse.
if ! git push --atomic origin HEAD:refs/heads/main "refs/tags/$tag"; then
  git tag -d "$tag" >/dev/null
  git reset -q --hard "$sha"
  die "push failed; deleted the local tag and bump commit"
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

echo "Released $tag. Approve and merge the \"Pin the marketplace to $tag\" pull request."
