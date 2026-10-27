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

Wake's current equity processing accepts exchange executions. Booked trades,
options, debt, FX, order summaries and symbol summaries remain in the original
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
