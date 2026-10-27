---
name: wake-docs
description: Maintain Wake documentation when creating, reorganizing, or synchronizing product, engineering, provider, research or usage guidance. Minor typo-only edits do not require this workflow.
---

# Wake documentation maintenance

Use the active checkout, including staged and unstaged changes. Repository scope
and delivery approval live in [AGENTS.md](../../../AGENTS.md). Use the
[docs index](../../../docs/README.md) to find document owners and the
[development guide](../../../docs/engineering/development.md#documentation-maintenance)
for delivery checks.

## Folder purposes and rules

Choose the folder by the question the material answers. Each document needs one
job within that folder's purpose. Before adding a folder, establish its distinct
purpose and content rules here; do not create another home for existing guidance.

### `docs/product/`: what Wake is for and what its financial concepts mean

Preserve the north star, user needs, product boundaries, financial meanings and
durable policy decisions. Explain intent and rationale independently of a release
or implementation. Broader product ambition does not establish implemented coverage.

Keep schedules, task status, port strategy, package layouts, database schemas and
API catalogs out. Visual design belongs in root `DESIGN.md`.

### `docs/engineering/`: how engineering decisions support the product

Explain high-level architectural responsibilities, boundaries, consequential
technical choices and rationale, plus the development and verification workflow.
Architecture describes the design; development guidance describes how to work on it.

Keep temporary implementation tactics out of durable architecture guidance. Do not
narrate the codebase or maintain prose
copies of packages, signatures, schemas, configuration options or test results.
Source, tests, tool configuration, CLI help and OpenAPI own those details.

### `docs/providers/`: how to configure and understand a particular source

Provide practical source-specific access/query setup, input meaning, reporting
limitations, pacing and troubleshooting that help someone use or interpret that
source. Keep provider-specific interpretation choices here when they require
explanation beyond source code; use primary sources for external factual claims.

Link to shared financial policy and the README workflow rather than redefining
them. Do not put core architecture, generic extension contracts or speculative
integrations here. Avoid copied vendor manuals and payload/field catalogs that
source code or upstream documentation already supplies.

### `docs/research/`: what external research and experiments have taught us

Memoize durable findings that would save someone a meaningful repeat investigation.
Use focused memos organized around a question, with the conclusion first. Record
sources, relevant dates or versions, experiment conditions, results and limitations.
Distinguish documented facts, observations and inferences. Link to primary external
sources and reproducible experiments rather than copying manuals or transcripts.
Keep private reports and credentials out of research artifacts.

Research provides evidence, not requirements, acceptance or implementation status.
Adopted product policies belong in product docs, architectural decisions in
engineering; link to supporting research where useful.
Reading is optional and task-specific, not a new default prerequisite.

Keep reusable findings rather than investigation logs, package inventories or task
progress. Failed experiments belong when they explain a useful constraint. Correct
or retire stale findings; preserve relevant conditions when behavior varies by
version or environment.

## Root documents

`docs/README.md` is navigation and document ownership, not another specification.
The repository `README.md` is a concise, descriptive setup and usage helper for
selected v0.1.0 behavior. Write it in the present tense, without future speculation
or deprecated workflows; it is not authoritative for requirements, acceptance or
implementation status. Keep commands grounded in help/source and avoid invented
screen labels. Source-specific setup detail may link to `docs/providers/`.

`docs/product/coverage.md` owns supported financial coverage, compatibility limits
and the boundaries required to verify them. Keep delivery chronology and completed
task inventories out of current documentation.

Root `DESIGN.md` is the portable visual-design standard. `AGENTS.md` owns repository
instructions. Link to those owners instead of creating copies under `docs/`.

## Maintain and verify

Read the affected owner documents and relevant selected work before editing.
Update coverage, design decisions and descriptive helpers with their owners. Resolve
competing claims by fixing their placement and ownership rather than adding caveats
or duplicate explanations.

Docs do not substitute for code. For implementation synchronization, inspect the
actual diff and affected source, tests, configuration and public contracts. Check
CLI claims against dispatch/help, wire claims against OpenAPI, storage claims
against SQL sources and commands against Mise tasks. Edit authoritative sources,
not generated output. Retired code does not dictate the selected release's usage
helper; supported coverage belongs in `docs/product/coverage.md`.

For moves or deletions, repair inbound links and anchors. Preserve useful current
intent and unresolved work unless the user changes scope. Remove redundant or
obsolete material rather than creating replacement inventories or delivery logs.

Check local links, anchors, task references, folder fit and diffs. Report only
validation actually performed and material gaps; prior evidence is not a new test
result. Stage completed changes and follow AGENTS.md for review and publication.
