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

For a foreground development instance, run `mise run bin -- config init --generate-key`, then
`mise run bin -- access set password` and `mise run serve`. Select the
same TOML for all three commands. Sign in with the password entered at the prompt.

Use `mise run bin -- help` for application commands. The [README](../../README.md)
provides a quick setup and usage helper. Keep automation in repository tasks
rather than app-specific wrappers.

To reset development storage, stop Wake and preview the selected files:

```sh
mise run bin -- storage reset financial
mise run bin -- storage reset financial --confirm
mise run serve
```

`wake storage reset` accepts `credentials` or `financial` and previews by default.
Only `--confirm` deletes files. The selected store's SQLite sidecars and
super-journals are included. Reset checks ownership without opening SQLite or
loading credentials. It refuses locked stores, unsafe links and protected files.
A failed deletion reports which files were already removed.

For a disposable instance, pass the same `--config /absolute/private/dev/config.toml`
to reset and serve. The storage path settings select the actual files. Reset
creates no directories or databases. `--json` reports selected and removed files
on stdout; activity and errors go to stderr.

Local daemon verification needs only the financial store and local access. The
provider integration harness uses separate private financial, synchronization,
and access files. Use synthetic data, keys and credentials. Copy a closed
encrypted access file only to verify its independent storage and original
identity binding.

## Appliance packaging and releases

[.container/](../../.container/) owns the runtime image, entrypoint, health check,
Quadlet and synthetic acceptance flow. Use the project’s Mise toolchain and
rootless Podman on Linux to build and test the appliance.

```sh
mise run container:build
mise run container:test
mise run container:test -- --quadlet
```

The first test runs isolated containers. `--quadlet` additionally needs a user
systemd manager, available ports 8787/8788 and Podman 5.6 or newer. It installs a
uniquely named temporary unit from a local HTTP URL and removes it afterward. It checks the custom storage
drop-in across URL replacement with a path containing spaces. Tests use only
synthetic passwords, client grants and retained evidence. Run this flow on Fedora
with SELinux enforcing for packaging changes; a disposable host reboot additionally
checks boot activation with lingering enabled.

The build uses the release executable and a digest-pinned runtime base containing
CA certificates, timezone data and the HTTP health-check client. `WAKE_IMAGE`
selects a local image name, defaulting to `localhost/wake:dev`.
`WAKE_IMAGE_VERSION` sets the release label. Keep runtime pins deliberate and
verify both target architectures when updating them.

CI builds and tests the image on native amd64 and arm64 runners. Push and manual
builds save those tested images, wait for the complete CI gate, and publish a
combined GHCR manifest through `container:publish`. Untagged branch builds use
`sha-<full-commit-SHA>` image tags. The publication job summary supplies the image
and matching Quadlet URL. PR builds do not publish images; Markdown-only pushes
skip image builds. A maintainer can manually run `ci` on a branch to build and
publish its current commit with the full gate, including a Markdown-only commit.
The [appliance guide](../../docs/deployment/wake-appliance.md#use-an-untagged-commit-or-branch)
describes installation from those images and a local-build fallback for an arbitrary revision.

Before tagging a release, update `Image=` in the Quadlet and the appliance
guide's concrete release URLs to that version. Publication
rejects mismatched image labels, revisions or architectures; a release tag must
also match the Quadlet's default image. The raw GitHub URL at the same tag supplies
the matching Quadlet. Make the GHCR package public for
anonymous installation and verify its published URL/image flow before announcing
the release. Updating the Quadlet leaves local `.container.d` overrides intact.

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

Put pure financial calculation code in the calculation subpackage.
`TestFinancialCalculationImports` checks direct imports in every production Go
file in that package, including files with build tags. Review changes to
allowed dependencies for transitive I/O and runtime access.

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

Independent repositories can build the development tool at an immutable Wake revision.
Set `WAKE_SRC` to an unused checkout path and `WAKE_REV` to the full commit ID:

```sh
git clone https://github.com/trippwill/wake.git "$WAKE_SRC"
git -C "$WAKE_SRC" checkout --detach "$WAKE_REV"
(cd "$WAKE_SRC" && mise install && mise run build:wake-dev)
"$WAKE_SRC/.cache/wake-dev" serve --workspace "$PWD/wake-session"
```

Wait for the JSON readiness event with the API endpoint and dedicated token-file path.
Interrupt the foreground process to stop it. The same revision can resume the workspace.
Use `reset --workspace /absolute/path` to preview cleanup and add `--confirm` to remove the
reserved `.wake-dev` directory. Keep personal files outside that directory.

Run `accept --workspace /absolute/fresh/path --consumer /absolute/adapter` to verify the
history and positions mirrors through interrupted reads, reconnect, restart, source and operator
corrections, identity merges, access recovery and warehouse replacement.
Select either view with `--profile history` or
`--profile positions`. The [adapter protocol](../../cmd/wake-dev/README.md) describes the
process interface. The full recovery and correction suite also runs through `mise run test:api`.

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
checkpoint and comparison identifiers at version 1. Advance the identifier whose
meaning changed. Private store formats advance independently as their schemas change.

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
the first supported release formats. When either format changes, embed the forward migration
as external SQL and update the matching fresh schema. Migration files run inside
Wake's attached SQLite transaction. Keep transaction control and format metadata
updates in the runner. Test the upgrade from the earliest supported data and compare the
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
changes. Give each document one job, such as explaining product intent, financial
meaning, high-level architecture, current coverage or an operator workflow.
[The index](../README.md) identifies those owners.

Document decisions and rationale that source alone cannot explain. Do not maintain
parallel catalogs of packages, schemas, CLI flags, API fields, rule implementations
or test results. CLI help, public contracts, source and tests own those details.

Keep everyday usage help and direct executable instructions in the root README.
Use `docs/deployment/` for operator deployment and operation instructions, with
a short entry in the README. The README describes selected v0.1.0 behavior in the
present tense, without deprecated workflows or future speculation. Keep commands
grounded in help and source and use actual screen labels. Coverage and limitations
belong in the coverage document. Remove obsolete documents and repair inbound links.

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
