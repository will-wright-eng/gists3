# 003 — The CLI Is the Product

**Status:** Accepted (2026-07-08)
**Supersedes:** the library-first premise running through
[000-design.md](000-design.md) §1 and the pre-rewrite README

---

## Decision

`g3` — the distributable CLI binary — is this module's product. The
S3-shaped Go package is its internal engine, not a public API. Accordingly
the package moved from the module root to `internal/gists3`, giving the
repository a standard application shape with an explicit lineage from the
entry point:

```
cmd/g3/main.go                 the product
  └── internal/gists3          S3-shaped engine (Client, operations, config)
        └── internal/gistapi   GitHub REST transport + wire types
internal/gists3test            black-box suite over the engine
cmd/example                    engine lifecycle demo / live smoke test
```

The module root now holds no Go files — only module metadata, docs, and
tooling.

## Context

000-design.md was written library-first: importers were the audience, the
CLI a §9 stretch goal. Once the CLI became the point, keeping the engine at
the module root had costs and no benefits: root placement exists solely to
make `import "github.com/will-wright-eng/gists3"` work for outsiders, a use
case that no longer exists — while `internal/` placement buys
compiler-enforced freedom to change the engine's API without ceremony.

## Consequences

**Superseded design clauses** (000-design.md, pending its WP8 rewrite):

| Clause | Old rationale | Standing now |
|---|---|---|
| §1.1 "drop-in familiarity" for importers | S3-literate *consumers* read call sites without docs | Internal architecture principle: S3 vocabulary still shapes the engine and maps 1:1 onto the aws-cli-flavored command surface |
| §1.3 / README "migrate to real S3 by swapping the constructor" | Consumer portability promise | Applies to this codebase only: `g3` could re-target real S3 by swapping the engine |
| §4.1 zero dependencies | Empty dependency graph *for importers* | Still enforced, now as binary hygiene: a single static `g3` with a clean supply chain |
| §6.5 no exported interface type | Don't freeze the method set for consumers | Moot — there are no consumers; the advice stands for internal use anyway |
| Root-package layout (§3, already amended by 001 §3.1) | Import path requires root placement | Fully replaced by the tree above |

**What deliberately does not change:**

- The engine keeps its S3-shaped API, godoc discipline (§5.5), typed
  errors, and behavioral contracts (§5.4). The 37-test black-box suite and
  85% coverage carry over untouched — only import paths changed.
- The library's "never ambient" credential rule (§5.6.1) still governs the
  engine; ambient resolution (env, config, gh) stays in the binaries.
- `cmd/example` remains as the engine's live smoke test, exercised by the
  integration workflow.
- 001-roadmap work packages survive intact: WP1 (and its
  [002 plan](002-cli-cp-ls-rm.md)) was already CLI-first; WP2–WP5 now serve
  CLI features rather than importer features; WP8's scope grows to include
  the table above.

**Reversal cost.** If a public library is ever wanted again, the move is
one `git mv` of `internal/gists3/*.go` back to the root plus an import-path
sweep — nothing about the engine's design assumes it is internal.

## Verification

Post-move: `make check` green (vet both tag sets, staticcheck, race suite,
build), `make cover` unchanged at 85.4%, and the compiled `g3 ls` ran
against the live API.
