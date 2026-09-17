# Documentation quality policy

This policy keeps documentation close to the code and turns structural drift
into a build failure. It complements architectural review; automated checks can
prove that documentation exists and remains connected, not that every sentence
is semantically perfect.

## Go source

- Every non-generated package has one package comment. Commands use a
  `Command <name> ...` comment.
- Every exported type, function, method, constant and variable has a GoDoc
  comment beginning with its name.
- Exported structure fields are documented because configuration units,
  ownership and secret handling are part of the contract.
- Tests and generated files are excluded from the mechanical GoDoc rule.
- Private helpers are commented only when their invariant or implementation is
  not obvious.

Useful comments explain contracts: security boundaries, ownership, units,
transaction scope, idempotence, concurrency, retries, lifecycle and failure
semantics. Comments that merely restate Go syntax should be rewritten or
removed. Source comments are written in English to match Go identifiers and
tooling; user and project documents may remain in French.

## Protobuf contracts

Files below `api/` are authoritative and generated bindings are never edited by
hand. Buf's `COMMENTS` lint category requires documentation for services, RPCs,
messages, enums, enum values, oneofs and fields. Comments must describe units,
authorization and idempotence where applicable.

## Repository atlas

[`atlas.md`](atlas.md) contains exactly one entry for every tracked or new,
non-ignored repository file. Entries use this machine-readable form:

```text
- `path/from/repository/root` — One-line responsibility and authority note.
```

`doccheck` compares exact paths in both directions. It rejects missing, stale or
duplicate entries and verifies that the atlas version matches
`internal/version/version.go`. Generated files remain listed in the atlas but
are explicitly identified as generated.

## Executable claims

`scripts/check_documentation_claims.sh` re-derives a small set of load-bearing
claims from source, examples and architecture documentation. Add an assertion
when a factual value is important enough that silent drift would mislead an
operator or weaken a security guarantee.

Assertions intentionally cover only stable, high-value facts. They are not a
replacement for unit tests or architectural review.

## Contributor workflow

Run the documentation gate whenever an exported API, Protobuf contract,
tracked file or load-bearing documented value changes:

```bash
make docs-check
```

`make check`, CI and `make release-check` include the same gate. Update source,
tests, documentation, atlas and assertions in the same commit.
