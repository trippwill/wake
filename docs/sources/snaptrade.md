# Connect a brokerage account with SnapTrade Personal

Wake reads cached purchases and sales for existing Fidelity US and Interactive
Brokers investment accounts linked in SnapTrade Personal. For Fidelity US, Wake
also reads reported positions. Link your brokerage in the SnapTrade dashboard
before creating the Wake connection.

Use one Wake source connection per brokerage account. If you already use
[IBKR Flex](ibkr-flex.md) for an IBKR account, use a different account here.

## Create the connection and confirm the account

1. In SnapTrade, create a Personal API key and copy its client ID and consumer
   key. Keep the consumer key private.
   [SnapTrade Personal API key instructions][personal-key]
2. In Wake, open **Connections → Add source connection** and find
   **SnapTrade Personal**. Fill in **Connection name**, **Personal Client ID**
   and **Personal consumer key**, then select **Create connection**. Wake encrypts
   the consumer key with your installation's credential key. If the key is
   missing, follow [Get started](../get-started.md) to provision it.
3. Open the connection's **Status** tab and select **Confirm account** beside the
   account you want Wake to synchronize. Check the source account and account
   display name. Leave account currency as **Unknown** if you are unsure.
4. Select **Confirm account** on the review screen to authorize automatic
   acquisition for that account.

If no account appears, check **Synchronization status** for a pending attempt
or a provider error. Select **Discover accounts again** when it is available.

## Check trade history and positions

Use the connection's **Status** tab to see acquisition progress and the next
attempt. Use **Reports** to inspect the received data and processing status.
A completed history scan schedules the next one after 24 hours by default.
SnapTrade's cache can lag the brokerage. The date of a trade and the time Wake
received it tell you different things. [SnapTrade data freshness][freshness]

Wake reads equity, fund and option purchases and sales at the level reported by
the source. It preserves reported fees and exact trade terms. Fidelity trades
include a civil date. IBKR trades can also include a timestamp. If a later
response omits a transaction, Wake keeps the evidence it already received.

For a confirmed Fidelity US account, open **Positions** and select the account.
Wake checks the holdings every 24 hours by default, independently of trade
history. The time shown records when SnapTrade fetched the holdings from
Fidelity. The brokerage data can already be up to 24 hours old at that point.

Wake reads stocks, ADRs, funds, bonds, and equity and ETF options from Fidelity US.
**Unit cost basis** shows the reported book price when present. An unsupported
holding or an unusable required field can make a dated holdings set incomplete.
See [Review your financial history](../history.md).

To change the history or positions cadence, open the connection's
**Access → Polling intervals**. The positions interval has a one-hour minimum.

## Repair source access

If Wake reports that the account is unavailable, check the brokerage connection
in the SnapTrade dashboard and repair it there. To replace a consumer key in
Wake, open the existing connection's **Access** tab, enter the replacement key,
and select **Replace credential**. Return to **Status** and select
**Retry connection** when available.

Keep the existing connection when repairing access to the same brokerage
account. If the account has a changed source identity, create a new connection
and confirm the account there. The old connection's retained evidence remains
available. For a restart review after storage recovery, see the guide for your
[native installation][native-recovery] or [appliance][appliance-recovery].

[personal-key]: https://docs.snaptrade.com/docs/personal-api-key
[freshness]: https://docs.snaptrade.com/docs/syncing
[native-recovery]: ../deployment/native.md#recover-missing-synchronization-storage
[appliance-recovery]: ../deployment/wake-appliance.md#troubleshoot-startup
