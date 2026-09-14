#!/usr/bin/env bash
# Merge official james-6-23/codex2api into the current branch.
# Usage:
#   .github/scripts/sync-upstream.sh           # merge upstream/main
#   .github/scripts/sync-upstream.sh v2.9.8    # merge a release tag
set -euo pipefail

if ! git remote get-url upstream >/dev/null 2>&1; then
  echo "missing remote: upstream" >&2
  exit 1
fi

ref="${1:-main}"
git fetch upstream --tags --force
if git rev-parse "upstream/${ref}" >/dev/null 2>&1; then
  target="upstream/${ref}"
elif git rev-parse "${ref}" >/dev/null 2>&1; then
  target="${ref}"
else
  echo "unknown ref: ${ref}" >&2
  exit 1
fi

echo "merging ${target} into $(git branch --show-current)"
git merge --no-edit "${target}"
echo "done. review, then: git push origin HEAD"
