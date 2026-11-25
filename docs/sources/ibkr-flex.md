# Connect Interactive Brokers with IBKR Flex

Wake reads trade history from a Trade Confirmation Flex query for one confirmed IBKR
account per connection. An optional Activity Flex query adds security transfers, cash
postings, reported realized profit and loss (P&L), and reported open positions.

Use one Wake source connection per brokerage account. If you already use
[SnapTrade Personal](snaptrade.md) for this IBKR account, use a different account here.

## Prepare a Trade Confirmation Flex query

1. In IBKR Client Portal, open **Performance & Reports → Flex Queries** and create
   a saved **Trade Confirmation Flex Query** with XML output.
2. Include account identifiers and execution-level trade details. Select instrument
   identifiers and contract terms, trade and execution IDs, dates and times,
   buy/sell, quantity, price, currency, commissions and taxes. Include transaction
   type, level of detail, and the opening or closing indicator where available.
   Include FIGI, ISIN and CUSIP where available and choose a timezone for timestamps.
3. Save the query and note its Query ID. In **Flex Web Service Configuration**,
   enable the service and generate a token. Keep the token private.
   [IBKR Flex Web Service instructions][flex-service]

Confirmation reports cover supported equity, ETF, option and debt executions and
reported FX conversions. Wake also reads reported option outcomes and identified
stock bookings. Order and symbol summaries remain available in the original report.

## Create the connection and confirm the account

1. In Wake, open **Connections → Add source connection** and find **IBKR Flex**.
2. Enter a **Connection name**, the **Confirmation Query ID** and the
   **Flex Web Service token**. Select **Create connection**. Wake encrypts the token
   with your installation's credential key. If the key is missing, follow
   [Get started](../get-started.md) to provision it.
3. Open the connection's **Status** tab. When an account appears under **Choose
   your account**, use **Confirm account** and verify the displayed source account
   and account name. Leave account currency as **Unknown** if you are unsure.
4. Select **Confirm account** on the review screen to authorize automatic
   acquisition for the selected account.

If no account appears, check **Synchronization status** for a pending attempt
or a provider error. Select **Discover accounts again** when it is available.

Use **Status** for the latest retrieval outcome and the next attempt. Use
**Reports** for received evidence and processing progress. The first acquisition
requests up to 365 days. Later requests overlap recent history and can widen after
downtime. The requested dates, dates returned by IBKR and receipt time answer
different questions. See [Review your financial history](../history.md).

## Optional Activity query

Create a saved **Activity Flex Query** with XML output for the same IBKR account.
Include the sections for the data you want Wake to read.

- **Transfers**. Use TRANSFER detail for security movements. Include account and
  transaction IDs, instrument references, direction, quantity, dates and any
  available transfer method or counterparty details. LOT rows remain evidence.
- **Cash Transactions**. Use DETAIL and include the account and transaction ID,
  signed amount and currency, type, description, dates, and available instrument,
  action and trade references. Include **Sales Taxes** with transaction and taxable
  transaction IDs, date, amount and currency for separately reported taxes.
- **Trades**. For realized P&L, use execution-level data with account and contract
  identifiers, trade and execution references, quantities, currencies, dates,
  **Cost Basis** and **Realized PNL**. The amounts apply to individual activities
  and reflect the broker's reported values.
- **Open Positions**. Use SUMMARY detail for the whole account without model or
  instrument filters. Include account and contract identifiers, asset class,
  quantity, currency, report date, side, available contract terms and
  **Cost Basis Price**. View the resulting account report in **Positions**.

On your existing Wake connection, open **Access**, enter the optional
**Activity Query ID**, then select **Save connection settings**. Both queries
use the same encrypted token. To stop requesting Activity reports, clear the
Activity Query ID. Wake keeps previously acquired evidence.

An included, empty section and a missing section have different meanings. For
example, a complete empty positions section can report no holdings. A missing
or unusable section leaves that report's positions unavailable. For exact fields,
position limits and P&L matching, see the [report interpretation reference][reference].

## Repair source access

Renew an expired or invalid token in IBKR. Generating a new token invalidates
the previous one. In Wake, open the same connection's **Access** tab, enter the
replacement token, and select **Replace credential**. If a saved query changed,
update its ID under **Access** and select **Save connection settings**.
[IBKR Flex Web Service instructions][flex-service]

Return to **Status** and select **Retry connection** when available. Wake keeps
the existing account and retained reports. Provider pacing still applies, so
check the next attempt time. If Wake shows a recovery review after restoring
or upgrading an installation, follow the recovery guide for your
[native installation][native-recovery] or [appliance][appliance-recovery].

[flex-service]: https://www.ibkrguides.com/adminportal/performanceandstatements/flex3.htm
[reference]: https://github.com/trippwill/wake/blob/v0.1.0/.docs/providers/ibkr-flex.md#optional-activity-query
[native-recovery]: ../deployment/native.md#recover-missing-synchronization-storage
[appliance-recovery]: ../deployment/wake-appliance.md#troubleshoot-startup
