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
Acquisition receipts, distinct report bodies and standardized source claims have
separate identities. Each receipt preserves actual coverage and byte digest even
when a provider's explicit versioned policy reuses a body for a report-window-only
change. Claim comparison uses the retained pre-correction meaning, excluding only
structural location and retaining appearance multiplicity. Known claims attach
provenance before reconciliation and skip financial recalculation and publication.
Changed non-equivalent bytes remain evidence even when all their claims are known.
Named/versioned rules and operator decisions explain conclusions through links.
Activity lifecycle, review reasons and selection authority are independent typed
facts. Persist and publish them without encoding state in combined display phrases.
Source failures retain machine classifications separately from diagnostic wording;
scheduling depends on the classifications.
Shared instrument enrichment compares all retained claims for the original root
against one pre-write baseline and persists that root once per processing batch.
Competing enrichments retain that baseline for review; execution IDs do not select
financial terms. Indexed source-occurrence bindings include nested underliers,
which are reconciled even when they have no activity of their own. Underlier
identity is calculated independently of disagreements in parent contract terms.
Activity correction preflight/save does not re-enrich shared source roots.
Admission compares each genuinely new claim against its retained source baseline
and every prior source assertion, excluding operator choices. A projected unknown
caused by existing disagreement cannot corroborate a later conflicting value or
admit its relationships. Shared compatibility rules govern this comparison and the
automatic financial calculation.

Financial calculation consumes one consistent retained input set: source claims,
identity bindings, selected references, active decisions and an explicit implemented
rule. It returns typed accepted values, independent conflict/authority facts and
explanations without SQL writes or upstream calls. Processing, correction preflight,
saving and release use that same calculation. Preflight substitutes a prospective
decision in memory inside a read transaction; saving persists the calculated result,
decision and publication atomically. Only groups reached by new claims or changed
source-correction state require recalculation. Retained appearances remain provenance;
financial selection reads distinct source claims with their multiplicity.

Small typed field policies own applicable activity/instrument families, scalar value
types, optionality and bounds. Domain constructors own parsing/application invariants;
editors consume the policies and own wording and controls. The published accepted
read model is encoded directly; OpenAPI conformance tests protect its wire contract.

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
configuration belong outside this consumer boundary. The response contract requires
exactly one financial body matching the resource kind.

A warehouse journal retains the final canonical snapshot of each affected resource
in the same transaction as its accepted state. Intermediate changes within that
transaction are private; successive committed transitions remain independently
readable. Relationship resolution, reference changes, cancellation/replacement and
operator choices advance their owning resources, including choices that change
authority while preserving a value. Unchanged replay and rollbacks publish nothing.
The warehouse retains journal history without automatic pruning within its publishing
generation. A publishing failure rolls back its transaction before recording
the failed attempt separately, leaving neither partial normalization nor journal
entries. Wire and admin models stay outside persistence.

Cursors are opaque, versioned and bound to the warehouse, publishing generation and
read purpose/position. Bootstrap captures a checkpoint **before** scanning current
resources in stable identity order, then replays changes through one fixed
high-water mark. Pagination does not hold a server snapshot. Consumers upsert by
resource revision so older replay cannot overwrite a newer scan result, while
retaining every delivered transition. They save their mirror, cursor and any
incomplete pass's high-water mark atomically. Each catch-up pass is finite even
while new writes continue. The acceptance consumer saves a new baseline and every
changed mirror/checkpoint atomically, skips disk writes for unchanged idle polls,
and treats an operator interrupt during follow-mode requests as a clean stop.

Daemon restarts and access-store recovery retain cursors when the
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

In admin proxy mode, the reverse proxy owns upstream user authentication and
supplies Wake's guard on every request. Wake checks the configured public host
before establishing a signed browser session during document navigation. It serves
that document with the new session; static assets do not establish sessions, and
background requests cannot rotate its CSRF token underneath an existing form. The
session does not replace the proxy guard; commands additionally require the public origin
and the session's CSRF token. HTTPS at the public origin protects the secure cookie.
The proxy must replace client-supplied guard headers, and its upstream connection
must remain private. This browser gate is separate from financial socket access.

Shutdown cancels and joins work before releasing resources. Recovery preserves
identity, evidence and decisions; derived views are reproducible. Unsupported
pre-release stores fail closed with explicit recreation guidance. The publishing
schema adds reverse endpoint indexes for relationship/correction invalidation and
per-activity explanation reads; its storage version advances so profiles from
the preceding development schema cannot silently miss those indexes and triggers.
Privacy depends
on the user's host and storage protection as well as Wake's credential handling.

## Persistence and work boundaries

Warehouse and encrypted access formats are checked independently. An unsupported
warehouse still fails closed with recreation instructions; an unchanged encrypted
access seed remains reusable when only the warehouse format changes.

One daemon owns two SQLite files: a warehouse containing financial evidence and
operational state, and an independent access store containing current connection
settings and encrypted credentials. Their paths default to XDG data and config
locations. The warehouse owns evidence, source descriptors, financial identities,
decisions, checkpoints, coverage, processing receipts, pacing and execution state.
Reference cache entries remain disposable; their supporting evidence is retained.

One attached SQLite writer uses DELETE rollback journals and EXTRA synchronization
for cross-store atomicity. Warehouse references use ordinary foreign keys;
relationships to access configuration are checked in the shared transaction.
Each file has an explicit development format and independent identity. The access
store has no reverse binding to a warehouse. Ownership, format and association
checks precede writable recovery. Unsupported formats fail closed; startup never
converts or deletes them. Recreation is an explicit operator action, and master
keys remain operator-provisioned through setup.

Offline CLI reset previews warehouse or credential files and requires explicit
confirmation before deletion. Warehouse reset disposes its operational state in
the same file and preserves encrypted access; credentials reset preserves the
warehouse. Reset checks filesystem ownership without opening SQLite or reading keys.
A warehouse remains inspectable without source credentials. Missing or replaced
access starts upstream work paused until authenticated review, required repair
and explicit resume. Retained pacing restrictions and acknowledged progress remain
in the warehouse. An intact restart retains authority and scheduling.

A fresh warehouse can reuse retained settings and encrypted credentials. It starts
paused until explicit resume and bounded discovery; account confirmation is still
required before financial synchronization. Development tests can copy a closed
encrypted access seed into separate private profiles and use independent warehouse
and access files. Those profiles never share a writable store or modify the seed.
This keeps warehouse disposal independent of credential provisioning without a
separate operational database or its association/rebinding lifecycle.

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
financial authority. Acquisition, discovery, settings preparation and normalization
are independent capabilities; orchestration selects each artifact's registered
interpreter rather than a global source/version. Common forms use provider field
metadata. Connection display names belong to the host, while provider settings,
credentials, continuation documents and native diagnostic codes belong to the source.
Providers without a normalization capability retain acquired evidence without
entering interpretation retries. Providers translate protocol progress and retry
causes into shared typed outcomes;
the core never infers their meaning from a native code or reference kind.
Normalization-only providers can interpret explicitly supplied bounded evidence
without acquisition credentials or scheduled upstream work. Supply grants no
account authority. Normalization receives retained bytes and a sequential claim
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
authorized acquisition starts independently when that capability is implemented.
The daemon resumes eligible durable work, retains pacing and unavailable ranges, and joins acquisition and reference
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

Change-journal reads require a checkpoint or continuation cursor. Collection cursors remain optional for initial resource scans. Publishing resolves source-correction endpoints only for corrections related to the activity being published, within the same transaction.
