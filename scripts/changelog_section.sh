#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <version-without-v>" >&2
  exit 2
fi

version="$1"
awk -v heading="## [${version}]" '
  $0 == heading || index($0, heading " - ") == 1 { found=1; next }
  found && /^## \[/ { exit }
  found && /^\[/ { exit }
  found { print }
  END { if (!found) exit 1 }
' CHANGELOG.md
