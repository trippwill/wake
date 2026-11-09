# Current coverage

Wake supports IBKR Flex and SnapTrade Personal in the same warehouse, with one
confirmed account per connection. Flex supplies Confirmation reports and optional
Activity reports for security transfers, non-trade cash postings, reported realized
P&L and reported positions. SnapTrade supplies cached purchases and sales for Fidelity
US and IBKR.
This document owns
coverage and limitations; the [README](../../README.md) provides usage help and
the [domain vocabulary](domain.md) owns financial policy.

## Account setup and authority

The workflow is:

1. The operator creates a connection with the selected provider’s settings and credentials.
2. Wake discovers the account information available through that connection.
3. The operator selects one discovered account and confirms or edits its details.
4. Wake creates the financial account and binds its reported account reference to
   the connection and provider as its authoritative financial source.
5. Automatic synchronization and financial processing start for that account.

Discovery may return several candidates. Each connection can activate one. Confirmation
creates the account and starts ongoing synchronization. Configure separate real accounts
when using both providers.
The [domain vocabulary](../product/domain.md#account-source-authority) defines
account identity, source authority and financial meaning.

Each account has one designated authoritative connection and provider, bound to its
qualified source-account reference. This does not designate IBKR as the instrument
reference master or remove attributable operator corrections. OpenFIGI remains
the primary instrument identity and naming reference.

## Included coverage

| Area | Coverage |
| --- | --- |
| Acquisition | IBKR Flex Confirmation and optional Activity queries sharing the same encrypted token. SnapTrade Personal cached history for the confirmed account. Both retain original reports and recover after restart. |
| Instruments | Equity/share-class roots with linked listings; actual US equity/ETF option contracts and underliers; nominal US Treasury and conventional gilt issues. Preserve applicable reported identifiers and terms without making them IBKR-dependent. |
| Trades | Flex executions and SnapTrade purchases/sales at their reported grain. Exact quantities, prices, dates and reported opening/closing qualification. Flex includes nested reported charge totals and components. |
| Booked activity | Reported option exercise, assignment and expiration, and their reported stock bookings. An `EXECUTION` detail level does not make every `BookTrade` an exchange execution. |
| Security transfers | Reported inbound/outbound security movements, separate transfer valuations and aggregate reported costs, counterparty details and exact quantities. Transfer lots remain evidence. |
| Currency conversion | FX executions reported in Confirmations, with explicit currency/amount roles and unknowns. No FX lot or P&L reconstruction. |
| Non-trade cash | Detailed funding, cash transfers, dividends, payments in lieu, broker/bond interest, withholding, fees, sales taxes and explicit adjustments. Separate signed postings, shared fee/tax charge totals, optional instruments, source action groups and explicit tax relationships. |
| Reported positions | Complete current account sets from Activity Open Positions summary detail, with exact signed quantities, dated contract terms and optional aggregate reported cost basis. Explicit empty/unavailable states; no holdings or lot reconstruction. |
| Realized P&L | Activity execution-level reported gains/losses and allocated costs linked to existing activity, with exact currencies, separate reporting dates, unknowns and source disagreement. No result calculation or lot reconstruction. |
| Relationships | Preserve reported order membership and explicit lifecycle/correction relationships without merging fills or legs or inventing missing membership. |
| Corrections | Repeated observations, compatible enrichment, explicit source cancellation/replacement, visible ambiguity and attributable operator corrections to existing activity, fields, names and links, with retained decisions. |
| Reference data | Allocation-aware OpenFIGI lookup, bounded composite-parent completion, local cache, retained resolution evidence and both anonymous and operator-key pacing profiles. Matching verified identities combine with retained aliases; conflicting choices require review. Refresh failures preserve established identity. |
| Administration | Authenticated admin workflows: connection/account setup, sync progress and failures, activity/instrument inspection, a separate latest reported positions workspace, compact conflicts, optional linked evidence/rules and correction of existing activity. |
| Continuity | Durable authority and decisions, repeat-safe replay, joined shutdown, protected credentials and reproducible derived views. |

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

This pre-release warehouse format supports one account per connection and requires
explicit warehouse recreation on upgrade.
Older warehouses fail closed and remain untouched; encrypted access seeds retain
their independent format. Reacquire and confirm source history in the new warehouse.
Recreating a warehouse discards warehouse-only decisions and evidence; preserve
any needed old warehouse before explicitly resetting. No automatic migration or
historical updater is supplied by this change.

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

The versioned read-only socket publishes complete accepted accounts, instruments,
listings and activity snapshots, individual resources and revision-bound explanations.
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
from complete financial snapshots. Each available set has one effective date and optional
reported timestamp; instrument IDs resolve through existing lookups. Newer missing,
unsupported or unusable coverage withholds rows. Same-effective-time incompatible claims
remain unavailable until an unambiguous later state or established source revision supplies
a usable set. A known empty set remains available. The existing SSE stream announces
positions versions independently, including availability changes, on connection/reconnect
and after committed changes. Shared identity combinations update affected position IDs;
quantity, cost or date changes alone do not advance the financial snapshot.

The [wire contract](../../api/openapi/v1.yaml) owns exact semantics. The independent
[consumer acceptance procedure](../engineering/development.md#socket-consumer-acceptance)
uses a separate process and actual private socket without Wake store access, internal
application calls or upstream credentials. Its own transactional mirror retains the
last complete validated snapshot on failed transfers or writes. Checks cover exact
values, conditional reads, SSE follow/reconnect, corrections/conflict raise/clear,
restart, access recovery and warehouse replacement. In-process tests alone do not
establish this boundary.

## Compatibility

The executable targets Linux. Storage uses explicit pre-release development formats with no
automatic compatibility migration. Unsupported stores fail closed with recreate guidance and
preserve the original files. Back up stores and sidecars before explicitly moving or resetting them.
The warehouse retains financial evidence, decisions and operational progress in one database. The
encrypted access store has an independent path. Replacing that store preserves the warehouse and
pauses upstream work until access is repaired and explicitly resumed; recreating the warehouse
preserves reusable encrypted access but establishes new financial identities. See
[architecture](../engineering/architecture.md) for trust and recovery boundaries and
[development](../engineering/development.md) for verification and reset procedures. No version tag
is implied by this coverage.

Admin activity, instrument, positions, review and retained-observation lists use server filtering
and stable ordering, with 50 rows per page by default. JavaScript is required for
administration. Tabulator renders activity, instrument, positions and observation tables from
authenticated JSON; source/status regions use focused updates, and accepted detail
pages offer an explicit refresh action. Retained observations preserve original
source bindings, underliers and appearance multiplicity after operator relinks.
Numeric ordering preserves exact decimal values,
keeps currency/unit roles distinct and places unknown values last. Status and setup
prerequisites use focused metadata reads; source detail loads one connection and a
bounded account-candidate page. Result counts and page links preserve intentional
URL filters. These bounds limit response memory; filtered counts and sorting still
require SQLite to inspect the matching history.

The Positions workspace shows account availability and reported effective time alongside
exact signed quantities, long/short meaning, applicable reported contract terms and optional
aggregate reported cost basis. Missing cost differs from explicit zero. Empty reported sets,
unavailable sets and filters with no matches have separate explanations. Instrument and source
links open existing inspection pages. Updates are offered explicitly and apply metadata and
rows together; unavailable results contain no earlier fallback positions. Display-label changes
are detected independently of the positions version. The workspace adds no position-correction
workflow, valuation, aggregate portfolio totals or lot reconstruction.

Processing, correction preflight, saved decisions and releases share deterministic
financial calculations. Synthetic proofs cover exact decimal and timestamp
precision, source disagreement, read-only checks, decision release and transaction
rollback. The retained-history contribution requirement remains in the
[development guide](../engineering/development.md). Provider interpretation versions
automatically queue retained reports; changed core calculation rules still require
an explicit historical update procedure.

Correction reconciliation connects ambiguous endpoints through a shared anchor and visits each
replacement path once. Synthetic long-chain, cycle and wide-ambiguity proofs preserve whole-group
review behavior. Large retained groups still require memory and calculation inside the writer
transaction; these checks do not establish unlimited history capacity.
