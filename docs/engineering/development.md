# Developing Wake

[AGENTS.md](../../AGENTS.md) owns repository constraints and delivery approval.
Start with the user's task and [current coverage](../product/coverage.md).
Code, tests and checked-in tool configuration are the implementation reference.

## Workspaces and tools

Use an isolated checkout for a reviewable change. For a manual demonstration, run a disposable
instance with synthetic evidence and its own configuration, data, state, runtime
paths and admin port. Do not use private operator reports as repository fixtures.

Mise is the task runner. Run `mise install` in a new checkout. Use `mise tasks`
and [.config/mise.toml](../../.config/mise.toml) to discover tasks, dependencies
and exact tool pins; [go.mod](../../go.mod) owns Go dependencies. Keep the lock file
aligned with intentional tool updates. Avoid machine-global configuration changes.

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
to reset and serve. The two `storage.*_path` settings and their overrides select
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

Test behavior at the boundary that owns it. Use focused checks for changed code
and affected callers. Persistence changes need rollback/replay and relevant restart
proofs; shared state/lifetime changes need race coverage; browser behavior needs
functional browser checks. Concurrency tests coordinate observable events rather
than sleeps. Keep tests focused on supported behavior and meaningful failure boundaries.

Expand checks when a failure or new concern warrants it. Full suites are available
but are not mandatory for every local change. CI workflows and task definitions
own exact execution. Report checks actually run and material gaps; prior results
are not current evidence.

Generated templ, sqlc and OpenAPI output is excluded from behavior coverage.
Check generated-source freshness and exercise the handwritten boundaries that
produce and consume it. Coverage thresholds still apply to handwritten code.

Keep source stable while generators or checks read it. SQL, public contract and templ source
changes require their generated-output checks. Stage completed code/generated/docs
changes and run `mise run source:check` before code delivery. Documentation-only
changes check links, ownership, preservation of intent and diffs; do not run
unrelated generators or application suites.

## Changes affecting retained data

For changes to normalization, identity/link selection, reconciliation or financial
rules, state whether previously processed history remains valid. If existing
results can change, identify the affected inputs and include an explicit update
procedure with the change. Recalculate from retained claims when sufficient;
reinterpret retained original reports when their normalized meaning changes.
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

The FIGI authority development format is an explicit, approved warehouse-disposal
exception to that retained-history procedure. It changes source interpretation,
identity grouping and public instrument shape; old warehouses fail closed. Deploy
by preserving any needed old evidence/decisions, explicitly recreating the warehouse
and reacquiring history with the reusable encrypted access seed. There is no
compatibility conversion, automatic updater or rebuild command. The general
retained-history requirement above continues to apply to later changes.

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

## Browser and frontend verification

Build functional structure and data in the existing shell. Browser checks cover
page/form/table structure, exact displayed data, navigation, commands, validation,
progress/error/conflict states and user-visible outcomes. Keep semantic labels,
working controls and keyboard access in each slice.

[DESIGN.md](../../DESIGN.md) is the visual standard. Choose focused affected workflows, with broader review at coherent
boundaries. Screenshots supplement functional checks and require inspection.

Use isolated synthetic instances and the supported Chromium workflow; keep the
browser sandbox enabled. Tests must not use a personal browser profile or source
credentials. Consult task definitions for browser discovery, environment switches
and artifacts. Source assets and their vendor inventory own exact library versions.

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

## Socket consumer acceptance

[Current coverage](../product/coverage.md) owns acceptance;
the [OpenAPI contract](../../api/openapi/v1.yaml) owns exact wire semantics. The
standalone [Go consumer](../../testdata/socket-consumer/main.go) has its own module
and no Wake imports. It reads only the socket and validates a complete accepted
snapshot against its embedded copy of the published OpenAPI contract. Generation
and checks keep that copy identical to the owning specification. The consumer
rejects incomplete responses, duplicate keys or resources, invalid links and
conflicting content for a retained resource revision. Exact decimals remain text.

The consumer commits its complete mirror and opaque version together in its own
private SQLite database. A failed download, validation or transaction preserves
the previous mirror and version. An unchanged ETag avoids download and persistence. Its private mirror
has an explicit application identity and format. Existing files are admitted through
an immutable read-only check before any write or journal setup. Unrecognized files
fail closed with instructions to preserve them and choose a new state path; the
example never initializes an unrelated existing database as its mirror.
Invalid saved state fails closed and remains available for inspection. Structured
results go to stdout; immediate/periodic progress and failures go to stderr. A
warehouse change is explicitly reported after a validated new baseline commits.

Run the automated synthetic proof:

```sh
mise install
mise run test:socket
```

This task runs consumer behavior/race checks, builds the independent binary and
runs it against a separate daemon process over the actual private Unix socket.
Controlled providers and disposable stores exercise exact values, conditional
reads, disconnect/restart, source correction conflict raise/clear, successive
operator corrections, repeat-safe mirroring, access recovery, warehouse replacement
and follow-mode progress/interruption. It is part of `mise run ci`. Persistence and
handler tests cover snapshot consistency, transactional rollback, unchanged claims,
late relationships, revision-bound explanations and structured read-only errors.
Consumer checks cover interrupted responses, malformed/incomplete protocol, failed
persistence and fixture assertions.

For a manual review, start a disposable configured Wake instance with synthetic
history, then use its actual socket path and an existing directory for client state:

```sh
mise run socket-consumer:sync -- --socket /absolute/private/runtime/wake.sock --state /absolute/private/client/mirror.db
mise run socket-consumer:follow -- --socket /absolute/private/runtime/wake.sock --state /absolute/private/client/mirror.db
```

Stop the consumer, use **Correct accepted data** in the admin console to change an
existing price twice, then restart it with the same state path. Its mirror contains
the newest accepted revision. Repeat synchronization to verify the version remains
unchanged, then restart an otherwise idle daemon and repeat. Reset only a disposable
warehouse to verify a clearly reported new baseline. To exercise source corrections,
conflict raise/clear and late relationships, run the controlled processing scenarios
in `mise run test`; do not modify Wake SQLite stores in a manual consumer review.

Fixture assertions are an array of `{kind, path, equals, matches}` entries. Dot paths
select fields in canonical snapshots; exact decimal expectations are JSON strings,
and `null` differs from zero. Add `--assert /absolute/path/assertions.json` to a sync
or follow command. Assertion runs also verify individual resource lookups and
separate explanation revisions. The supplied
[Confirmation assertions](../../testdata/socket-consumer/fixtures/confirmations.json)
match the synthetic account used by `test:socket`. Stop the daemon to check visible
transport failure; select a state path below a missing/unwritable directory to check
persistence failure; change an expected value to check assertion failure. Preserve
an existing state file when testing failures. The consumer never contacts upstream
providers and does not extend the production operator CLI.

The provider proof uses disposable Go workspace files to compile an unrelated
module and run it through the actual private host and SQLite store. Run
`mise run test:provider` for that proof, including race detection. CI and behavior
coverage include the same proof; the production module imports no fixture.

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
The retained-data update requirement above still applies; the shared calculation
does not provide an automatic historical updater.

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
