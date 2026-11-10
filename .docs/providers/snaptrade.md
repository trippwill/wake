# SnapTrade Personal

Wake discovers existing Fidelity US and Interactive Brokers investment accounts
in SnapTrade Personal. Wake reads cached purchases and sales for both brokers
and reported positions for Fidelity US. Link or repair your
brokerage in the SnapTrade dashboard. Use a different real account on each Wake
connection, including direct IBKR Flex connections. See [SnapTrade Personal
setup](https://docs.snaptrade.com/docs/getting-started#personal-quickstart).

## Connect and confirm

1. Create a Personal API key in SnapTrade and copy its client ID and consumer key.
2. In Wake, open **Connections → Add source connection**, select **SnapTrade
   Personal**, and save a name, the client ID, and the consumer key. Wake stores
   the consumer key encrypted.
3. Open the connection’s **Status**, select **Confirm account** for the desired
   candidate, and confirm its details. Leave the account currency empty if it is
   unknown. Confirmation grants automatic acquisition for that account.

Use **Access** on the connection to replace the consumer key. If Wake reports
that the upstream account is unavailable, check that the original brokerage
connection is active in SnapTrade and retry after repairing it. An account with
changed source identity requires a new connection and confirmation. Keep existing
financial history when repairing credentials. See [Wake setup and recovery](../../README.md#connect-and-inspect).

## Understand the history

A completed scan schedules the next one after 24 hours by default. Configure the
interval in the connection's **Access** view. Wake reads the available
cache through a fixed UTC date and rechecks upstream account identity at the start
of a scan. A resumed scan keeps its original end date. The cache can lag the
brokerage. Wake retains accepted discovery and activity responses and uses the
source account’s native activity IDs to recognize
repeats. An unstable page sequence restarts from the beginning after a delay.
Source status and retry timing are under **Status**. Processing and retained
evidence are under **Reports**. See [SnapTrade data freshness](https://docs.snaptrade.com/docs/syncing).

Wake interprets equity, fund, and option purchases and sales at the reported
transaction grain. It preserves exact trade terms and reported fees. Fidelity
trades contribute a civil date. IBKR trades can also contribute a timestamp.
Trade cash amounts with an unspecified gross or net role stay in the source
report. A missing transaction in a later response leaves the retained history
intact. See [SnapTrade activities](https://docs.snaptrade.com/reference/Account%20Information/AccountInformation_getAccountActivities).

## Fidelity US positions

Wake checks for positions every 24 hours by default, independently of trade history.
Set the positions interval under **Access → Polling intervals** or use
`"snaptrade:positions"` in `[polling.defaults]`. The minimum is one hour.
An existing confirmed account starts position sync on upgrade. Open **Positions** and
select the account to see its latest complete holdings. The time shown is when
SnapTrade fetched the holdings from Fidelity. Fidelity data can already be up to
24 hours old at that point.

Wake reads stocks, ADRs, funds, bonds, and equity and ETF options. Bond quantities
use the units SnapTrade reports. Cash-equivalent funds remain holdings. Positions
shows the top-level book price as **Unit cost basis** when present. Holdings SnapTrade classifies as
`other` retain their reported quantity and identity as unclassified instruments.
An unsupported holding or an unusable required field makes a dated set incomplete. See [Fidelity coverage](https://support.snaptrade.com/Fidelity-ad8feaa69a1c83eca04301cfac6e5071?pvs=21)
and the [positions endpoint](https://docs.snaptrade.com/reference/Account%20Information/AccountInformation_getAllAccountPositions).
