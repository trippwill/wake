# Set up IBKR Flex

The selected v0.1.0 coverage uses Trade Confirmation Flex reports for one IBKR account per connection,
with an optional Activity Flex query for transfers, non-trade cash postings and
reported realized P&L on individual activities, and reported open positions.

## Prepare access and the query

In IBKR reporting, create a saved Trade Confirmation Flex query with XML output
and enable Flex Web Service access. Keep its saved Query ID and access token for
connection setup. Keep the token private and renew it through IBKR when required.

Include the available account information and execution/booked-trade details:
instrument identifiers and terms, source account and trade references, quantities,
buy/sell, prices, currencies, dates and times, commissions and taxes. Include FIGI,
ISIN and CUSIP where available, alongside IBKR references. Missing identifiers
remain visible for resolution or review.

Wake accepts equity, ETF and option exchange executions, reported option outcomes and independently
identified stock bookings and debt executions and reported FX conversions. Order summaries and
symbol summaries remain in the original report. The [current coverage](../product/coverage.md) owns
broader selected coverage.

Use execution detail and include `transactionType`, `levelOfDetail`, the source
execution/trade references and any opening/closing indicator. An execution detail
level alone does not make a booking an exchange execution. Include an explicit
timezone on timestamps; a missing time remains unknown. Account currency comes
from available profile metadata or operator confirmation, never from a trade's
currency.

## Optional Activity query

Create a saved Activity Flex query with XML output for the same account, then add
its Activity Query ID to the existing Wake connection. Both queries use the same
encrypted Flex Web Service token. The provider interprets each report section and
contributes to the shared core Activity model. Removing the Activity Query ID stops
its acquisition while preserving retained results and activity.

Include Transfers at TRANSFER detail for inbound/outbound security movements,
including source account and transaction ID, instrument references, direction,
quantity, transfer date, report/settlement dates, transfer method, delivering broker
and reported counterparty details.
Reported transfer price, position value, aggregate cost and P&L stay separate.
LOT detail remains original evidence and does not create additional movements.
See IBKR's [Transfers reference](https://www.ibkrguides.com/reportingreference/reportguide/transfersfq.htm).
Transfer-only reports do not require a Trades section. Missing sections establish
no coverage; an included empty section reports no rows for its returned window.

Upgraded interpretations automatically queue retained acquired reports for bounded,
repeat-safe processing without refetching. Inspect processing progress and failures;
existing activity identities and operator decisions remain in place.

### Non-trade cash postings

Include Cash Transactions at DETAIL level, including account, transaction ID,
amount/currency, type, description, posting/report/settlement/ex dates and the
reported available-for-trading date, instrument identifiers when
available, action ID and any trade reference. SUMMARY rows remain evidence without
another financial contribution. See the
[Cash Transactions reference](https://www.ibkrguides.com/reportingreference/reportguide/cash%20transactionsfq.htm).
Include Sales Taxes with transaction ID, taxable
transaction ID, date, signed amount and currency for separately reported tax postings.

Unsupported identifier schemes remain in evidence. When no supported instrument
reference or reported symbol is available, an otherwise complete cash posting is
accepted without an instrument link.

Cash deposits/withdrawals, dividends, payments in lieu, interest, withholding,
standalone fees and adjustments retain their signed reported amounts and types.
Unfamiliar types remain other reported cash. Shared action IDs are grouping references,
not posting identities. Other explicit trade references relate a posting to its
trade without becoming event aliases or proving a shared monetary component.
Explicit taxable-transaction links resolve as their targets
arrive. Transfer cash amounts create cash activity only when nonzero, and remain
separate from securities quantities and transfer valuations. Cash Transactions and
Transfers use the same qualified cash transaction references for overlapping claims.

An explicitly trade-linked Bond Interest Paid/Received row contributes accrued
interest to that trade instead of another non-trade event; an unmatched trade
reference remains pending. Neither a payment description nor matching amounts
establish that link. Zeroes on genuine detailed postings remain exact zeroes.
No posting, summary, interest-accrual or dividend-accrual section establishes a
calculated cash balance, net income, holdings or tax treatment.

### Reported realized P&L

Include execution-level Trades with source account, contract, trade and execution
references, quantities, currencies, trade/report dates, Cost Basis and Realized PNL.
The observed XML names include `tradeID`, `ibExecID`, `transactionID`, `conid`, `cost`
and `fifoPnlRealized`. Include both trade and execution references because booked
activity can lack an execution ID. Preserve the signed allocated cost and gain/loss;
Wake does not recompute either or subtract charges a second time. IBKR documents the
[Activity Trades fields](https://www.ibkrguides.com/reportingreference/reportguide/tradesfq.htm).

Only execution-level activity results contribute realized P&L. Closed-lot rows, period
summaries and dedicated Forex P/L Details remain outside this interpretation. Extra
sections remain in the original evidence. In particular, an FX conversion trade's
reported result does not establish the realized gain on currency holdings.

The first Activity acquisition requests 365 days. Daily acquisition requests a
seven-day overlap, widening for downtime with bounded older catch-up. This cadence
does not establish live P&L or guarantee discovery of older restatements. Returned
coverage and source report dates remain distinct from retrieval time. IBKR's
[P&L-not-ready responses](https://www.ibkrguides.com/adminportal/performanceandstatements/flex3error.htm)
retain retry progress without acknowledging a completed report. Both streams share
the token's durable request budget and keep independent continuations and failures.

Wake links results through qualified source references and original activity context.
Unmatched results wait for related evidence. Repeated known results add provenance;
changed results use the same field reconciliation as other activity observations.
Competing values retain the durable baseline with disagreement; an originally
unreported value remains unknown when later enrichments compete. Retrieval order
does not establish revision authority. Select or release retained fields through
the activity's correction workflow; choosing partial evidence preserves omitted
terms. Correcting a trade's price or quantity does not
recalculate the broker's result or its allocated cost.

### Reported open positions

Include Open Positions at SUMMARY detail for the whole confirmed account, without
instrument or model filters. Include account and contract references, asset class,
quantity (`position`), currency, report date, side, available contract terms and
Cost Basis Money (`costBasisMoney`) when needed. Wake preserves the reported aggregate
cost, including explicit zero, without deriving it from prices or lots. See IBKR's
[Open Positions reference](https://www.ibkrguides.com/reportingreference/reportguide/open%20positionsfq.htm).

Summary rows establish one common reported date; an explicitly empty section uses the
statement end date. Missing sections establish no position coverage. LOT rows never
add quantities; a lot-only populated section cannot establish the summary set. Malformed
rows, mixed report dates, model partitions or unsupported asset families withhold the
whole set. An unusable set retains its latest valid summary row date when available;
the statement end date applies when no summary row establishes a date.
This adapter supports the existing equity/ETF, option and debt families.
Generation and retrieval times do not select between conflicting same-date sets.
Positions use the Activity acquisition cadence and their own client version and event;
they do not establish live holdings, market values or unrealized P&L.

## Connection and processing boundaries

The [current coverage](../product/coverage.md) defines
connection creation, account discovery, operator confirmation and automatic
synchronization; the
[quick guide](../../README.md#using-wake-v010) describes current setup and use.

Saved access tokens require encryption. Do not put a token in a display name or
diagnostic message. Confirmation reports update during the day with an unspecified
delay; successful retrieval and completed normalization or financial processing
remain separate outcomes.

Confirmation discovery and the first authorized acquisition independently request
the preceding 365 days. Ongoing Confirmation synchronization requests two days every
five minutes and widens
after downtime. Older catch-up uses dated windows of at most 365 days. IBKR
documents both period and dated overrides in its
[Flex Web Service guide](https://www.ibkrguides.com/adminportal/performanceandstatements/flex3.htm).
Returned bounds can differ from requested bounds; inspect both in synchronization
progress. Unavailable ranges remain pending while recent work continues.

Generation polling, pacing and transient retries resume automatically. Invalid
access or query configuration pauses the affected work. Renew the token or repair
the saved query configuration, then use the connection's repair/retry controls.
Changing a query restarts its own bounded source protocol while preserving
the confirmed account and retained reports. Source financial authority stays with
the confirmed connection, provider and qualified source account.

Option execution terms retain the reported strike, currency, put/call, expiry and
premium multiplier separately from the underlier.
A structurally valid, checksum-valid `underlyingSecurityID` is retained as an ISIN
on the underlier, with its source field attribution. Malformed or differently
formatted values do not become ISIN assertions and do not reject the contract.
A reported OCC symbol is eligible for mapping only when its exact structure,
expiry, put/call and exact strike agree with the reported terms. Wake does not
construct an OCC symbol from a display name or guessed terms.
 Confirmation does not establish
an adjusted deliverable merely by reporting a multiplier; absent deliverables stay
unknown. Instrument mapping follows the response subject in the
[OpenFIGI mapping contract](https://www.openfigi.com/api/documentation), so an
underlier lookup cannot select the option contract.

Wake interprets IBKR's reported FIGI as a composite for stock/fund and supported
equity-option records, and as an instrument/issue FIGI for supported debt. This is
an adopted provider convention supported by experiments, rather than a published
IBKR guarantee. All 56 distinct reported FIGIs in the inspected Confirmation and
Activity samples agreed: ten stocks and 45 options were composites, and one bond
was issue-level. The original field attribution and exact value remain attached;
OpenFIGI can corroborate the claim and contradictory results remain reviewable.
A composite does not identify the reported listing venue. See the
[sample study and limits](../research/ibkr-figi-allocation.md).

Wake interprets the documented `Ex`, `A` and `Ep` codes on option bookings as
exercise, assignment and expiration. Stock bookings retain independent identities;
these markers alone establish no link to an option activity. The ambiguous `GEA`
marker remains reviewable. Qualified trade IDs support rows without execution IDs.
See the [IBKR Confirmation code reference](https://www.ibkrguides.com/reportingreference/reportguide/codestradeconfirm.htm).

Debt executions preserve nominal (face) quantity, exact reported price and currency,
principal/amount, proceeds, accrued interest, commissions, taxes, net cash and net cash with
billable independently. Wake makes no quotation-basis assertion and does not calculate a missing
principal or settlement total. In the inspected gilt and Treasury-bill samples, trade net cash
excludes the separately reported accrued interest. Keep those roles separate even when their
arithmetic happens to agree. A Confirmation trade row does not establish coupon or maturity from a
symbol; missing terms remain unknown. Richer explicitly supplied issue terms retain their own
provenance. See [debt reporting research](../research/ibkr-debt-reporting.md) for the documentation
limits and sample evidence. Debt reference identity uses an issue FIGI rather than requiring a
share-class FIGI, and no listing is created without reported listing evidence.

For a reported FX pair, buy/sell establishes sent and received currency roles.
Quantity supplies the base amount and a reported gross amount supplies the quote
amount when available; Wake does not calculate a missing counterpart from the
rate, proceeds or net cash. The quoted rate retains its original orientation.
Gross, proceeds, net cash and charges stay separate, including charges reported
in another currency. Currency references establish no balance, lots or P&L.

Wake interprets an explicit `Ca` with an original trade reference as cancellation,
and an explicit `Co` with that reference and an independently identified replacement
row as replacement. It retains uncertain code/reference combinations for review.
The [IBKR configuration reference](https://www.ibkrguides.com/reportingreference/reportguide/trade%20confirmation%20configuration.htm)
defines the original-trade reference; the code reference defines the markers.
Neither a missing later row nor an amount/time match is a correction instruction.

Wake rejects upstream reports whose raw bytes or decoded XML attributes contain the access token,
and rejects credential-bearing decoded generation references. Accepted report bytes remain unchanged
as evidence.

## Charges and fee breakdowns

Confirmation execution rows preserve commission as a total with broker execution,
broker clearing, third-party execution/clearing, regulatory and other commission
components. Regulatory fees retain their own subtotal and FINRA, Section 31 and
other regulatory details. All levels use the explicit commission currency, including
FX commissions reported in another currency. Tax, sales tax, trade charge and other
tax remain independent roles in the transaction currency; Wake does not infer an
undocumented containment relationship between them. IBKR's
[Confirmation configuration reference](https://www.ibkrguides.com/reportingreference/reportguide/trade%20confirmation%20configuration.htm)
distinguishes the commission total and its principal components.

The regulatory nesting is an interpretation supported by the inspected private
Activity/Confirmation sample, not a universal arithmetic guarantee: all 190 execution
rows supplying the regulatory fields matched that subtotal's three details, while
one commission total differed from its six principal components. Preserve totals
and details independently, including zeros, rebates and missing subtotals. The
samples establish no nonzero relationship between the separate tax/charge fields.
Private reports remain outside repository fixtures; synthetic tests exercise these
interpretations and their limits.

Activity Cash Transactions fee postings retain their reported total amount with no
invented breakdown. Sales Taxes remain separate postings with their explicit taxable
transaction links. Neither the taxable amount nor a related fee creates a combined
total. Activity Commission Details and summary rows remain original evidence without
an additional financial contribution.

## Repeated reports

Every completed fetch records its actual digest, retrieval time and requested and
returned bounds. The provider's Confirmation report comparison excludes only the attribute
values `fromDate` and `toDate` on report-level `FlexStatement` elements. All other
bytes, including generated timestamps, whitespace and row dates, remain part of
the comparison. Report-window-only repeats reuse the retained body without
repeating normalization. Receipts identify that reuse; the evidence page displays
the retained original body rather than reconstructing discarded window variants.

Activity reports use exact-byte body comparison. Their source dates and original
reporting bounds remain intact; the Confirmation window-exclusion policy does not
apply. Per-activity claim comparison still excludes structural row location while
preserving references, exact values, unknowns, source dates and row multiplicity.
