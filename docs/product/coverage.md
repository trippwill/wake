# Current coverage

Wake supports one IBKR account with automatic Confirmation acquisition, financial
review and attributable corrections to existing activity. This document owns
coverage and limitations; the [README](../../README.md) provides usage help and
the [domain vocabulary](domain.md) owns financial policy.

## Account setup and authority

The workflow is:

1. The operator creates an IBKR connection with its access and saved Confirmation
   query configuration.
2. Wake discovers the account information available through that connection.
3. The operator selects one discovered account and confirms or edits its details.
4. Wake creates the financial account and binds its reported account reference to
   the connection, provider and Confirmation stream as its authoritative source.
5. Automatic synchronization and financial processing start for that account.

Discovery may return several candidates; v0.1.0 activates one account. Discovery
does not create financial accounts or start ongoing history synchronization.
The [domain vocabulary](../product/domain.md#account-source-authority) defines
account identity, source authority and financial meaning.

Each account has one designated authoritative `(connection, provider, stream)`
for its financial activity. This does not designate IBKR as the instrument
reference master or remove attributable operator corrections. OpenFIGI remains
the primary instrument identity and naming reference.

## Included coverage

| Area | Coverage |
| --- | --- |
| Acquisition | One IBKR account, one authoritative Confirmation stream, automatic acquisition, retained original reports, overlapping windows, restart and catch-up. |
| Instruments | Equity/share-class roots with linked listings; actual US equity/ETF option contracts and underliers; nominal US Treasury and conventional gilt issues. Preserve applicable reported identifiers and terms without making them IBKR-dependent. |
| Trades | Execution-level purchases/sales, distinct fills and option legs, reported opening/closing qualification, exact quantities, prices, monetary roles, charges and dates. |
| Booked activity | Reported option exercise, assignment and expiration, and their reported stock bookings. An `EXECUTION` detail level does not make every `BookTrade` an exchange execution. |
| Currency conversion | FX executions reported in Confirmations, with explicit currency/amount roles and unknowns. No FX lot or P&L reconstruction. |
| Relationships | Preserve reported order membership and explicit lifecycle/correction relationships without merging fills or legs or inventing missing membership. |
| Corrections | Repeated observations, compatible enrichment, explicit source cancellation/replacement, visible ambiguity and attributable operator corrections to existing activity, fields, names and links, with retained decisions. |
| Reference data | Core OpenFIGI lookup, local cache, retained resolution evidence and both anonymous and operator-key pacing profiles. Missing enrichment remains visible. |
| Administration | Authenticated admin workflows: connection/account setup, sync progress and failures, activity/instrument inspection, compact conflicts, optional linked evidence/rules and correction of existing activity. |
| Continuity | Durable authority and decisions, repeat-safe replay, joined shutdown, protected credentials and reproducible derived views. |

Some reported bookings lack execution IDs; acceptance cannot universally require
one. Keep exact reported values, currencies, units and time precision. Source money
is not recomputed to make an equation agree. Reported settlement dates are not proof
of completed cash settlement. Preserve reported option multipliers and terms;
unknown deliverables remain unknown, rather than an assumed 100 shares. Historical
activity retains the terms used to interpret it. A general automatic contract-
adjustment or financial reconstruction engine is not required.

Source-established relationships are useful when present. A missing relationship
does not authorize an inferred stock delivery, cash payment or strategy. Family
and market examples are representative, not rejection lists
for otherwise interpretable records.

## Deferred coverage

- Creation of new financial activity through manual-entry workflows.
- Activity Flex acquisition and processing, including Activity-to-Confirmation
  enrichment and summary/component reconciliation.
- Financial contributions from Confirmation order or symbol summaries. Preserve
  them in the original report; execution/booked rows supply financial activity.
- Deposits, withdrawals, standalone fees, dividends, coupons, withholding and
  other non-trade cash activity absent from the Confirmation stream.
- Securities transfers, reported lot/cost allocations, wash-sale adjustments and
  dedicated corporate-action processing, including Treasury-bill maturity events.
- Positions, balances, NAV, reported or calculated P&L, and reconstruction of
  holdings, cash, lots or missing lifecycle outcomes.
- SnapTrade and other acquisition providers, multiple-account workflows, file
  imports, automatic source switching and peer-source reconciliation.

Exercise, explicit cancellation/replacement, adjusted-option and representative
gilt checks use representative evidence or clearly identified synthetic cases.
Synthetic repeat-acquisition proofs cover receipts without redundant bodies or
normalization, pre-correction claim comparison, duplicate-row multiplicity and
retention of disputed variants. Known claims add provenance without changing the
accepted publication. Raw report retention alone does not establish financial coverage. Private reports
and credentials are not repository fixtures.

## Canonical publishing

The versioned read-only socket publishes complete accepted accounts, instruments,
listings and activity snapshots, individual resources and revision-bound explanations.
Each snapshot has warehouse identity and an opaque publication version. Conditional
GET avoids unchanged downloads. Accepted source/operator corrections, conflicts and
relevant reference/link changes advance publication; repeated known claims do not.
Restarts and access-store recovery retain publication with an intact warehouse.
Warehouse replacement establishes a clearly reported new baseline.

The [wire contract](../../api/openapi/v1.yaml) owns exact semantics. The independent
[consumer acceptance procedure](../engineering/development.md#socket-consumer-acceptance)
uses a separate process and actual private socket without Wake store access, internal
application calls or upstream credentials. Its own transactional mirror retains the
last complete validated snapshot on failed transfers or writes. Checks cover exact
values, conditional reads, corrections/conflict raise/clear, restart, access recovery
and warehouse replacement. In-process tests alone do not establish this boundary.

## Compatibility

The executable targets Linux. Storage uses explicit pre-release development
formats with no automatic compatibility migration. Unsupported stores fail closed
with recreate guidance and preserve the original files. Back up stores and sidecars
before explicitly moving or resetting them. The warehouse retains financial
evidence, decisions and operational progress in one database. The encrypted access store has an independent path. Replacing that
store preserves the warehouse and pauses upstream work until access is repaired
and explicitly resumed; recreating the warehouse preserves reusable encrypted
access but establishes new financial identities. See
[architecture](../engineering/architecture.md) for trust and recovery boundaries and [development](../engineering/development.md)
for verification and reset procedures. No version tag is implied by this coverage.

Admin activity, instrument, review and retained-observation lists use server filtering
and stable ordering, with 50 rows per page by default. JavaScript is required for
administration. Tabulator renders activity, instrument and observation tables from
authenticated JSON; source/status regions use focused updates, and accepted detail
pages offer an explicit refresh action. Retained observations preserve original
source bindings, underliers and appearance multiplicity after operator relinks.
Numeric ordering preserves exact decimal values,
keeps currency/unit roles distinct and places unknown values last. Status and setup
prerequisites use focused metadata reads; source detail loads one connection and a
bounded account-candidate page. Result counts and page links preserve intentional
URL filters. These bounds limit response memory; filtered counts and sorting still
require SQLite to inspect the matching history.

Processing, correction preflight, saved decisions and releases share deterministic
financial calculations. Synthetic proofs cover exact decimal and timestamp
precision, source disagreement, read-only checks, decision release and transaction
rollback. The retained-history contribution requirement remains in the
[development guide](../engineering/development.md); there is no automatic updater.

Correction reconciliation connects ambiguous endpoints through a shared anchor and visits each replacement path once. Synthetic long-chain, cycle and wide-ambiguity proofs preserve whole-group review behavior. Large retained groups still require memory and calculation inside the writer transaction; these checks do not establish unlimited history capacity.
