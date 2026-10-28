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
help. The [versioned financial contract](../../api/openapi/v1.yaml) owns the socket
interface; [development guidance](development.md#socket-consumer-acceptance) provides
runnable consumer checks.

## Canonical publishing and continuity

The private Unix socket serves finite, bounded, read-only HTTP/JSON requests from
the daemon's OS user. It publishes accepted accounts, instruments, listings and
activity, with warehouse-local Wake identities and resource revisions. Financial
values retain exact decimal text, including trailing scale; absent values are
explicitly unknown rather than zero. Numerically equal reports with different
scale remain compatible, while the accepted selection keeps its retained text.
Civil dates stay dates and timestamps retain their reported fractional precision.
Compact explanations are separate reads tied to the resource revision they
explain. Raw reports, private evidence traversal, credentials and operational
configuration belong outside this consumer boundary.

A warehouse journal retains the final canonical snapshot of each affected resource
in the same transaction as its accepted state. Intermediate changes within that
transaction are private; successive committed transitions remain independently
readable. Relationship resolution, reference changes, cancellation/replacement and
operator choices advance their owning resources, including choices that change
authority while preserving a value. Unchanged replay and rollbacks publish nothing.
The warehouse retains journal history without automatic pruning within its publishing
generation. Wire and admin models stay outside persistence.

Cursors are opaque, versioned and bound to the warehouse, publishing generation and
read purpose/position. Bootstrap captures a checkpoint **before** scanning current
resources in stable identity order, then replays changes through one fixed
high-water mark. Pagination does not hold a server snapshot. Consumers upsert by
resource revision so older replay cannot overwrite a newer scan result, while
retaining every delivered transition. They save their mirror, cursor and any
incomplete pass's high-water mark atomically. Each catch-up pass is finite even
while new writes continue.

Daemon restarts and access/operational-store recovery retain cursors when the
warehouse remains intact. Starting a new projection rebuild rotates the publishing
generation atomically with its durable job, invalidating older cursors. Retrying or
resuming that job keeps its generation. A newer rebuild supersedes older jobs; only
the latest failed job can be retried. Reads remain unavailable during rebuilding
or a failed rebuild awaiting retry; successful completion permits a new bootstrap.
Warehouse recreation likewise requires a new baseline. Every-transition delivery
applies within a generation. Invalidated cursors require explicit rebootstrap;
malformed cursors, missing resources and rebuilding have distinct errors. The
server never substitutes a fresh cursor for a rejected one.

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

One daemon owns a three-store profile: a financial warehouse, active connections
and credentials, and reconstructable operational execution state. Each store has an independent
file setting, with XDG defaults for financial data, active configuration and
operational state. The warehouse
retains historical source descriptors independently of current access, and owns
evidence, financial identity, decisions and durable recovery inputs. Acknowledged
checkpoints, coverage, completed-work receipts and pacing/retry restrictions survive
loss of execution state. Reference cache entries are disposable; their supporting
evidence remains retained.

One attached SQLite writer uses DELETE rollback journals and EXTRA synchronization
for cross-store atomicity. Financial references remain within the warehouse;
cross-store relationships are checked within the shared transaction. Every store
has an explicit development format. The warehouse owns its identity; the operational
store records which warehouse it belongs to. Credentials remain independent of
warehouse identity. Ownership,
format and association checks precede writable recovery. Unsupported formats fail
closed; startup never converts or deletes them. Recreation is an explicit operator
action, and master keys remain operator-provisioned through setup. Offline CLI
reset exposes credentials and warehouse choices, previews the selected files and
requires explicit confirmation for deletion. Warehouse reset also clears its
operational state; credentials reset preserves financial data. It checks filesystem
ownership without opening SQLite or reading keys.

A supported warehouse remains useful for inspecting retained history without source
credentials. A supported operational store associated with a different warehouse
is cleared and rebound to the selected warehouse without changing financial identity.
Missing companions are recreated and execution obligations reconstructed
from durable inputs; this pauses acquisition and reference access until authenticated
operator review, any required access repair and explicit resume. An intact restart
retains its existing authority and scheduling. Recreating companions cannot shorten
retained retry restrictions or discard acknowledged progress. A fresh warehouse
can reuse retained connection settings and encrypted credentials, starting paused
until explicit resume and bounded discovery. Account confirmation is required
before financial synchronization.

Operational diagnostics use structured, component-scoped events and configured log
levels across acquisition, normalization, financial processing, reference work,
operator actions, recovery and transport/process lifetime. Opaque operation IDs
correlate stages; durations, classified outcomes and retained retry timing explain
progress. Info summarizes completed work and required operator action; debug adds
stage, validation, pacing and cache detail. Idle polling is silent, repeated failures
are bounded with suppressed counts, and long work reports progress every 30 seconds.
Success follows the owning commit; the stopped event follows joined work and cleanup.

Startup always reports whether admin is enabled and, when listening, the actual
bind address and port, including at warn/error log levels. Diagnostics exclude
credential/configuration payloads, financial values, source identifiers, operator
rationale, raw request data and raw upstream errors at every level. Admin failures
use fixed classifications and registered route patterns; ordinary reads and SSE
traffic stay out of info logs.

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
Conflicting terms retain the accepted values and expose review status. Option
contracts retain their own identity, a separate underlier link and the
reported terms used by each historical execution. Listings remain absent where
evidence supplies no listing venue. Explicit lifecycle relationships retain
unresolved qualified endpoints; they resolve
when the endpoint is accepted, without timing or amount matching. Reported stock
bookings retain identities separate from option outcomes. Source summaries and
unsupported activity stay
in evidence without becoming accepted executions.

The reference service maps identifiers using only their type and value; source
currency and venue do not constrain identity lookup. It batches jobs under a shared
durable budget. Cache reuse and retained selection evidence have separate
lifetimes: successful lookups
are reusable for seven days and successful no-match results for six hours, while
the evidence supporting accepted selections remains retained. Transient failures
are not negative results. Configured key failures require repair or explicit
selection of anonymous access. Reference selection distinguishes equity share
classes from option contracts; an
underlier result never identifies its option. An unambiguous reference can enrich
an existing
identity; unifying distinct existing roots requires review rather than a silent
merge. Financial processing remains useful with provisional reference identity.

Administrative commands use authenticated HTTP forms. Committed work triggers
SSE refresh hints; fallback polling reads the same status regions. Editable forms
remain outside those regions, and refreshes preserve focused controls.

Activity identity allocations and baseline selections remain retained independently
of accepted-value projection updates. The projector applies the same typed
compatibility and enrichment rules to retained observations during processing and
recovery. Source corrections are evaluated across connected relationship groups;
ambiguous endpoints, competing replacements and cycles prevent partial application.
Accepted value, link and conflict-status changes advance entity revisions; an
unchanged replay does not. Cancellation and supersession retain original activity
and its evidence, while replacement activity retains its own identity.

Authenticated correction commands retain session-derived attribution, timestamp,
scope and optional rationale. A submission token makes retries repeat-safe and an
expected entity revision prevents stale writes. Decision history appends supersession
and release rather than mutating previous decisions. The projector reapplies active
decisions over automatic selections; automatic reference selections are retained
separately so refresh and release preserve the correct authority. Reference candidate
selection is restricted to the affected instrument's identifier-mapping job within
a retained batch, and link decisions validate the target family and listing root.

Processing retry reuses retained reports and successful normalization records.
Projection rebuild persists a job and its entity/explicit-relationship groups,
committing one bounded group at a time from current retained inputs and decisions.
An interruption resumes after committed progress; failure rolls back the current
group and leaves other views at their last commit. A related group that exceeds
the bounded work limit is rejected visibly before rebuilding begins. Source and
reference access, credentials, identity allocations, reference evidence/cache,
budgets and retry eligibility are independent of rebuild. Refresh hints follow
committed progress, and recovery controls stay outside refreshed status regions.

Shutdown signals event streams to finish before draining HTTP servers. Ordinary in-flight requests retain their bounded grace period and are joined before storage closes.

Restoring lost access settings invalidates opaque source continuations while preserving retained pacing deadlines. Known unchanged settings preserve their continuation. Automatic underlier enrichment continues beneath an operator link override, so releasing the decision restores the source-established link.
