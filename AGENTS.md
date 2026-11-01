# Wake

Local-first, self-hostable financial evidence warehouse and synchronization
daemon. Licensed under MPL-2.0 ([LICENSE](LICENSE)).

## Read for the task

- Start with the user's request, [product brief](docs/product/brief.md) and
  [current coverage](docs/product/coverage.md) as relevant.
- Use [financial language](docs/product/domain.md) for meanings and policy choices,
  and [architecture](docs/engineering/architecture.md) for high-level boundaries.
  Source and tests own package layouts, schemas, signatures and exact behavior.
- Use [DESIGN.md](DESIGN.md) as the portable visual-design standard and
  [development](docs/engineering/development.md) for tools and check selection.
- The [README](README.md#using-wake-v010) is a descriptive quick helper for
  v0.1.0 use, not an authoritative specification. Keep it in the present tense,
  within selected release scope, without deprecated workflows or future speculation.
  [IBKR setup](docs/providers/ibkr-flex.md) provides source setup help.
  CLI help and published contracts own exact public interfaces.
- Use [$wake-docs](.agents/skills/wake-docs/SKILL.md) for substantive documentation
  changes. The [index](docs/README.md) assigns document ownership. Do not recreate
  deleted historical plans or prose copies of implementation details.

## Constraints

- Scope work to the user's request; do not expand into unrelated changes.
- Wake is one daemon (`wake serve`); packaging wraps it. The core has no
  systemd, launchd, or Docker dependency.
- Preserve immutable evidence artifacts/records. Normalize source records,
  reconcile state, and retain traces and disagreements.
  Corrections append state; clients consume the reconciled model projection.
- Keep connection/account, observation/event, and evidence/canonical state
  distinct. Preserve source currency, dates, time precision, and exact values.
- Keep validated exact decimal text authoritative inside Money, Quantity, and
  Price. Convert explicitly to `go.trippwill.dev/num` without losing precision.
  Round only at explicit calculation or display boundaries; no authoritative
  `float64` amounts.
- SQLite is private storage with Wake as sole ordinary writer. Follow the
  implemented schema and domain contracts; do not invent speculative layouts.
- Acquisition success, processing, freshness, and reconciliation status are
  distinct. Persist progress and pacing; preserve replay/idempotency, atomic
  derived writes, preflight checks, credential lifetime, and shutdown joins.
- Keep credentials encrypted at rest, secret-safe in memory and diagnostics,
  and fail closed on configured missing/invalid keys. Setup is CLI-led;
  daemon startup must not generate key material.
- Use Go stdlib HTTP, `database/sql`, `modernc.org/sqlite`, sqlc, templ, and
  embedded browser assets. No Node/npm or web framework; introduce a browser
  build pipeline only for a demonstrated need. Published provider extension
  contracts are deliberate public boundaries; internal collaborator interfaces
  belong to consumers. Do not create empty future packages or universal service APIs.
- Until the first version tag, replace internal APIs directly. Unsupported
  pre-release databases fail closed with recreate instructions; no compatibility
  migrations, aliases, or automatic database deletion.
- No trading/order execution, brokerage gateway, live market-data platform,
  full tax engine, general ledger, SaaS multi-tenancy, or distributed replication.

## Development and delivery

- Mise is the only task runner. Pins/tasks live in [.config/mise.toml](.config/mise.toml).
  Run `mise install`. Select tests and checks from the changed code, affected
  callers, and integration boundaries; include race, browser, or coverage checks
  when the change warrants them. Stage generated changes and run
  `mise run source:check` before delivery. Full suites are optional when the scope
  warrants them or the user requests them; report the checks run and material gaps.
  Documentation-only changes check links, information preservation, and diffs.
- Go files have `SPDX-FileCopyrightText: 2026 trippwill` and
  `SPDX-License-Identifier: MPL-2.0`, except generated files under
  `internal/persistence/sqlite/sqlc/`. Keep that directory as native sqlc output;
  it is excluded from all linters and formatters. Follow enforced lint/formatting
  elsewhere; detailed whitespace and generator rules are in the development guide.
- Prefer sealed immutable values, constructor/parser invariants, private
  representations, and read-only accessors; no exported `Validate` methods.
  Small direct newtypes are a limited exception. Mutability needs evidence.
- All handwritten production SQL belongs in external `.sql` files, including
  bootstrap, migrations, connection settings, inspection and maintenance. Generated
  sqlc Go may embed its external SQL sources; tests are exempt. Use typed `const`
  sentinel errors and `errors.Is`; see the development guide for the pattern.
- Keep current documentation aligned with the change.
  Changes to design invariants must update their owning product, engineering or
  provider docs in the same change; a chat, plan, or PR description is not a substitute.

## Everyday workflow

- When using subagents, explicitly select an appropriate model and reasoning
  level for each task. Every presented plan includes a recommendation for the
  implementation model and reasoning level.
- Normally use one chat, worktree, and branch per reviewable change, usually
  one PR. Reuse a suitable existing checkout. See the [development guide](docs/engineering/development.md#workspaces-and-tools)
  for setup and isolated review instances.
- Implement, update affected docs, validate, and stage freely. Present the
  changed behavior, checks actually performed, material gaps, and how to try it.
  Use one Conventional Commit per logical change, explaining why.
- Deliver coherent changes with meaningful functional verification. See the
  [development guide](docs/engineering/development.md#browser-and-frontend-verification)
  for browser and frontend checks.
- Obtain one explicit approval covering both commit and push of the reviewed
  changes to the stated destination. PRs are the default: publish the branch
  and open/update the PR, then obtain separate merge approval after CI and review.
  For a requested direct push to main, the combined commit-and-push approval
  also accepts the change. Approval to continue work does not authorize publication.
- PR merge approval is final acceptance. Acceptance includes any operator
  testing or demonstration used in review; there is no separate Operator UAT,
  or milestone sign-off. Record only checks actually performed.
