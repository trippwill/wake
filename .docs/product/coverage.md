# Current coverage

Wake serves retained IBKR Flex and SnapTrade Personal financial history, with one
confirmed account per source. The current pre-release build runs local financial
work only. The provider descriptions below cover the retained evidence and tested
provider capabilities. See the [README](../../README.md) for usage and the
[domain vocabulary](domain.md) for financial policy.

## Network access

Wake runs as a single-user Linux daemon with separate IPv4 loopback listeners for
browser administration and the financial API. Each listener has a configurable
port. Operators provide any remote access and transport protection.

Browser administration uses a password, signed sessions, and CSRF protection.
The host operator can set or reset the password through the CLI. Each financial
client uses a separately revocable
bearer token. Tokens grant the instance's published financial reads, including
events and positions, without administrative authority or per-account scopes.
They have no automatic expiry.

Local pages, corrections, API reads, and retained-evidence processing run with
the financial database and local authority. Live provider and reference work is
disabled while synchronization recovery is being completed. The daemon does not
need a credential key or usable synchronization or access databases to serve
retained financial history.

## Account source authority

Each retained connection can have multiple discovery candidates and one confirmed
account. Confirmation retains the chosen qualified source-account reference and
the details used to establish the binding. The
[domain vocabulary](../product/domain.md#account-source-authority) defines account
identity, source authority, and financial meaning.

Each account has one designated authoritative connection and provider, bound to its
qualified source-account reference. This does not designate IBKR as the instrument
reference master or remove attributable operator corrections. OpenFIGI remains
the primary instrument identity and naming reference.

## Included coverage

| Area | Coverage |
| --- | --- |
| Acquisition | IBKR Flex Confirmation and optional Activity queries sharing the same encrypted token. SnapTrade Personal cached history and Fidelity US positions for the confirmed account. Both retain original reports and recover after restart. |
| Instruments | Equity/share-class roots with linked listings; actual US equity/ETF option contracts and underliers; nominal US Treasury and conventional gilt issues. Preserve applicable reported identifiers and terms without making them IBKR-dependent. |
| Trades | Flex executions and SnapTrade purchases/sales at their reported grain. Exact quantities, prices, dates and reported opening/closing qualification. Flex includes nested reported charge totals and components. |
| Booked activity | Reported option exercise, assignment and expiration, and their reported stock bookings. An `EXECUTION` detail level does not make every `BookTrade` an exchange execution. |
| Security transfers | Reported inbound/outbound security movements, separate transfer valuations and aggregate reported costs, counterparty details and exact quantities. Transfer lots remain evidence. |
| Currency conversion | FX executions reported in Confirmations, with explicit currency/amount roles and unknowns. No FX lot or P&L reconstruction. |
| Non-trade cash | Detailed funding, cash transfers, dividends, payments in lieu, broker/bond interest, withholding, fees, sales taxes and explicit adjustments. Separate signed postings, shared fee/tax charge totals, optional instruments, source action groups and explicit tax relationships. |
| Reported positions | Complete current account sets from IBKR Activity Open Positions summary detail or SnapTrade Fidelity US holdings, with exact signed quantities and reported contract terms. Both sources can include reported unit cost basis. Explicit empty/unavailable states; no holdings or lot reconstruction. |
| Realized P&L | Activity execution-level reported gains/losses and allocated costs linked to existing activity, with exact currencies, separate reporting dates, unknowns and source disagreement. No result calculation or lot reconstruction. |
| Relationships | Preserve reported order membership and explicit lifecycle/correction relationships without merging fills or legs or inventing missing membership. |
| Corrections | Repeated observations, compatible enrichment, explicit source cancellation/replacement, visible ambiguity and attributable operator corrections to existing activity, fields, names and links, with retained decisions. |
| Reference data | Allocation-aware OpenFIGI lookup, bounded composite-parent completion, local cache, retained resolution evidence and both anonymous and operator-key pacing profiles. Matching verified identities combine with retained aliases; conflicting choices require review. Refresh failures preserve established identity. |
| Administration | Authenticated admin workflows: connection/account setup, sync progress and failures, activity/instrument inspection, a separate latest reported positions workspace, compact conflicts, optional linked evidence/rules and correction of existing activity. |
| Continuity | Durable authority and decisions, repeat-safe replay, joined shutdown, protected credentials and reproducible derived views. |

Overview shows the last successful report retrieval and next automatic event for
each scheduled source stream. Repeated reports count as successful retrievals.
The next event time indicates when Wake can attempt the work, subject to source
access and pacing.

Some reported bookings lack execution IDs; acceptance cannot universally require
one. Keep exact reported values, currencies, units and time precision. Source money
is not recomputed to make an equation agree. Reported settlement dates are not proof
of completed cash settlement. Preserve reported option multipliers and terms;
unknown deliverables remain unknown, rather than an assumed 100 shares. Historical
activity retains the terms used to interpret it. A general automatic contract-
adjustment or financial reconstruction engine is not required.
Instrument resources expose identity; option/debt terms remain on dated activity
and observations. Resource lookups redirect retired IDs to their current identity;
old-ID explanations and new correction submissions are stale. Complete snapshots
replace a consumer's resource graph, including combined identities and listings.

Debt activity retains nominal face quantity and exact reported price without a
quotation-basis claim. Principal, proceeds, trade net cash, accrued interest and
charges remain separate; no combined settlement total is inferred.

Source-established relationships are useful when present. A missing relationship
does not authorize an inferred stock delivery, cash payment or strategy. Family
and market examples are representative, not rejection lists
for otherwise interpretable records.

The [SnapTrade guide](../providers/snaptrade.md) describes its broker coverage, cached
acquisition and interpretation.

The IBKR provider reads activity terms from Confirmation reports and per-activity
realized results, transfers and cash postings from an optional Activity query for
the confirmed account. These
are provider-owned report interpretations; they contribute to one accepted Activity
model through the same typed activity-observation contract. Financial assertions
may be partial; they do not require a complete execution row or a second P&L pipeline.
Unmatched and incomplete observations remain retained and reviewable; later related
evidence can establish their links or supply required terms. An ambiguous established
link remains visible with conflict.
Disabling the Activity query stops acquisition while preserving retained results.

Activity trade-row P&L remains distinct from FX holding gains and from period totals.
A result is not recomputed from proceeds, charges or an operator's corrected price.
Source restatements without established revision semantics retain the baseline and
disagreement. Competing enrichments of an originally unknown field remain unknown;
operators can select reported fields without replacing omitted activity terms. Additional
lot sections remain original evidence without financial contribution.

Transfer lot rows and cash summaries do not create additional activity. Cash postings
retain signed values, including refunds and explicit zeros. Shared action references
group postings without merging their identities; tax references resolve when related
postings arrive. Explicit trade-linked bond-interest rows support the trade's accrued
interest rather than creating another non-trade payment. Missing targets stay pending.
There is no net-income, cash-balance or lot reconstruction.

Attached trade and FX charges preserve commission totals, nested regulatory subtotals
and components, with financial categories and source roles. Missing totals remain unknown;
no component sum, currency conversion or all-in charge is inferred. Standalone fee/tax
postings expose their existing cash amounts through the same charge view. IBKR's detailed
cash postings supply totals without a fee-component breakdown; separate linked tax postings
remain separate. The domain accepts explicitly reported standalone breakdowns.

Retained acquired reports are automatically queued when their provider interpretation
version changes. Bounded processing preserves identities and decisions, commits each
report atomically and exposes unfinished processing and retries independently of
acquisition success. No fresh report or warehouse reset is required.

## Deferred coverage

- Creation of new financial activity through manual-entry workflows.
- Activity contributions to trade terms and lifecycle outcomes, period P&L summaries,
  dedicated Forex P/L Details and summary/component reconciliation.
- Financial contributions from Confirmation order or symbol summaries. Preserve
  them in the original report; execution/booked rows supply financial activity.
- Lot identities and allocations, wash-sale calculations and
  dedicated corporate-action processing, including Treasury-bill maturity events.
- Balances, NAV, unrealized or calculated P&L, historical position queries, position corrections,
  and reconstruction of
  holdings, cash, lots or missing lifecycle outcomes.
- More than one confirmed account per connection, additional acquisition providers,
  file imports, automatic source switching and peer-source reconciliation.

Exercise, explicit cancellation/replacement, adjusted-option and representative gilt checks
use representative evidence or clearly identified synthetic cases. Synthetic
repeat-acquisition proofs cover receipts without redundant bodies or normalization,
pre-correction claim comparison, duplicate-row multiplicity and retention of disputed
variants. Subject support retains the first observation, compatible new information and
distinct conflicts independently for activities, instruments and nested underliers. Repeated
claims, identifier reordering and compatible recombinations add no supporting links or publication
changes. Different activities with unchanged
instrument facts add no instrument support; their own contributions and publications remain
independent. Position bindings likewise survive discarded instrument support. Holding
quantities and dates can change independently of instrument evidence, and active position
interpretations remain usable when their first instrument-supporting appearance belongs to an
earlier interpretation. Report pages preserve processed normalized appearances, unselected and
pending observations, duplicate locations and interpretation versions. Related activity
context remains available in report and instrument support views for discarded activity support
without adding persistent links.
Anonymous-underlier enrichment and disputes propagate through instrument relationships
independently of parent support and operator selections. Interpretation replacement changes
current eligibility without deleting contribution history or multiplying repeated support.
Instrument-family disputes preserve evidence without attaching option fields to another
family. Self-referential reported underliers preserve conflicts without blocking report
processing or selecting a cyclic relationship. References resolving to inadmissible underlier
families remain source conflicts and cannot establish automatic underlier choices. Raw report
retention alone does not establish
financial coverage. Private reports and
credentials are not repository fixtures.

## Canonical publishing

The versioned read-only API publishes complete accepted accounts, instruments,
listings and activity snapshots, individual resources and revision-bound explanations.
Activity bodies distinguish trades, option outcomes, currency conversions, security transfers and
cash postings. Each body carries its applicable fields. Dated instrument terms distinguish option,
debt, equity, fund and unclassified instruments. Null denotes a field without an accepted value.
Explanations distinguish an omitted source assertion, an explicit unavailable assertion,
unresolved selection and established reporting expectations. Zero remains a reported value.

Each snapshot has warehouse identity and an opaque publication version. Conditional
GET avoids unchanged downloads. Accepted source/operator corrections, conflicts and
relevant reference/link changes advance publication; repeated known claims do not.
SSE announces current publication on connection and after accepted changes. Bursts
can coalesce; reconnects deliver current state without replay. Clients synchronize
through complete snapshots and retain the prior mirror on failure. Notifications
cover accepted financial state, independently of admin workflow status.
Restarts and access-store recovery retain publication with an intact warehouse.
Warehouse replacement establishes a clearly reported new baseline.

Reported positions have a separate account read, opaque version and ETag. They are excluded
from complete financial snapshots. Each available set has an as-of date and optional timestamp.
For Fidelity through SnapTrade, the timestamp records the source fetch. Instrument IDs
resolve through existing lookups. Newer missing,
unsupported or unusable coverage withholds rows. Same-effective-time incompatible claims
remain unavailable until an unambiguous later state or established source revision supplies
a usable set. A known empty set remains available. The existing SSE stream announces
positions versions independently, including availability changes, on connection/reconnect
and after committed changes. Shared identity combinations update affected position IDs;
quantity, cost or date changes alone do not advance the financial snapshot.

The [wire contract](../../api/openapi/v1.yaml) owns exact semantics. The independent
[consumer acceptance procedure](../engineering/development.md#api-consumer-acceptance)
uses a separate process and authenticated TCP requests without Wake store access, internal
application calls or upstream credentials. Its own transactional mirror retains the
last complete validated snapshot on failed transfers or writes. Checks cover exact
values, conditional reads, SSE follow/reconnect, corrections/conflict raise/clear,
restart, access recovery and warehouse replacement. In-process tests alone do not
establish this boundary.

## Compatibility

The executable targets Linux. Starting with v0.1.0, patch releases within a given
`0.x` minor preserve the published HTTP/JSON contract, public Go APIs, CLI machine
output and exit behavior, and supported configuration keys and defaults. A change
that existing strict consumers reject is breaking even when it only adds a field or
enum value. New minor releases may introduce documented breaking changes. A breaking
HTTP change requires a new URL contract version. Concurrent support for older URL
versions depends on the release.

The executable release, OpenAPI specification revision and HTTP URL contract version
have separate meanings. Publication versions are opaque equality tokens scoped to
their published view. Provider interpretation versions identify how Wake reads
retained source evidence. Private store formats govern whether the executable can
open its databases.

Patch releases preserve usability of supported stores. An incompatible minor release
documents its supported upgrade or explicit recreation procedure. This pre-release
storage format assigns separate files to financial data (`storage.warehouse_path`),
execution state (`storage.sync_path`), and encrypted upstream access
(`storage.connections_path`). Use a compatible build for earlier formats or preserve
the existing files and sidecars before explicitly recreating the warehouse. Wake
refuses unsupported formats without rewriting them.

### Backup and recovery

Use a warehouse snapshot to capture committed financial history, evidence,
decisions, and publication while Wake runs. To restore, stop Wake and choose a
vacant warehouse and sidecar path. The restored warehouse retains financial IDs
and gets a fresh publication epoch, so consumers retrieve fresh snapshots and
positions baselines. A new installation supplies its own local authority.
Existing sessions, client grants, and optional databases belong to the installation
and remain outside the archive.

Operational backup and restore with credentials are unavailable in this build.
Keep original stores and credential keys for access through a compatible build.
See [architecture](../engineering/architecture.md#persistence-and-work-boundaries)
for storage boundaries.

### Forward upgrades

Stop Wake and run `wake storage upgrade` with the selected configuration to check
the financial format. The check leaves the stores untouched. Pre-release formats
have no compatibility migrations. The financial store keeps the durable rebuild
cursor and serves progress in Overview. A partially rebuilt view remains gated
until its rebuild finishes.
