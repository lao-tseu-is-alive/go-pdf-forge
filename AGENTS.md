# go-pdf-forge Agent Instructions

## Mission

Build a Go cloud-native PDF self-service application with a Vue/TypeScript frontend. Users upload PDFs in chunks, processing continues asynchronously with Ghostscript, progress is exposed over authenticated fetch-SSE, and temporary results expire automatically.

Read `docs/brief_go_pdf_self_service_agent.md`, `ARCHITECTURE.md`, `docs/ROADMAP.md`, and `docs/DOCUMENTATION.md` before making architectural changes. `docs/ROADMAP.md` is the source of truth for implementation order and task status.

## Repository identity

- Repository and application name: `go-pdf-forge`.
- Go module: `github.com/lao-tseu-is-alive/go-pdf-forge`.
- Backend toolchain: Go 1.27.1, Protobuf, Buf, ConnectRPC, PostgreSQL/pgx.
- Frontend: Vue 3 and TypeScript.
- Main binaries: `cmd/pdf-api`, `cmd/pdf-worker`, diagnostic `cmd/mail-poc`, and explicit schema tool `cmd/pdf-migrate`.

## Reference repositories

The following sibling repositories are read-only references. Never modify them unless the user explicitly asks:

- `../go-grpc-file-upload`: Buf, Protobuf, ConnectRPC, upload commit/hash patterns.
- `../go-cloud-k8s-employe-jwt`: F5-to-JWT identity provider.
- `../go-geo-tree-table`: current JWT consumer, Vue/sessionStorage, PostgreSQL, logging and Kubernetes patterns.

Reuse ideas and contracts deliberately; do not copy obsolete or unsafe implementation details.

## Stable architecture decisions

- PostgreSQL is the source of truth and job queue. Claim jobs with `FOR UPDATE SKIP LOCKED` plus leases and recovery; do not add Redis, NATS, Kafka or Temporal for the MVP.
- Development uses the local PostgreSQL database `go_pdf_forge`. Production uses a PostgreSQL server external to k3s.
- Never read, print, commit or log `.env`, passwords, JWTs, S3 keys, SMTP credentials or DSNs.
- Use an S3-compatible `BlobStore`; Garage is the development backend. Filesystem storage is for tests/local fallback only.
- API and workers must not depend on a shared local filesystem. Worker scratch space is disposable.
- Browser uploads use `StartUpload`, idempotent `UploadChunk`, `CommitUpload`, and `AbortUpload`. Default maximum upload is `256MiB`; default chunk size is `8MiB`. Never load a complete PDF into API or worker Go memory.
- Verify a per-chunk SHA-256 and the complete object SHA-256 before queueing the job.
- Ghostscript remains the PDF engine and is invoked with `exec.CommandContext` and separate arguments, never through a shell.
- PDF processing is best effort: try `/ebook` (approximately 150 DPI) first, then `/screen` (approximately 72 DPI) only if needed. The target size defaults to `75MiB` and is indicative. Preserve the best valid result even when the target is not met.
- Default retention is 48 hours. Purging blobs and metadata must be idempotent.
- SSE uses a normal HTTP endpoint consumed with `fetch()` so headers can be sent. It must survive reconnects and must not control job lifetime.
- Email is processed through a transactional outbox only after `cmd/mail-poc` has validated the actual SMTP path. SMTP failure never changes a completed PDF job to failed.
- On 2026-09-15 the local `mail-poc` reached the configured internal relay without authentication and one message was delivered to an internal `lausanne.ch` mailbox. The first relay hop was plain SMTP inside the trusted network; subsequent Exchange hops used TLS 1.2. The Go client talks SMTP directly, so no local `ssmtp` package is required.

## Identity and anonymous access

- Supported modes are `required`, `optional`, and `anonymous`.
- Authenticated requests accept the existing HS512 JWT contract but use a hardened local adapter that validates signature, time and issuer and never logs tokens.
- `external_id` is the numeric Goeland employee ID and is represented as `int64`.
- JWTs are stored in browser `sessionStorage` as required by the existing infrastructure.
- Anonymous use has no login but uses a random capability/session secret; store only its HMAC-SHA-256 digest server-side. The HMAC pepper is a deployment secret of at least 32 bytes. Invalid JWTs must never downgrade to anonymous access.
- Anonymous capability secrets may be stored in browser `localStorage` until job expiry so a user can close and reopen the browser.
- Public anonymous mode requires application-level rate limits and PostgreSQL-backed quotas per IP/session. Trust forwarded client IP headers only from configured proxy CIDRs.
- Anonymous email notifications are disabled.

## Deployment targets

- Local native development with local PostgreSQL, Garage, Ghostscript and Poppler tools.
- Rancher Desktop/k3s for local manifest tests.
- Internal k3s production using Kustomize, Sealed Secrets and the existing Service/LoadBalancer pattern.
- Public VPS using hardened systemd units and an external TLS reverse proxy.
- Kubernetes containers run non-root, drop all capabilities, disable service-account token mounting, use a read-only root filesystem where practical, and declare CPU, memory and ephemeral-storage requests/limits.

## Change discipline and verification

- Keep changes incremental and compiling; write tests with each behavior.
- `internal/version/version.go` is the release version source of truth. A release updates it, the README current-version banner and a versioned `CHANGELOG.md` section in the same commit.
- Update `docs/ROADMAP.md` whenever a task starts, completes, changes scope, or changes order. Task IDs are stable and must remain unique.
- Every completed roadmap task must appear by ID in a dated `CHANGELOG.md` section, and every released task ID must be marked complete in the roadmap; `make release-check` enforces both directions.
- Document every non-generated Go package and exported Go API according to `docs/DOCUMENTATION.md`; comments describe contracts and invariants rather than restating syntax.
- Keep `docs/atlas.md` synchronized exactly whenever a non-ignored repository file is added, removed or renamed.
- Keep Protobuf declarations documented under Buf's `COMMENTS` lint category and add executable documentation assertions for stable operational or security claims.
- Run `make docs-check` for documentation-sensitive changes. It is also part of `make check`, CI and the release gate.
- Before a release commit run `make release-prepare`; after committing, `CONFIRM_RELEASE=vX.Y.Z make release` requires a clean `main`, creates an annotated tag and atomically pushes `main` plus the tag. Never bypass `make release-check`.
- Treat `.proto` files as authoritative and never hand-edit generated files.
- Prefer `rg`, existing scripts, Make targets, and `uv` for any Python tooling.
- Do not run migrations against a real database, send email, create buckets/keys, deploy, or change external infrastructure without explicit user authorization.
- Before handoff, run the relevant subset of `gofmt`, `go test ./...`, `go vet ./...`, `buf lint`, frontend typecheck/tests/build, and container/manifests validation.
