#!/usr/bin/env bash
set -euo pipefail

roadmap="${1:-docs/ROADMAP.md}"
changelog="${2:-CHANGELOG.md}"

if [[ ! -f "${roadmap}" ]]; then
    echo "release-traceability-check: roadmap not found: ${roadmap}" >&2
    exit 1
fi
if [[ ! -f "${changelog}" ]]; then
    echo "release-traceability-check: changelog not found: ${changelog}" >&2
    exit 1
fi

released_changelog="$(mktemp)"
trap 'rm -f -- "${released_changelog}"' EXIT

# Ignore Unreleased: a completed task is considered delivered only when its ID
# appears below a dated semantic-version heading.
awk '
    /^## \[[0-9]+\.[0-9]+\.[0-9]+\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$/ { released=1 }
    released { print }
' "${changelog}" >"${released_changelog}"

failed=0
while IFS= read -r task_id; do
    [[ -n "${task_id}" ]] || continue
    if ! grep -Fq -- "${task_id}" "${released_changelog}"; then
        echo "release-traceability-check: completed task ${task_id} is missing from a dated changelog section" >&2
        failed=1
    fi
done < <(sed -n 's/^- \[x\] \*\*\(GPF-[0-9][0-9][0-9]\).*/\1/p' "${roadmap}")

while IFS= read -r task_id; do
    [[ -n "${task_id}" ]] || continue
    if ! grep -Fq -- "- [x] **${task_id} " "${roadmap}"; then
        echo "release-traceability-check: released task ${task_id} is not marked complete in the roadmap" >&2
        failed=1
    fi
done < <(grep -oE 'GPF-[0-9]{3}' "${released_changelog}" | sort -u || true)

if [[ "${failed}" -ne 0 ]]; then
    exit 1
fi

echo "release-traceability-check: OK"
