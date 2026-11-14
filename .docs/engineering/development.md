# Developing Wake

[AGENTS.md](../../AGENTS.md) owns repository constraints and delivery approval.
Start with the user's task and [current coverage](../product/coverage.md).
Code, tests and checked-in tool configuration are the implementation reference.

## Workspaces and tools

Use an isolated checkout for a reviewable change. For a manual demonstration, run a disposable
instance with synthetic evidence and its own configuration, data, state, runtime
paths and listener ports. Do not use private operator reports as repository fixtures.

Mise is the task runner. Run `mise install` in a new checkout. Use `mise tasks`
and [.mise/config.toml](../../.mise/config.toml) to discover tasks, dependencies
and exact tool pins; [go.mod](../../go.mod) owns Go dependencies. Keep the lock file
aligned with intentional tool updates. Avoid machine-global configuration changes.

Run `mise run build:release` to build the standalone executable at `.cache/release/wake`.
The release task disables CGo, removes filesystem paths with `-trimpath`, and strips
symbols and DWARF debug information with `-ldflags='-s -w'` to reduce binary size.
Use an ordinary build when debugger symbols are needed. `mise run build` continues
to check all Go packages with the existing development build settings.

For a foreground development instance, create a private instance directory with an
empty `config.toml`. Run `mise run bin -- credentials generate-key`, then
`mise run bin -- admin set-unlock`, and `mise run serve`. Select the same TOML for all
three commands.

Use `mise run bin -- help` for application commands. The [README](../../README.md)
provides a quick setup and usage helper. Keep automation in repository tasks
rather than app-specific wrappers.

To reset development storage, stop Wake and preview the selected files:

```sh
mise run bin -- reset warehouse
mise run bin -- reset warehouse --confirm
mise run serve
```

`wake reset` accepts `credentials` or `warehouse`, uses the configured file paths
and previews by default. Only `--confirm` deletes files. Credentials reset removes
the connections store; warehouse reset removes the financial warehouse and its
operational state. Both include SQLite sidecars and super-journals, without opening SQLite or loading
credentials; corrupt or unsupported databases do not prevent reset. Configuration,
external keys and browser access files are retained. Ownership of every existing
store is acquired before any deletion; locked stores, unsafe links and protected
files are refused.

For a disposable instance, pass the same `--config /absolute/private/dev/config.toml`
to reset and serve. The two `storage.*_path` settings select
the actual files. Missing files are harmless, and reset creates no directories or
databases. `--json` reports selected and removed files on stdout; activity and errors
go to stderr.

Resetting credentials preserves financial history and pauses upstream work for
access repair and explicit resume. Resetting the warehouse preserves connection
settings and encrypted credentials. The fresh warehouse starts paused; resume
runs bounded discovery, and account confirmation grants synchronization authority.
Operational state shares the warehouse file and lifetime. A failed deletion reports
the files already removed and returns failure; preflight refusal deletes nothing.

For credential-bearing test seeds, stop the seed writer before copying its encrypted
access file. Give every run its own private copy and warehouse; never point concurrent
profiles at the seed or a shared access file. Provision the protection key separately.
Tests and agents can then reuse protected credentials without receiving their plaintext,
and a warehouse reset preserves the test profile's access copy. Use synthetic keys and
credentials for ordinary automated verification.

## Select meaningful verification

Choose tests at the boundary that owns the behavior. Exercise exact values and
financial meaning in ordinary tests, rollback and replay in a disposable store,
and shared lifetimes under the race detector. Coordinate concurrent tests with
observable events. Keep browser checks for interactions such as focus, history
and refresh. Exercise route responses and static navigation through HTTP tests.

`mise run ci` runs the routine checks locally. Code CI on PRs, main and tags
uses `mise run test:race:focused` for shared state and lifetime proofs. Run
`mise run test:race` or dispatch the Actions `full` profile for the broad sweep.
Use `mise run test:coverage` for ordinary behavior coverage, including the
external provider and host modules. Generated sources have separate freshness
checks and are excluded from behavior coverage reporting.

Stage finished changes and run `mise run source:check` before code delivery.
Use `mise run docs:check` for Markdown and links. Report checks actually run
and material gaps.

### Browser and frontend verification

Use `mise run test:browser` for isolated synthetic Chromium workflows and
`mise run test:browser:repeat` to investigate intermittent failures. Keep the
browser sandbox enabled and use a disposable profile. Wait for the specific
document or interaction state, and use explicit CSS or XPath selectors.
[DESIGN.md](../../DESIGN.md) owns the visual standard. Inspect screenshots
alongside functional checks. After editing browser assets, use
`mise run assets:gen` and `mise run assets:check` for the embedded copies.

### API consumer acceptance

Run `mise run test:api` to exercise the independent
[example consumer](../../testdata/api-consumer/main.go) against a separate
daemon process over TCP with a dedicated client token. [Current coverage](../product/coverage.md)
describes the supported behavior, and the [OpenAPI contract](../../api/openapi/v1.yaml)
defines the wire format.

## Changes affecting retained data

For changes to normalization, identity/link selection, reconciliation or financial
rules, state whether previously processed history remains valid. If existing
results can change, identify the affected inputs and include an explicit update
procedure with the change. For activity or instrument selection changes covered by the retained-claims
calculation, increment `RetainedClaimsRule` to schedule the update. Rules affecting
different inputs must define their update scope in the same change. Recalculate
from retained claims when sufficient. Increment the provider interpretation
version when the normalized meaning of an original report changes.
An unchanged fresh report must not suppress work required by changed rules or
interpretation. Do not present affected results as current until that work succeeds.

Preserve immutable evidence, stable financial identities, operator decisions and
their authority, exact values and time precision. Refetching reports or resetting
the warehouse is not a substitute for updating retained history. Do not rely on
an operator remembering to run a generic maintenance action.

Verify the update against already processed synthetic history with no new source
claims. Check that the new interpretation or rule takes effect, active decisions
remain applied, and published values/explanations advance when their meaning
changes. Cover failure and repeat execution so an interrupted update cannot expose
partial results or duplicate financial activity. Record why no historical update
is needed when a change leaves retained results valid.

The first release starts the Wake-owned rule, interpretation, continuation,
checkpoint, comparison, and store format identifiers at version 1. Advance the
identifier whose meaning changed.

- Core rule and provider interpretation changes that affect existing financial
  results require a retained-history update or provider replay, respectively.
- Bump a continuation or checkpoint version when persisted progress changes
  meaning or can no longer be read as written. Specify how the affected work
  resumes or restarts, preserving pacing and retained evidence.
- Bump an equivalence policy when report comparison changes. Keep the policy and
  digest recorded for each existing receipt and verify deduplication at the
  policy boundary.
- Advance the affected store format when a supported store requires a storage
  upgrade. Warehouse and encrypted-access formats advance independently.

See [current coverage](../product/coverage.md) for storage compatibility and
evidence-preservation guidance.

## Forward store upgrades

The warehouse and access store each have an ordered migration chain starting at
their v1 release formats. When either format changes, embed the forward migration
as external SQL and update the matching fresh schema. Migration files run inside
Wake's attached SQLite transaction. Keep transaction control and format metadata
updates in the runner. Test the upgrade from retained v1 data and compare the
result with a fresh store at the target format. Keep migration SQL stable once
released because its contents identify the approved plan.

Ship a release note with every retained-claims rule increment. Key the note by
the new rule version in the upgrade baseline. Explain the change, which retained
results can change, and which financial views will be temporarily unavailable.
Preview and unattended startup show the same notes in version order, including
intervening rule changes. Missing notes prevent an upgrade before storage writes.

## Coding conventions

Follow the formatter and lint configuration. Go files carry the repository's MPL
headers, except generated files under `internal/persistence/sqlite/sqlc/`. Keep
that directory as native sqlc output, without added headers or formatter changes.
It is excluded from all linters and formatters; sqlc validation, generated-source
freshness checks, compilation and tests still cover it.

In handwritten Go, prefer modern syntax and standard-library helpers when they
express the same intent more directly: `any` instead of `interface{}`,
`slices.Contains` for membership, `slices.Backward` for reverse iteration and
`maps.Copy` for map copies. Use `strings.Builder` for repeated string concatenation
in loops; ordinary concatenation is fine for a few fixed parts. Preserve ordering,
ownership and error handling when replacing handwritten code with a helper.

Enforce financial invariants at constructors/parsers and preserve immutable
values with private representations. Exact decimal text remains authoritative;
round only at deliberate calculation/display boundaries.

Handwritten production SQL belongs in external SQL sources; Go owns orchestration
and mappings. Package sentinel errors are typed constants, checked with `errors.Is`.
Use deliberate published provider contracts and narrow consumer-owned internal
interfaces. Source and contract tests define signatures, layouts and behaviors.

Never encode semantic state in concatenated display phrases or recover it by
searching, splitting or comparing rendered wording. This applies throughout the
repository, including persisted failures used by scheduling. Use typed states,
reasons and machine codes; presentation owns labels. Protocol tokens and actual
text search remain ordinary text operations.

## Documentation maintenance

Use the [Wake docs skill](../../.agents/skills/wake-docs/SKILL.md) for substantive
changes. Each document has one job: product intent, financial meaning, high-level
architecture, current coverage or descriptive usage help.
[The index](../README.md) identifies those owners.

Document decisions and rationale that source alone cannot explain. Do not maintain
parallel catalogs of packages, schemas, CLI flags, API fields, rule implementations
or test results. CLI help, public contracts, source and tests own those details.

Keep everyday usage help in the root README. It describes selected v0.1.0 behavior
in the present tense, without deprecated workflows or future speculation. It is
not a specification, task list or acceptance gate. Keep commands grounded in help
and source; avoid inventing screen labels or implementation details. Coverage and
limitations belong in the coverage document. Remove obsolete documents and repair
inbound links.

Run `mise run docs:check` for Markdown style and local links, including anchors. Checks include
Git-tracked and unignored new Markdown files, including hidden directories and fixture
documentation. Prose has a 100-column limit; tables and code blocks are exempt. Keep code examples
and financial meaning intact when reflowing text. `mise run docs:links:external` additionally
contacts external sites and is an explicit, network-dependent check outside the ordinary gate.

Markdown tool versions are declared on the file tasks that use them and resolved
in the project lockfile. They are installed and activated for those tasks.

CI checks documentation on every change and skips application suites for Markdown-only
diffs. Tag builds and manual runs include the application suites.

## Financial calculation changes

Load retained claims and reference/link selections before calculation. Use the shared
pure calculation for processing and the prospective active decision set for both
preflight and saving. Keep persistence and upstream acquisition outside that code.
A correction check uses one read transaction and must not write decisions, selection
baselines, revisions, publication or operational state. Saving checks the same
revision and persists its decision and calculated result atomically. Releasing a
scope recalculates automatic terms while retaining other active choices.

Typed field policies own applicability, value kinds, optionality and scalar bounds;
editor labels and help remain in presentation. Reuse the retained activity codecs
and encode the authoritative published read model directly. Include constructor,
exact-text, disagreement, release and rollback cases when changing these rules.
The retained-data update requirement above still applies. A provider interpretation
version change automatically queues retained acquired reports for bounded processing,
including reports whose bodies have not changed. Changed core calculation rules
still need an explicit historical update procedure; the shared calculation alone
does not supply that updater.

## Provider extensions

Start with the independently compiled [synthetic provider](../../testdata/external-provider/fixture.go)
and its [host/store proofs](../../testdata/provider-integration/host_test.go).
Implement only the public capabilities needed by the source. A normalizer can
accept supplied evidence without an acquirer, account discoverer or credential
fields. Keep source formats, settings, credentials and protocol interpretation
inside the provider; composition selects registrations. Common admin forms use
the descriptor's fields. A connection's display name is host metadata, separate
from those provider settings.

Use the public [financial parsers](../../financial/value.go) for exact money,
quantity, price, currency and civil-date values, and the
[timestamp parser](../../financial/timestamp.go) for reported time precision. The decimal
parsers accept bounded finite base-ten syntax, expand exponents exactly and retain
fractional scale. They do not guess grouping, locale, missing values or financial
roles. A provider must explicitly interpret its own source syntax before calling
them; keep unknown amounts unknown and preserve original bytes and source locations.

Artifacts compare exact bytes by default. A provider can attach an optional
versioned equivalence fingerprint to exclude explicitly identified report-window
fields. Preserve every other byte in that policy and prove it with mutation tests;
never use accepted or corrected values to recognize source claims. Acquisition
receipts preserve actual byte digests and requested/returned bounds even when a
body is reused. Standardized claim identity excludes structural location while
retaining exact values, qualified references, time precision, unknowns and duplicate
multiplicity. Keep source claim comparison before corrections and enrichment.

Translate source protocol statuses to public progress phases and retry reasons.
Native diagnostic codes remain provider-owned identifiers; shared scheduling and
presentation do not interpret them. Qualify a reported event as distinct only when
the source establishes that meaning within its assigning authority, kind and scope.
An arbitrary native ID spelling never establishes fill identity in the core.
Run `mise run test:provider` to verify independent public imports, scoped secret
lifetime, durable pacing, atomic acknowledgement, cancellation, normalization
rollback and replay through actual host/storage boundaries.

## Admin read changes

Keep prerequisite, status, list and detail reads scoped to the question being answered.
Do not reintroduce exhaustive history reads into normal request or refresh paths.
Exercise bounded pages, stable identity ties, explicit unknown handling and exact
numeric ordering beyond floating-point precision. Use the registered SQLite driver
when adding collations; a zero-value driver does not inherit global registrations.
Warehouse and encrypted access formats have independent version checks, so an
unchanged private access seed remains reusable when only the warehouse changes.
