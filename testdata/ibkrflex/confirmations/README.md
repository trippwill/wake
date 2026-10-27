# Synthetic IBKR Confirmation reports

These fixtures use the IBKR Confirmation XML element/attribute shape. They contain fictional activity, not anonymized copies of
private trades. Account and provider IDs, dates, quantities, prices, fees and
execution histories are newly authored. Public Microsoft, gilt and Treasury-bill
security metadata is retained for reference-resolution examples. Option contracts
are fictional and have no asserted FIGI, ISIN or CUSIP.

## Files and expected sequence

- [initial.xml](initial.xml): 17 execution-detail rows, including five `BookTrade`
  bookings, plus one order and one symbol summary.
- [overlap.xml](overlap.xml): ten unchanged rows and two newly observed executions.
  One new observation has a trade date inside the earlier window. Earlier trades
  outside this window are absent; absence does not cancel them.
- [expected.json](expected.json): stable scenario labels, counts, exact-value checks
  and sequence expectations, independent of any Wake schema or generated IDs.
- [openfigi.json](openfigi.json): dated public mapping responses for offline Microsoft,
  gilt and bill reference stubs. No API key or live lookup is needed.

Read initial, repeat the same bytes, read overlap, then repeat overlap. There are
17, 17, 19 and 19 distinct reported trade IDs respectively. This is an expected
identity invariant for processing tests. Summaries never add executions.

## Useful cases

Two fractional fills share an order, timestamp and financial terms but have distinct
trade/execution IDs. They remain distinct. Decimal text includes more than eight
fractional places and retained trailing zeros.

Option opening/closing executions are separate from put/call assignment outcomes
and expiration. Assignment stock purchases/sales are their own bookings. All five
bookings lack execution IDs. Every option points to the same source underlier, but
no explicit lifecycle-pair reference is supplied; matching dates or amounts alone
must not manufacture one. The long call has one remaining contract and no
reported outcome despite its expiry date; expiry alone does not supply one.

FX rows preserve base/quote roles and include a GBP amount with a USD commission;
the fixture does not convert that commission into GBP. The gilt uses nominal
quantity, a price per 100 and separately reported accrued interest. The Treasury
bill lacks a reported FIGI but has ISIN/CUSIP. Its reported amount is rounded to
cents and intentionally differs from exact nominal-times-price arithmetic; keep
both source values.

The order/symbol summaries repeat execution economics at another grain. Empty
account alias and unused optional fields remain present. The XML retains all
report attribute names, with newly authored or empty values.

## Limits

These are focused synthetic scenarios, not complete production exports. Only two
representative summaries are included. The overlap timeline is authored; it does
not measure IBKR latency. Exercise, explicit source cancellation/replacement,
adjusted deliverables and multiple accounts are not covered. Activity Flex processing is outside the current coverage.

The [coverage guide](../../../docs/product/coverage.md) owns financial coverage
independently of this fixture. Synthetic tests do not establish upstream arrival
latency, account discovery across configurations or broker-wide reporting coverage.
