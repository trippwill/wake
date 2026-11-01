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

Launch configuration resolves once on the actual parsed CLI command. Strictly decode
and validate the selected TOML file before applying higher-priority values; explicit
flags cannot conceal an invalid file. TOML table and field names use the catalog's exact
spelling and case. Native CLI environment and map sources apply
`defaults < TOML < environment < explicit flags`, and discovery reports the authority
of each selected value. Build typed configuration directly from these flags; do not
reparse overrides through a second command or serialize intermediate TOML. Resolution
performs no filesystem writes. Cross-setting requirements are checked on the effective
configuration, allowing valid partial files to combine with environment or flags.
Setup edits configured intent separately from launch
overrides; directory preparation belongs to commands that need it.

## Responsibilities

| Boundary | Responsibility |
| --- | --- |
| Composition | Select and connect concrete implementations and compiled provider packages. |
| Host | Configuration, credentials, durable work, scheduling, progress, cancellation and shutdown. |
| Source providers | External protocols and source-specific interpretation into qualified financial claims. |
| Core reference service | OpenFIGI access, shared anonymous/keyed pacing, local cache and retained resolution evidence. |
| Financial application | Wake identities, links, accepted projections, uncertainty and retained operator decisions. |
| Persistence | Evidence, durable progress, atomic writes and derived accepted read views. |
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
saving and release use that same calculation. Field parsing and whole-value
constructor failures return the typed invalid-input outcome, not a service failure. Preflight substitutes a prospective
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
Detailed evidence is optional. Prerequisite and status reads use source/access and
work metadata without decoding financial history. Source list/detail, overview,
review and accepted activity/instrument lists have separate typed read models.
Lists filter and order in SQLite, default to 50 rows per page and retain stable
identity tie-breakers. Exact decimal ordering uses the registered numeric collation;
unknown values sort last independently of direction and currency/unit groups remain
explicit. Each paged read model is materialized in one consistent read transaction,
released before rendering or client I/O. Presentation owns label wording and may
supply structured label matches and shared activity text fragments. Search composes
complete labels from exact values and this presentation wording before pagination;
SQL does not infer state from those words.
Reference ordering includes selection authority before the stable identity tie.
Review rendering and search share category/reason definitions; status/count
constraints are passed as typed review matches before pagination.
Missing-value and applicability searches use structured matches supplied by
presentation. Activity-cell wording is also supplied by presentation for exact
substring matching across amount roles and currency pairs. This is search formatting;
storage never interprets these words as state or financial policy.
Recovery pages read bounded source-access metadata, expose continuation links and
check readiness across every retained connection in one consistent access snapshot
before offering resume. Resume uses the same complete metadata query; it does not
enumerate authority through mutable, paginated UI lists. Review
labels retain structured activity context, and filtered empty pages distinguish
an unmatched result from an empty review queue.
The socket exposes accepted financial views with
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

The warehouse stores the current accepted resource and its revision in the same
transaction as accepted state. Intermediate writes inside a transaction remain
private. Relationship resolution, reference changes, cancellation/replacement and
operator choices advance their owning resources, including choices that change
authority while preserving a value. A publication version advances once when a
transaction changes accepted resources or their explanations. Unchanged source
claims, acquisition receipts alone and rollbacks do not advance it. A publishing
failure rolls back accepted writes before recording the failed attempt separately.
The socket adapter owns its narrow financial-read interface. Wire and admin
adapters consume application read values without exposing database transactions.

`GET /v1/snapshot` returns warehouse identity, an opaque publication version and
the complete accepted resource set. One consistent read materializes the response
and releases its transaction before client I/O. Its ETag supports conditional GET;
an unchanged poll avoids loading resource bodies and returns no payload. Individual
resource reads remain available. Explanation requests name the mirrored resource
revision and fail visibly if it has changed.

Consumers validate a complete snapshot before atomically replacing their mirror
and version. They preserve the prior mirror on failed downloads, validation or
writes. The independent acceptance consumer uses its own private SQLite transaction
for this replacement and skips writes for unchanged conditional reads. It retains
exact scalar text and explicit unknowns, checks unique identities and resource
links, and validates bodies against the published contract. It bounds downloads
to 128 MiB and reports excess size as a failure without replacing its mirror.

Daemon restarts and access-store recovery preserve warehouse identity and accepted
publication state. A different warehouse installs a clearly reported new baseline.
Synchronization delivers current accepted state; it does not deliver every
intermediate committed transition that occurred while a client was disconnected.
Immutable evidence and operator decision history remain retained inside Wake.

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
identity. A compatible ticker-only underlier keeps its root when its first
qualified reference arrives. If that reference already has a root, the parent's
automatic link selects it only when their complete retained source identities agree, including new claims in the same
processing transaction,
while preserving both roots and their source bindings. Conflicting identifiers
retain the established link and require review. Underlier claims in one batch
share the reconciled target, regardless of reference/identifier arrival order.
Compatible earlier source inputs also contribute to that target; their original
bindings remain retained.
Operator link selections remain in effect until released. Unifying distinct
existing roots requires review rather than a silent merge. Financial processing remains useful with provisional reference identity.

Administrative commands use authenticated HTTP forms. Interactive activity,
instrument and observation tables require JavaScript and consume authenticated JSON.
Tabulator owns each grid and reloads through its API on SSE hints or polling; HTML
does not duplicate its rows. Queries filter, order and page the complete matching set,
with 50 rows per page by default and exact decimal ordering. URLs retain intentional
filter and page state; native column persistence retains layout preferences.
Source/status regions retain focused htmx updates. Accepted detail pages are explicit
reads with a refresh action, while retained observations update independently.
Editable forms preserve drafts, selection and focus while work runs.

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
Source and reference access, identity allocations, reference evidence/cache,
budgets and retry eligibility retain their own lifetimes. Refresh hints follow
committed work, and access recovery controls stay outside refreshed status regions.
Historical calculation changes follow the explicit retained-data contribution
requirement in [development](development.md#changes-affecting-retained-data).

Shutdown signals event streams to finish before draining HTTP servers. Ordinary in-flight requests retain their bounded grace period and are joined before storage closes.

Restoring lost access settings invalidates opaque source continuations while preserving retained pacing deadlines. Known unchanged settings preserve their continuation. Automatic underlier enrichment continues beneath an operator link override, so releasing the decision restores the source-established link.

Publishing resolves source-correction endpoints only for corrections related to the activity being published, within the same transaction.
