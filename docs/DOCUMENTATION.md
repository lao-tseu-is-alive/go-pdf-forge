# Documentation quality contract

This document is the normative documentation contract for `go-pdf-forge`. It
applies equally to human contributors and coding agents. Its purpose is to keep
the repository understandable without relying on conversational memory and to
turn detectable documentation drift into a build or release failure.

The words **MUST**, **MUST NOT**, **SHOULD** and **MAY** express requirement
levels. Automated checks prove structure, coverage and selected factual links;
review remains responsible for clarity and semantic accuracy.

## Outcomes

A compliant change leaves a future reader able to answer four questions:

1. What contract does each package and exported API provide?
2. Which file owns each behavior, decision, generated artifact or operation?
3. Which documented operational and security claims are enforced by code?
4. Which controls ran before the change could become a release?

Documentation is part of the implementation. It MUST be updated in the same
change as the behavior, file inventory or release state it describes.

## Sources of truth and responsibilities

| Component | Responsibility | Enforcement |
| --- | --- | --- |
| `docs/DOCUMENTATION.md` | Defines this normative contract and the adoption model for other repositories. | Durable references from `AGENTS.md` and `README.md`; executable assertions. |
| Go source | Documents package purpose and exported contracts beside the implementation. | `cmd/doccheck --scope go`, exposed as `make godoc-check`. |
| `api/**/*.proto` | Defines the authoritative network contracts and their semantics. | `buf lint`, including the `COMMENTS` category configured in `buf.yaml`. |
| `docs/atlas.md` | Gives every repository file one discoverable responsibility and authority note. | `cmd/doccheck --scope atlas`, exposed as `make atlas-check`. |
| `scripts/check_documentation_claims.sh` | Connects selected stable prose claims to source, examples and automation. | `make docs-assert`. |
| `docs/ROADMAP.md` | Owns implementation order, scope and task completion state. | Roadmap and release-traceability guards. |
| `CHANGELOG.md` | Records what a released version actually delivered. | Version, changelog and bidirectional task traceability guards. |
| `Makefile` | Composes local documentation, quality and release gates. | `make docs-check`, `make check`, `make release-check` and `make release`. |
| `.github/workflows/*.yml` | Runs the same repository-owned gates in CI and during publication. | GitHub branch/tag workflows; no separate reduced CI policy. |
| `AGENTS.md` | Makes the contract mandatory for any compatible coding agent entering the repository. | Repository instruction loading plus an executable reference assertion. |

No summary in another file supersedes this document. Short references SHOULD
point here instead of copying rules that could later diverge.

## Go source contract

### Package documentation

Every non-generated Go package MUST contain at least one package comment:

- a library package comment begins with `Package <package-name> `;
- a `main` package comment begins with `Command ` or `Package main `.

The comment explains the package boundary and responsibility, not its directory
name. A package does not need a separate `doc.go` when an existing source file
is the natural home for that comment.

```go
// Package upload manages persistent multipart upload metadata and enforces
// ownership, ordering and digest invariants without storing PDF bytes.
package upload
```

### Exported API documentation

Every exported type, function, constant and variable MUST have a GoDoc comment
whose first word is its exact identifier. Exported methods on exported receiver
types follow the same rule. Every named exported struct field MUST have a field
or trailing comment beginning with its exact field name.

```go
// Session represents server-side upload state; it never contains a raw
// anonymous capability.
type Session struct {
	// ExpiresAt is the exclusive UTC deadline after which writes are rejected.
	ExpiresAt time.Time
}

// Commit verifies the ordered part metadata and atomically makes an upload
// immutable. Repeating a successful commit is safe.
func (s *Store) Commit(ctx context.Context, id uuid.UUID) error
```

A useful contract comment states the facts callers need that the Go type system
cannot express. Depending on the API, that includes:

- units, bounds, default values and whether deadlines are inclusive;
- ownership, secret handling and authorization boundaries;
- transaction scope, idempotence and partial-failure behavior;
- concurrency safety, ordering, locking and retry semantics;
- resource lifecycle, cancellation and cleanup responsibilities;
- sentinel errors and meaningful failure conditions;
- whether data is streamed or may be retained in memory.

Comments MUST NOT merely restate the declaration. Private helpers SHOULD be
commented when they carry a non-obvious invariant, security decision or
algorithmic constraint. Otherwise, clear naming and small functions are
preferred.

Source comments are written in English to match Go identifiers, Go tooling and
the public API. Project and operator documents MAY use French.

### Mechanical scope and review scope

`cmd/doccheck` examines every non-ignored `.go` file known to Git, including
new untracked files. It mechanically excludes `_test.go` files and files that
Go identifies as generated. It also excludes exported-looking methods on
private receiver types because those methods are not a public package API.

These exclusions only remove a mechanical GoDoc requirement. Tests MUST remain
readable, generated files MUST identify their generator, and complex private
behavior still requires human review. Generated files MUST be changed through
their authoritative source and generator, never by hand.

## Protobuf contract

Files below `api/` are authoritative. Generated Go and TypeScript bindings are
derived artifacts and MUST NOT be edited manually.

`buf.yaml` enables `STANDARD` and `COMMENTS`. Services, RPCs, messages, enums,
enum values, oneofs and fields therefore require comments. Those comments MUST
describe relevant units, optionality, authorization, idempotence, ownership and
error semantics. Regenerate bindings with `make generate` after a contract
change and commit the source plus generated result together.

## Repository atlas contract

[`atlas.md`](atlas.md) is a controlled, file-by-file index. It MUST contain
exactly one entry for every tracked or new non-ignored repository file,
including documentation, migrations, generated bindings and automation.

The canonical inventory is the NUL-delimited result of:

```bash
git ls-files -z --cached --others --exclude-standard
```

Each entry uses this machine-readable form and a canonical path relative to the
repository root:

```text
- `path/from/repository/root` — One-line responsibility and authority note.
```

Descriptions SHOULD distinguish an authoritative input from a generated
artifact, test, example or operational wrapper. `doccheck` compares paths in
both directions and rejects missing, stale, duplicated or non-canonical
entries. It also requires the `Version suivie` banner to match
`internal/version/version.go`.

When a file is added, add its atlas entry in the same change. When a file is
removed, remove its entry. When a file is renamed, update its path and review
its description rather than treating the operation as a blind text rename.

## Executable documentation claims

Some prose is load-bearing: a stale default, security boundary or release rule
could mislead an operator even when compilation succeeds.
`scripts/check_documentation_claims.sh` uses explicit literal assertions to
link selected claims across their authoritative implementation, configuration
example, architecture description and agent instructions.

Add or update an assertion when all of these conditions hold:

- the fact is stable and operationally or security relevant;
- two or more repository surfaces must agree;
- silent drift would be worse than an explicit maintenance failure;
- a deterministic assertion can identify the expected source text.

Literal assertions are intentionally simple and visible. They SHOULD NOT cover
every sentence, and MUST NOT replace unit, integration or security tests. If a
refactor intentionally changes asserted text, update the implementation, prose
and assertion together after verifying that the underlying contract still
holds.

## Roadmap, changelog and version traceability

Documentation quality also covers the claim that work has shipped:

- `docs/ROADMAP.md` is authoritative for task IDs, scope, order and status;
- a completed `GPF-*` task MUST appear in a dated, versioned changelog section;
- a task named by a released changelog section MUST be marked complete in the
  roadmap;
- `internal/version/version.go`, the README version banner, roadmap banner,
  atlas banner and release changelog section MUST agree for a release.

`scripts/check_release_traceability.sh` enforces the task relationship in both
directions. The other release scripts and Make targets enforce version and
changelog consistency. The `Unreleased` section may describe ongoing work but
does not prove that a task was delivered.

## Control chain

The repository deliberately composes one set of controls rather than defining
different local, CI and release standards:

```text
make docs-check
  ├─ make godoc-check  -> cmd/doccheck --scope go
  ├─ make atlas-check  -> cmd/doccheck --scope atlas
  └─ make docs-assert  -> scripts/check_documentation_claims.sh

make check
  ├─ format, Protobuf lint, tests and go vet
  └─ make docs-check

make release-check
  ├─ make check
  ├─ generated-code reproducibility
  ├─ version, changelog, roadmap and bidirectional traceability
  └─ build and embedded version checks

make release-prepare -> make release-check before the release commit
make release         -> make release-check again on clean main, tag, atomic push
GitHub CI/release    -> make release-check on remote runners
                     -> make postgres-test with a dedicated PostgreSQL service
```

A contributor or agent MUST NOT bypass a failing documentation gate. Fix the
authoritative source, its documentation or the checker as appropriate. A
checker change requires tests demonstrating both the accepted and rejected
behavior.

## Change workflow

Before considering a change complete, apply every relevant row:

| Change | Required documentation work |
| --- | --- |
| Add or change a Go package/API | Update package and API contract comments; run `make godoc-check`. |
| Change a Protobuf API | Update `.proto` comments, regenerate bindings and run Buf lint/generation checks. |
| Add, remove or rename any non-ignored file | Synchronize `docs/atlas.md`; run `make atlas-check`. |
| Change a stable default or security/operational promise | Update all owning surfaces and the executable claim when appropriate. |
| Start, complete, reorder or rescope a roadmap task | Update `docs/ROADMAP.md` in the same change. |
| Prepare a release | Synchronize version banners and changelog, then run `make release-prepare`. |

For any documentation-sensitive change, the minimum local command is:

```bash
make docs-check
```

Before normal handoff, run `make check`. Before a release commit, run
`make release-prepare`; publication through `make release` rechecks a clean
`main` before creating and pushing the annotated tag.

## Definition of done

Documentation work is complete only when:

- public contracts describe caller-relevant semantics and invariants;
- authoritative and generated files are clearly distinguished;
- the atlas matches the complete non-ignored file inventory;
- stable cross-file claims agree and have assertions where warranted;
- roadmap, changelog and version claims are honest for the current lifecycle;
- `make docs-check` and the broader gate appropriate to the change pass;
- the review confirms meaning and clarity beyond mechanical coverage.

## Reusing this contract in another Go repository

The model is intentionally portable. Another repository can adopt it for
Codex, Claude or human contributors without copying application-specific
architecture decisions.

1. Copy and adapt this document as the single normative documentation
   contract; preserve the requirement levels and identify local sources of
   truth.
2. Put a short mandatory reference in the repository-level agent instruction
   file (`AGENTS.md`, `CLAUDE.md`, or an equivalent entry point). Do not paste a
   second full copy of the rules there.
3. Port `cmd/doccheck` and its positive/negative tests, or implement equivalent
   deterministic checks for package/API comments and an exact file inventory.
4. Create `docs/atlas.md` and define its canonical inventory, syntax and version
   relationship.
5. Enable the Protobuf `COMMENTS` lint category when the project uses
   Protobuf, and keep generated artifacts subordinate to their source.
6. Add a small executable-claims script for stable cross-file operational and
   security facts. Select claims deliberately rather than asserting all prose.
7. Compose these checks into `docs-check`, the normal quality gate, CI and the
   release gate so the same policy runs everywhere.
8. If the project uses roadmap task IDs and a changelog, add bidirectional
   traceability. Otherwise document the local mechanism that proves what a
   release contains.
9. Require a checker change to include regression tests, and document all
   project-specific exclusions.

The current implementation has deliberate local assumptions: the version lives
in `internal/version/version.go`, atlas banners use `Version suivie`, roadmap
tasks use `GPF-NNN`, and automation is composed by this repository's
`Makefile`. A port MUST either preserve those conventions or change the checker,
tests and this contract together. Copying only the prose does not provide the
assurance; the value comes from coupling clear rules with deterministic local
gates.
