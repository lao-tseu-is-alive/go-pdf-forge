# go-pdf-forge Architecture

## Purpose

`go-pdf-forge` is a temporary, asynchronous PDF optimization service. It accepts large browser uploads without buffering the full document in Go memory, processes untrusted PDFs with external tools, exposes durable job progress, and deletes input and output objects after a configurable retention period.

The repository contains two application processes, not independent business microservices:

- `pdf-api`: authentication or anonymous-session validation, chunked upload, job API, SSE and download.
- `pdf-worker`: durable job claim, PDF analysis/optimization/validation, purge and outbox delivery.

`mail-poc` is an isolated diagnostic binary used to prove SMTP connectivity before notifications are enabled.

## Runtime topology

```text
Browser
  | Connect unary chunks + Bearer JWT or anonymous capability
  | HTTP fetch-SSE and streamed download
  v
pdf-api (replicable) ------ PostgreSQL (jobs, leases, quotas, outbox)
  |                              ^
  | S3 multipart/objects         | SKIP LOCKED claim
  v                              |
Garage/S3 <----------------- pdf-worker (bounded concurrency)
                                  | pdfinfo, pdfimages, Ghostscript
                                  + SMTP outbox dispatcher
```

PostgreSQL and object storage are the only cross-process coordination mechanisms. No correctness property relies on an in-memory event hub or a pod-local filesystem.

PostgreSQL integration tests run against a random schema per test. Each isolated
pool has that schema as its sole search path, applies the embedded migrations,
and drops the schema with `CASCADE` during cleanup. The migration suite runs two
concurrent upgrade attempts under the advisory lock, then verifies a complete
down/up round trip. Queue tests start more concurrent workers than available
jobs and prove that `SKIP LOCKED` never attributes a job twice. CI and release
publication provide a dedicated PostgreSQL service for this opt-in suite; normal
unit tests and `make check` remain database-independent.

## Identity and ownership

The runtime supports three configured modes:

- `required`: a valid internal JWT is mandatory.
- `optional`: a valid JWT identifies an employee; otherwise an anonymous session is required.
- `anonymous`: login is not exposed and all users receive anonymous sessions.

An authenticated principal snapshots numeric `user_id`, numeric `external_id`, login, name and email on job creation. The JWT is never persisted. A local verifier compatible with the existing HS512 token contract validates the signature, temporal claims and configured issuer without logging tokens or raw claims.

An anonymous session is a 256-bit random capability. `CreateAnonymousSession` returns an opaque token once; the database stores only its HMAC-SHA-256 digest using a deployment secret. Anonymous jobs belong to the session, not merely to a guessable job UUID. If an Authorization header is present but invalid, the request fails instead of falling back to anonymous access.

Session creation and successful authentication also retain only domain-separated
HMAC digests of canonical client IP addresses. Capability comparison is constant
time in Go. Expiry is an exclusive timestamp, revocation preserves its first
timestamp, and the conditional last-seen update prevents a concurrent expiry or
revocation from authenticating successfully.

Authorization is checked in storage queries as well as service handlers. Authenticated administrators may inspect all jobs; regular employees and anonymous sessions can access only their own jobs.

## Upload and object storage

The default browser chunk is `8MiB`, the default upload limit is `256MiB`, and
an incomplete upload expires after 24 hours by default. `StartUpload` validates
declared size and metadata and opens an upload session. `UploadChunk` records
the index, byte length, SHA-256 and backend part identifier. New parts must be
strictly contiguous from index zero and have the exact expected size. Repeating
the same index, size and hash succeeds idempotently; changing an accepted index
fails.

Garage is the first production-shaped `BlobStore` backend. Its multipart implementation prevents the API from buffering the whole input. At commit, the service finalizes the object and verifies the full SHA-256 by streaming it back through a hasher. A job becomes `queued` only after size, chunk continuity and digest checks pass.

Filesystem storage implements the same domain contract for focused tests and a zero-infrastructure fallback. It is not used to coordinate multiple API or worker processes.

## Job queue and lifecycle

PostgreSQL owns the state machine:

```text
uploading -> queued -> analyzing -> optimizing -> validating -> completed
     |          |          |             |             |
     +----------+----------+-------------+-------------+-> failed/cancelled
completed/failed/cancelled -> expired
```

Workers claim queued or retryable jobs in short transactions using `FOR UPDATE SKIP LOCKED`. A claim records a worker ID, lease deadline, attempt count and heartbeat. External processing happens after the claim transaction commits. Expired leases are recoverable. State transitions use expected-state predicates to prevent stale workers from overwriting newer decisions.

The claim orders eligible jobs by availability and creation time, changes the
winner from `queued` to `analyzing`, and records its first start time in the
same statement as the row lock. A lease deadline is exclusive. Heartbeats and
phase transitions require the current worker ID, an active status and an
unexpired deadline; terminal transitions clear the lease. Progress cannot move
backwards within one attempt.

Recovery locks a bounded expired-lease batch with `SKIP LOCKED`. Jobs with a
cancellation or deletion request become `cancelled`, jobs at `max_attempts`
become `failed`, and the remainder return to `queued` after their configured
retry delay. Requeue resets indicative progress but preserves attempt count and
the first start time. This makes process death recoverable without an in-memory
coordinator and prevents a stale worker from completing work after lease loss.

Running cancellation is cooperative: the API records `cancel_requested_at`; the owning worker observes it and cancels the command context. Closing a browser or SSE connection never requests cancellation.

Job creation is an atomic `INSERT ... SELECT` from an owner-matching committed
upload. The repository copies the verified object key, complete SHA-256, byte
size and sanitized filename rather than trusting caller-supplied duplicates.
Every user-facing lookup or mutation includes all owner columns in SQL; a
missing job and a job owned by someone else are intentionally indistinguishable.

User deletion creates a durable `deletion_requested_at` tombstone, shortens the
retention deadline and requests cancellation of active work. Tombstoned jobs
immediately disappear from owner-facing reads, but their object keys remain
available to the future purge until blob deletion succeeds. Repeating deletion,
including for an absent or differently owned job, is safe and reveals no
ownership information.

## PDF strategy

Input plausibility checks are followed by `pdfinfo`; encrypted or unreadable input fails with a stable error code. `pdfimages -list` is diagnostic only. External commands receive explicit arguments, deadlines and bounded output capture.

If configured, an input already below the target is returned unchanged. Otherwise the worker tries Ghostscript `/ebook` first and stops when its valid output meets the indicative target. It tries `/screen` only when necessary. Every candidate must be readable by `pdfinfo`, preserve the page count and have non-zero size. The smallest valid result is preserved when no candidate meets the target; `target_met=false` is reported rather than failing an otherwise successful job.

## Events and notifications

The SSE endpoint emits `snapshot`, `progress`, `completed`, `failed` and `heartbeat` events. On connection and reconnection it reads the authoritative current job from PostgreSQL. Initial delivery may use bounded database polling so events work across replicas without another messaging system.

Completion and failure write outbox events in the same database transaction as the terminal job state. The worker dispatches notifications with retry and backoff. Anonymous sessions cannot request email. Deployments select one SMTP configuration (`plain`, `starttls`, or implicit `tls`); providers do not fail over automatically.

## Public anonymous protections

Anonymous traffic is constrained by configurable PostgreSQL-backed fixed-window
limits for session creation, upload starts, job creation and committed bytes.
The job repository and API will additionally bound active jobs per IP and
session plus global anonymous queue depth. Raw forwarding headers are ignored
unless the direct peer belongs to `TRUSTED_PROXY_CIDRS`. HTTP body, header and
command timeouts protect against slow clients.

The initial fixed window is 24 hours. Starting defaults are 20 session
creations per IP, 10 uploads and jobs per session, 50 uploads and jobs per IP,
1 GiB committed per session, and 5 GiB committed per IP. These values are
deployment configuration, not database constants, and must be tuned from
observed traffic. Session and IP counters are updated in one PostgreSQL
transaction; exceeding either scope rolls back the complete consumption.

## Deployment

Local native development uses PostgreSQL and Garage on the host. Rancher Desktop validates k3s manifests; pods require host-reachable endpoints rather than `127.0.0.1`. Internal production uses external PostgreSQL, Garage/S3, Kustomize overlays, Sealed Secrets and the existing LoadBalancer/F5 routing model. Worker scratch data uses bounded `emptyDir`, never shared PVC storage.

The VPS deployment provides separate hardened systemd units for API and worker. TLS and coarse connection limiting terminate at a reverse proxy, while application quotas remain authoritative across replicas.

The location and topology of the production Garage service remain an operational decision. The application contract does not depend on it.
