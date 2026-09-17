#!/usr/bin/env bash
set -euo pipefail

require_literal() {
    local file="$1"
    local literal="$2"
    local claim="$3"

    if ! rg --fixed-strings --quiet -- "$literal" "$file"; then
        echo "docs-assert: ${claim}: ${file} does not contain expected text: ${literal}" >&2
        return 1
    fi
}

# Runtime limits must agree between source defaults, the environment example,
# and the operator-facing architecture.
require_literal internal/config/config.go 'defaultMaxUploadBytes int64 = 256 * 1024 * 1024' 'upload limit source'
require_literal .env.example 'MAX_UPLOAD_BYTES=268435456' 'upload limit environment example'
require_literal ARCHITECTURE.md 'default upload limit is `256MiB`' 'upload limit architecture'

require_literal internal/config/config.go 'defaultChunkBytes     int64 = 8 * 1024 * 1024' 'chunk size source'
require_literal .env.example 'UPLOAD_CHUNK_BYTES=8388608' 'chunk size environment example'
require_literal ARCHITECTURE.md 'default browser chunk is `8MiB`' 'chunk size architecture'

require_literal internal/config/config.go 'defaultTargetBytes    int64 = 75 * 1024 * 1024' 'PDF target source'
require_literal .env.example 'PDF_TARGET_BYTES=78643200' 'PDF target environment example'
require_literal AGENTS.md 'The target size defaults to `75MiB` and is indicative.' 'PDF target agent contract'
require_literal ARCHITECTURE.md 'indicative target' 'best-effort target architecture'

require_literal internal/config/config.go 'durationValue(lookup, "JOB_RETENTION", 48*time.Hour)' 'retention source'
require_literal .env.example 'JOB_RETENTION=48h' 'retention environment example'
require_literal AGENTS.md 'Default retention is 48 hours.' 'retention agent contract'

# Identity and storage boundaries are security contracts, not optional prose.
for mode in required optional anonymous; do
    require_literal ARCHITECTURE.md "\`${mode}\`" "authentication mode ${mode}"
done
require_literal AGENTS.md 'Anonymous email notifications are disabled.' 'anonymous email prohibition'
require_literal ARCHITECTURE.md 'Anonymous sessions cannot request email.' 'anonymous email architecture'
require_literal ARCHITECTURE.md 'No correctness property relies on an in-memory event hub or a pod-local filesystem.' 'cross-process coordination boundary'

echo "docs-assert: OK"
