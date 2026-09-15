#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

version="$(sed -n 's/^[[:space:]]*Version = "\([0-9][0-9.]*\)"/\1/p' internal/version/version.go)"
tag="v${version}"

if [[ -z "${version}" ]]; then
  echo "release: unable to read internal/version/version.go" >&2
  exit 1
fi
if [[ "${CONFIRM_RELEASE:-}" != "${tag}" ]]; then
  echo "release: set CONFIRM_RELEASE=${tag} to confirm this external operation" >&2
  exit 1
fi
if [[ "$(git branch --show-current)" != "main" ]]; then
  echo "release: releases must be made from main" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "release: working tree is dirty; commit the version and changelog first" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
  echo "release: local tag ${tag} already exists" >&2
  exit 1
fi
set +e
git ls-remote --exit-code --tags origin "refs/tags/${tag}" >/dev/null 2>&1
remote_tag_status=$?
set -e
case "${remote_tag_status}" in
  0)
    echo "release: remote tag ${tag} already exists" >&2
    exit 1
    ;;
  2)
    ;;
  *)
    echo "release: unable to verify remote tag ${tag}; refusing to publish" >&2
    exit 1
    ;;
esac

make release-check
git tag -a "${tag}" -m "${tag}"

echo "release: pushing main and ${tag} atomically to origin"
if ! git push --atomic origin main "refs/tags/${tag}"; then
  echo "release: push failed; local annotated tag ${tag} was kept for inspection" >&2
  exit 1
fi

echo "release: ${tag} pushed; GitHub Actions will publish the release"
