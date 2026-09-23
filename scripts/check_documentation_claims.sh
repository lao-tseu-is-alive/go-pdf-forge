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

require_literal internal/config/config.go 'durationValue(lookup, "UPLOAD_SESSION_TTL", 24*time.Hour)' 'upload session TTL source'
require_literal .env.example 'UPLOAD_SESSION_TTL=24h' 'upload session TTL environment example'
require_literal ARCHITECTURE.md 'an incomplete upload expires after 24 hours by default.' 'upload session TTL architecture'

require_literal internal/config/config.go 'defaultTargetBytes    int64 = 75 * 1024 * 1024' 'PDF target source'
require_literal .env.example 'PDF_TARGET_BYTES=78643200' 'PDF target environment example'
require_literal AGENTS.md 'The target size defaults to `75MiB` and is indicative.' 'PDF target agent contract'
require_literal ARCHITECTURE.md 'indicative target' 'best-effort target architecture'

require_literal internal/config/config.go 'durationValue(lookup, "JOB_RETENTION", 48*time.Hour)' 'retention source'
require_literal .env.example 'JOB_RETENTION=48h' 'retention environment example'
require_literal AGENTS.md 'Default retention is 48 hours.' 'retention agent contract'

# Anonymous fixed-window defaults are an operator-facing contract and must not
# drift independently between runtime configuration and deployment examples.
require_literal internal/config/config.go 'defaultAnonymousQuotaWindow                  = 24 * time.Hour' 'anonymous quota window source'
require_literal internal/config/config.go 'defaultAnonymousQuotaSessionsPerIP     int64 = 20' 'anonymous session quota source'
require_literal internal/config/config.go 'defaultAnonymousQuotaUploadsPerSession int64 = 10' 'anonymous upload session quota source'
require_literal internal/config/config.go 'defaultAnonymousQuotaUploadsPerIP      int64 = 50' 'anonymous upload IP quota source'
require_literal internal/config/config.go 'defaultAnonymousQuotaJobsPerSession    int64 = 10' 'anonymous job session quota source'
require_literal internal/config/config.go 'defaultAnonymousQuotaJobsPerIP         int64 = 50' 'anonymous job IP quota source'
require_literal internal/config/config.go 'defaultAnonymousQuotaBytesPerSession   int64 = 1024 * 1024 * 1024' 'anonymous byte session quota source'
require_literal internal/config/config.go 'defaultAnonymousQuotaBytesPerIP        int64 = 5 * 1024 * 1024 * 1024' 'anonymous byte IP quota source'
require_literal .env.example 'ANONYMOUS_QUOTA_WINDOW=24h' 'anonymous quota window environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_SESSIONS_PER_IP=20' 'anonymous session quota environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_UPLOADS_PER_SESSION=10' 'anonymous upload session quota environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_UPLOADS_PER_IP=50' 'anonymous upload IP quota environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_JOBS_PER_SESSION=10' 'anonymous job session quota environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_JOBS_PER_IP=50' 'anonymous job IP quota environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_BYTES_PER_SESSION=1073741824' 'anonymous byte session quota environment example'
require_literal .env.example 'ANONYMOUS_QUOTA_BYTES_PER_IP=5368709120' 'anonymous byte IP quota environment example'
require_literal ARCHITECTURE.md 'The initial fixed window is 24 hours.' 'anonymous quota architecture window'
require_literal ARCHITECTURE.md 'transaction; exceeding either scope rolls back the complete consumption.' 'anonymous quota atomicity architecture'

# Identity and storage boundaries are security contracts, not optional prose.
for mode in required optional anonymous; do
    require_literal ARCHITECTURE.md "\`${mode}\`" "authentication mode ${mode}"
done
require_literal AGENTS.md 'Anonymous email notifications are disabled.' 'anonymous email prohibition'
require_literal ARCHITECTURE.md 'Anonymous sessions cannot request email.' 'anonymous email architecture'
require_literal ARCHITECTURE.md 'No correctness property relies on an in-memory event hub or a pod-local filesystem.' 'cross-process coordination boundary'

echo "docs-assert: OK"
