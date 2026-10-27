# Architecture

This overview describes Wake's architectural responsibilities and consequential
choices. Code and tests define the implementation;
[current coverage](../product/coverage.md) owns supported behavior and limitations.

## One local application

Wake is one daemon with one ordinary writer of private SQLite storage. The CLI
provisions local access, the admin console handles user workflows, and a read-only
socket serves downstream applications. Deployment wrappers do not define the core.
Wake uses Go, standard-library HTTP, SQLite/sqlc, templ and embedded browser assets.
The build configuration and dependency manifests own exact tools and versions.

## Responsibilities

| Boundary | Responsibility |
| --- | --- |
| Composition | Select and connect concrete implementations and compiled provider packages. |
| Host | Configuration, credentials, durable work, scheduling, progress, cancellation and shutdown. |
| Source providers | External protocols and source-specific interpretation into qualified financial claims. |
| Core reference service | OpenFIGI access, shared anonymous/keyed pacing, local cache and retained resolution evidence. |
| Financial application | Wake identities, links, accepted projections, uncertainty and retained operator decisions. |
| Persistence | Evidence, durable progress, atomic writes and rebuildable read views. |
| Presentation | Adapt application commands and queries to CLI, admin and read-only socket clients. |

Providers use deliberate published contracts that an external Go package can
implement. They receive neither SQL nor private storage/UI dependencies. Providers
report source meaning; core decides financial identity and acceptance. Small
internal collaborator interfaces belong to their consumers. Published signatures,
source-claim shapes and their proofs belong in code and contract tests.

## Evidence before conclusions

Preserve immutable evidence and qualified claims independently of accepted views.
Named/versioned rules and operator decisions explain conclusions through links.
Reference acquisition happens outside deterministic rule evaluation. Cache eviction
cannot erase evidence supporting an accepted result, and a refresh cannot silently
overwrite an operator correction.

Persist progress with the evidence or derived writes it acknowledges. External
calls do not hold storage transactions open. Work must be repeat-safe and survive
restart; failures leave useful progress and honest gaps. Acquisition, processing,
freshness and financial disagreement remain separate concerns. Individual failure
must not stop unrelated work. The source/tests own exact state machines, pacing,
transaction mechanics and storage layout.

## User and consumer boundaries

The admin console exposes understandable work, values, conflicts and next actions.
Detailed evidence is optional. The socket exposes accepted financial views with
stable identities and compact conflict status; consumer changes include corrections
and relevant link/reference changes, not just new activity. Presentation adapters
share application meaning without depending on one another's UI or wire types.

[DESIGN.md](../../DESIGN.md) owns the visual standard. Exact CLI commands are in
help. The retained socket currently exposes no financial resources; the release
goal owns the later publishing boundary.

## Trust and recovery

Protect credentials at rest and keep secrets out of ordinary views, errors and
logs. Local setup explicitly provisions protection; daemon startup does not create
replacement key material. Source responses and imported files are untrusted input.
Keep administrative authority separate from read access. The local deployment
assumes a trusted OS user and private storage; wider access requires an explicit
trust decision rather than accidental network exposure.

Shutdown cancels and joins work before releasing resources. Recovery preserves
identity, evidence and decisions; derived views are reproducible. Unsupported
pre-release stores fail closed with explicit recreation guidance. Privacy depends
on the user's host and storage protection as well as Wake's credential handling.

## Persistence and work boundaries

The private single-file store has an explicit development format. Unsupported
formats fail closed before writable recovery, schema or journal changes. Startup
never converts or deletes a database; recreation is an explicit operator action.
Master keys remain operator-provisioned through setup.

Providers use public `financial`, `source` and `provider` packages. Registration is
explicit and starts no work. Source references remain qualified claims, with no
Wake financial IDs. Discovery returns candidates and grants no recurring access or
financial authority. Normalization receives retained bytes and a sequential claim
sink; it receives no credentials, reference lookup, SQL or presentation services.

The host scopes work and credential callbacks to a connection and joins admitted
operations on shutdown. Explicit retry uses the same per-connection admission,
refuses active work and advances the checkpoint revision so an earlier acquisition
cannot overwrite the retry. A source step's evidence, continuation revision, discovery
candidates and acknowledged progress commit atomically. Observed progress is
separate and can remain visible after an unacknowledged failure. Shared pacing
reservations and cooldowns survive restart. Unknown continuation versions remain
explicitly unavailable rather than resetting provider state.

Normalization writes retained records and its successful processing outcome in
one transaction. Failed normalization retains the original artifact and attempt
outcome while rolling back every record. Row rejection is an explicit retained
record; a failed host write aborts the attempt even if a normalizer swallows the
sink error. Replaying a successful artifact/version returns its retained outcome.
Records retain artifact locations and interpretation versions without producing
financial authority by themselves.

Operator confirmation commits a Wake account, its qualified source binding and
the intent to synchronize together. Discovery stops after its bounded attempt;
authorized acquisition starts independently. The daemon resumes eligible durable
work, retains pacing and unavailable ranges, and joins acquisition and reference
work during shutdown. Requested windows and reported coverage remain distinct.

Financial acceptance applies named/versioned authority, observation-matching and
instrument-linking rules transactionally. Native references corroborate Wake-owned
identities; separate fills remain separate. Matching observations add provenance.
Conflicting terms retain the accepted values and expose review status. Source
summaries, bookings and unsupported families stay in evidence without contributing
equity executions.

The reference service maps identifiers using only their type and value; source
currency and venue do not constrain identity lookup. It batches jobs under a shared
durable budget. Cache reuse and retained selection evidence have separate lifetimes: successful lookups
are reusable for seven days and successful no-match results for six hours, while
the evidence supporting accepted selections remains retained. Transient failures
are not negative results. Configured key failures require repair or explicit
selection of anonymous access. An unambiguous reference can enrich an existing
identity; unifying distinct existing roots requires review rather than a silent
merge. Financial processing remains useful with provisional reference identity.

Administrative commands use authenticated HTTP forms. Committed work triggers
SSE refresh hints; fallback polling reads the same status regions. Editable forms
remain outside those regions, and refreshes preserve focused controls.
