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
to reset and serve. The three `storage.*_path` settings and their overrides select
the actual files. Missing files are harmless, and reset creates no directories or
databases. `--json` reports selected and removed files on stdout; activity and errors
go to stderr.

Resetting credentials preserves financial history and pauses upstream work for
access repair and explicit resume. Resetting the warehouse preserves connection
settings and encrypted credentials. The fresh warehouse starts paused; resume
runs bounded discovery, and account confirmation grants synchronization authority.
The operational store belongs to its warehouse and is rebuilt from durable inputs
when missing or associated with a different warehouse. A failed deletion reports
the files already removed and returns failure; preflight refusal deletes nothing.

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

Keep source stable while generators or checks read it. SQL, public contract and templ source
changes require their generated-output checks. Stage completed code/generated/docs
changes and run `mise run source:check` before code delivery. Documentation-only
changes check links, ownership, preservation of intent and diffs; do not run
unrelated generators or application suites.

## Coding conventions

Follow the formatter and lint configuration. Go files carry the repository's MPL
headers. Enforce financial invariants at constructors/parsers and preserve immutable
values with private representations. Exact decimal text remains authoritative;
round only at deliberate calculation/display boundaries.

Handwritten production SQL belongs in external SQL sources; Go owns orchestration
and mappings. Package sentinel errors are typed constants, checked with `errors.Is`.
Use deliberate published provider contracts and narrow consumer-owned internal
interfaces. Source and contract tests define signatures, layouts and behaviors.

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

The provider proof uses disposable Go workspace files to compile an unrelated
module and run it through the actual private host and SQLite store. Run
`mise run test:provider` for that proof, including race detection. CI and behavior
coverage include the same proof; the production module imports no fixture.
