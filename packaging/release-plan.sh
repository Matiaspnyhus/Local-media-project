#!/usr/bin/env bash
# Works out, for one run of the GitHub checks, which commit is tested, which
# version the programs are stamped with, and whether a version is published.
# It prints three lines (sha=, version=, release=) for the checks to read.
# The rules are explained above the "plan" job in .github/workflows/checks.yml.
#
# It reads GITHUB_SHA, GITHUB_REF, GITHUB_REF_NAME and GITHUB_EVENT_NAME as
# GitHub sets them, and MESSAGE: the message of the newest commit pushed.
set -euo pipefail

sha="$GITHUB_SHA"
version="$GITHUB_REF_NAME"
release=""

if [[ "$GITHUB_REF" =~ ^refs/tags/(v[0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
  release="${BASH_REMATCH[1]}"
elif [[ "$GITHUB_EVENT_NAME" == push && "$GITHUB_REF" == refs/heads/* &&
        "${MESSAGE:-}" =~ \[release\ (v[0-9]+\.[0-9]+\.[0-9]+)\ at\ ([0-9a-f]{40})\] ]]; then
  release="${BASH_REMATCH[1]}"
  sha="${BASH_REMATCH[2]}"
  if ! git cat-file -e "$sha^{commit}" 2>/dev/null; then
    echo "The commit named for $release ($sha) does not exist here." >&2
    exit 1
  fi
  if ! git merge-base --is-ancestor "$sha" "$GITHUB_SHA"; then
    echo "The commit named for $release ($sha) is not part of this branch." >&2
    exit 1
  fi
  if git rev-parse -q --verify "refs/tags/$release" >/dev/null ||
     [ -n "$(git ls-remote --tags origin "refs/tags/$release" 2>/dev/null)" ]; then
    echo "$release exists already. A version is published once; choose the next number." >&2
    exit 1
  fi
fi

if [ -n "$release" ]; then
  version="$release"
  if ! git cat-file -e "$sha:docs/release-notes/$release.md" 2>/dev/null; then
    echo "docs/release-notes/$release.md is missing in that commit: write the release notes first." >&2
    exit 1
  fi
  if git cat-file -e "$sha:packaging/NOT-FOR-RELEASE.txt" 2>/dev/null; then
    git show "$sha:packaging/NOT-FOR-RELEASE.txt" >&2
    exit 1
  fi
fi

echo "sha=$sha"
echo "version=$version"
echo "release=$release"
