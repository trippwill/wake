# Set up IBKR Flex

The selected v0.1.0 coverage uses Trade Confirmation Flex reports for one IBKR account.

## Prepare access and the query

In IBKR reporting, create a saved Trade Confirmation Flex query with XML output
and enable Flex Web Service access. Keep its saved Query ID and access token for
connection setup. Keep the token private and renew it through IBKR when required.

Include the available account information and execution/booked-trade details:
instrument identifiers and terms, source account and trade references, quantities,
buy/sell, prices, currencies, dates and times, commissions and taxes. Include FIGI,
ISIN and CUSIP where available, alongside IBKR references. Missing identifiers
remain visible for resolution or review.

Wake accepts equity, ETF and option exchange executions, reported option outcomes and
independently identified stock bookings and debt executions and reported FX conversions. Order summaries and symbol summaries remain in the original
report. The [current coverage](../product/coverage.md) owns broader selected coverage.

Use execution detail and include `transactionType`, `levelOfDetail`, the source
execution/trade references and any opening/closing indicator. An execution detail
level alone does not make a booking an exchange execution. Include an explicit
timezone on timestamps; a missing time remains unknown. Account currency comes
from available profile metadata or operator confirmation, never from a trade's
currency.

## Connection and processing boundaries

The [current coverage](../product/coverage.md) defines
connection creation, account discovery, operator confirmation and automatic
synchronization; the
[quick guide](../../README.md#using-wake-v010) describes current setup and use.

Saved access tokens require encryption. Do not put a token in a display name or
diagnostic message. Confirmation reports update during the day with an unspecified
delay; successful retrieval and completed normalization or financial processing
remain separate outcomes.

Discovery and the first authorized acquisition independently request the preceding
365 days. Ongoing synchronization requests two days every five minutes and widens
after downtime. Older catch-up uses dated windows of at most 365 days. IBKR
documents both period and dated overrides in its
[Flex Web Service guide](https://www.ibkrguides.com/adminportal/performanceandstatements/flex3.htm).
Returned bounds can differ from requested bounds; inspect both in synchronization
progress. Unavailable ranges remain pending while recent work continues.

Generation polling, pacing and transient retries resume automatically. Invalid
access or query configuration pauses the connection. Renew the token or repair
the saved query configuration, then use the connection's repair/retry controls.
Changing connection settings restarts the bounded source protocol while preserving
the confirmed account and retained reports. Source financial authority stays with
the selected account and Confirmation stream.


Option execution terms retain the reported strike, currency, put/call, expiry and
premium multiplier separately from the underlier. Confirmation does not establish
an adjusted deliverable merely by reporting a multiplier; absent deliverables stay
unknown. Instrument mapping follows the response subject in the
[OpenFIGI mapping contract](https://www.openfigi.com/api/documentation), so an
underlier lookup cannot select the option contract.


Wake interprets the documented `Ex`, `A` and `Ep` codes on option bookings as
exercise, assignment and expiration. Stock bookings retain independent identities;
these markers alone establish no link to an option activity. The ambiguous `GEA`
marker remains reviewable. Qualified trade IDs support rows without execution IDs.
See the [IBKR Confirmation code reference](https://www.ibkrguides.com/reportingreference/reportguide/codestradeconfirm.htm).


Debt executions preserve nominal quantity, exact quoted price, accrued interest and
all reported monetary roles independently. A Confirmation trade row does not
establish coupon, maturity or quotation basis from a symbol or amount equation;
missing terms remain unknown. Richer explicitly supplied issue terms retain their
own provenance. Debt reference identity uses an issue FIGI rather than requiring a
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

Wake rejects upstream reports whose raw bytes or decoded XML attributes contain the access token, and rejects credential-bearing decoded generation references. Accepted report bytes remain unchanged as evidence.

## Repeated reports

Every completed fetch records its actual digest, retrieval time and requested and
returned bounds. The provider's report comparison excludes only the attribute
values `fromDate` and `toDate` on report-level `FlexStatement` elements. All other
bytes, including generated timestamps, whitespace and row dates, remain part of
the comparison. Report-window-only repeats reuse the retained body without
repeating normalization. Receipts identify that reuse; the evidence page displays
the retained original body rather than reconstructing discarded window variants.
