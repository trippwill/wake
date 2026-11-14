# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Each instance serves one user. Local and LAN use without internet exposure is the
supported scope.

Wake acquires IBKR Flex reports, cached SnapTrade Personal trade history, and
reported Fidelity US positions through SnapTrade.
Confirm one account per connection and inspect activity, instruments and available
reported positions in the admin console. Wake retains original reports, exact
financial values and conflicts.

### Set up and start

To build Wake from a Linux checkout:

```sh
mise install
mise run build:release
export PATH="$PWD/.cache/release:$PATH"
```

With `wake` on `PATH`, create the configuration and key for a new instance at the
default path:

```sh
install -d -m 0700 "${XDG_CONFIG_HOME:-$HOME/.config}/wake"
touch "${XDG_CONFIG_HOME:-$HOME/.config}/wake/config.toml"
wake credentials generate-key
wake admin set-unlock
wake serve
```

`wake credentials generate-key` uses the existing TOML and creates a private
key at `credentials.key_path`. For a custom key path, create its private parent
directory first. When you already have a TOML and usable key, start with `wake serve`.
Set the browser unlock before or after startup and sign in at `http://127.0.0.1:8787`.
Stop the foreground daemon with Ctrl+C.

Wake selects `config.toml` through `--config`, `WAKE_CONFIG`, or
`${XDG_CONFIG_HOME:-$HOME/.config}/wake/config.toml` in that order. Use the same
file for key generation, serving, and recovery. An empty file uses the following defaults:

```toml
[storage]
warehouse_path = "wake.db"
connections_path = "connections.db"
unattended_upgrade = false

[credentials]
key_path = "credentials.key"

[paths]
# runtime_dir defaults to a directory under $XDG_RUNTIME_DIR/wake.
# Set a private local directory here if XDG_RUNTIME_DIR is unavailable.

[log]
level = "info"

[admin]
listen = "127.0.0.1:8787"
# public_origin is derived from the loopback listener.
currency_refresh = false

[api]
listen = "127.0.0.1:8788"

[polling.defaults]
# Use the registered interval for each provider stream.
```

File and directory settings accept absolute paths or paths relative to the selected
TOML's directory. Wake keeps private browser and client authority in `auth.json` beside
that file. Keep a single TOML per instance directory. The default runtime directory is
scoped to the resolved instance directory.

For a prebuilt Linux binary on PATH, run the same three Wake commands without the
build steps. For a prepared configuration, you can supply an existing private
regular file containing 32 raw random bytes at `credentials.key_path`. Setup
verifies the file and its ability to decrypt saved source credentials, if present.
See [operator access and restore](.docs/engineering/architecture.md#operator-access-and-restore)
for the scope of each file.

### Connect and inspect

Open **Connections → Add source connection** and choose the provider. For IBKR Flex,
enter the saved Confirmation Query ID and token. For SnapTrade Personal, enter the
Personal client ID and consumer key. Wake encrypts secret fields and discovers
accounts. See the [IBKR setup](.docs/providers/ibkr-flex.md) and
[SnapTrade setup](.docs/providers/snaptrade.md) guides.

Select **Confirm account**, enter a display name, and confirm the available
details. Leave an unknown account currency empty. Confirmation grants automatic
synchronization for that connection’s selected account. Configure another connection
for a different brokerage account. Use the account selector in Activity or Positions
when several accounts are confirmed.

**Overview** shows setup steps, financial review and source work. Under
**Synchronization**, **Refresh now** brings forward routine polls for confirmed
sources within their pacing limits. It preserves pending retries and continuations. Use the sidebar's
**Activity**, **Instruments**, **Positions** and **Needs review** destinations to inspect financial
history, instrument identities, latest reported positions and unresolved questions. Each workspace
has its own path. Activity, instrument and evidence pages retain a return path to the
originating view within the current tab.
Shared resource links open with canonical navigation. Unresolved financial and
identity conflicts remain distinct from ordinary reference gaps and source failures.
Positions shows its reported effective date/time and availability, exact quantities, reported
contract terms and optional aggregate reported cost basis. It distinguishes an empty reported
set from unavailable positions and links to instrument details and source reports.

Under **Administration**, **Connections** groups each source's **Status**, **Reports**
and **Access**. Repair its Query ID or token beside the affected source.
**Access → Polling intervals** lets you set an interval for each recurring stream.
Clear the field and save to inherit its default. **Status** shows the effective
interval and the next eligible time. Changing the interval recalculates a queued
routine poll from the previous cycle, subject to provider pacing. In-progress
work and retries keep their own deadlines.

To set an installation default, add an entry under `[polling.defaults]` in the
launch configuration. Keys use the registered `provider-id:stream-id` and values
are quoted durations, for example `"ibkr-flex:confirmations" = "10m"`.
Use `wake config check` to validate the file before restarting. At startup, Wake
recalculates queued routine polls whose inherited intervals changed.
**Instrument reference** manages
the optional encrypted OpenFIGI key independently of IBKR access. A rejected key
stays visible until repaired or explicitly cleared for anonymous access.

Use **Correct accepted data** or **Correct instrument data** to correct a field/link,
compare retained evidence or manage decisions. The editor shows the current value;
dates, choices and links use field-specific controls, and edits receive live checks.
Search compatible retained records when correcting a link. Comparisons lead with
differences. Decisions retain the authenticated session, time
and optional rationale. Release a decision to return to automatic selection.

A source's **Reports** view provides **Retry processing report** for retained reports.
Each source report shows its observed financial meaning in a searchable, paginated
table with related activity, source locations and acceptance rules.

**System → Status** shows the current financial database file size in readable
units and exact bytes. This includes retained reports and warehouse operational
data; encrypted access storage and temporary journals are separate.
**System → Configuration** identifies the TOML file loaded at startup, when present,
alongside the effective storage and runtime paths.

If `connections.db` is lost, Wake recreates it and pauses upstream work.
A missing or mismatched credential encryption key also pauses synchronization.
Retained history and existing API grants remain usable. Check **System → Status** for
key recovery guidance and **Connections → Recovery** for source and reference review.
After repair, select **Resume upstream work**. If the original key is unavailable,
stop Wake, back up the files, run `wake reset credentials`, and review the preview.
Confirm the reset and provision a usable key. To generate a replacement, move any
unusable key file aside and run `wake credentials generate-key` with Wake stopped.
Start Wake, enter your source credentials again, and resume synchronization.

The admin console requires JavaScript. Activity, instrument, positions and observation tables
load authenticated JSON, with filtering, exact ordering and pagination performed on
the server at 50 rows per page by default. URLs retain filters and page state;
Tabulator retains column preferences. Accepted detail pages offer an explicit refresh
action, while retained observations update independently. Correction drafts survive
validation errors, and changed data requires review before resubmission.

The daemon writes structured operational logs to stderr, including completed work,
failures and recovery actions. Startup reports both listeners. Set `[log] level = "debug"`
in the TOML for stage, validation, pacing, and cache detail.

To check storage compatibility with a new version, stop Wake and run `wake upgrade`
using the new binary and the same profile. Review any plan before applying it.
See [forward upgrades](.docs/product/coverage.md#forward-upgrades).

To clear credentials or financial history, stop Wake and preview the reset:

```sh
wake reset warehouse
wake reset warehouse --confirm
```

Choose `credentials` or `warehouse`. Reset is a dry run unless `--confirm` is present,
and works with corrupt or unsupported databases. A warehouse reset also clears its
operational state and retains credentials. Start Wake, select **Resume upstream
work**, then confirm the rediscovered accounts. A credentials reset retains financial
history; repair access in **Connections** and resume. Configuration, keys and browser
access are retained.

### Find help

```sh
wake help
wake help credentials generate-key
wake config describe
wake config check
```

Use command help for exact options.

Run `wake admin set-unlock` against the selected instance to set or replace the browser
unlock. It works while Wake is running and signs out existing browser sessions. An
unprovisioned browser listener stays locked until you set the unlock.

### Read financial data from the CLI

Create a named token in **System → API clients** after unlocking the browser.
Save it in a private token file on the client with mode `0600`.

```sh
export WAKE_ENDPOINT=http://127.0.0.1:8788
export WAKE_TOKEN_FILE="$HOME/.config/wake/laptop.token"
wake --json doctor
wake --json accounts list
wake --json accounts get "$ACCOUNT_ID"
wake --json activity query --account "$ACCOUNT_ID" --from 2026-10-01 --to 2026-10-31 --family trade
wake --json positions get "$ACCOUNT_ID"
wake --json request get '/v1/activity/ACTIVITY_ID'
```

Set `ACCOUNT_ID` to an ID from `resources[].id` in the accounts list.
For a different endpoint or credential, pass `--endpoint https://wake-api.home.arpa`
or `--token-file /absolute/client.token` on the leaf command before any positional
argument. Flags override `WAKE_ENDPOINT` and `WAKE_TOKEN_FILE`; the default endpoint
is `http://127.0.0.1:8788`. Client reads use those settings independently of daemon
configuration. Tokens grant financial reads and do not unlock browser administration.
Revoke a lost or retired token in **System → API clients**.

Account lists return `warehouse_id`, `version`, and `resources`. Activity
queries read a complete snapshot for each page and filter it locally. Dates are
inclusive. Results default to accepted activity, newest civil date first, with
up to 100 results per page. For example, a page can return
`{"warehouse_id":"warehouse","version":"version","resources":[],"next_offset":null}`.
When `next_offset` is an integer, pass it as `--offset` with the same filters.
Compare `warehouse_id` and `version` across pages when consistency matters.
Run `wake activity query --help` for the state, family, and limit options.

Exact account and positions reads and raw GETs return the
[API JSON object](api/openapi/v1.yaml). Resource lookups follow up to five identity
redirects within the same resource kind at the configured origin. To explain a
resource, use the ID and revision from its current lookup. Position views distinguish
an available empty array from an unavailable view with `positions: null` and a reason.
Successful commands write JSON to stdout. With `--json`, errors go to stderr
and preserve recognized API codes, as in
`{"event":"error","code":"not_found","message":"Wake API returned HTTP 404","exit_status":1}`.

#### Install the Codex skill

Copy the [wake-read skill](.agents/skills/wake-read/SKILL.md) from a Wake checkout
into your Codex skills directory. For a new installation, run from the checkout
root with `CODEX_HOME` set if you use a custom location.

```sh
codex_skills="${CODEX_HOME:-$HOME/.codex}/skills"
mkdir -p "$codex_skills"
cp -R .agents/skills/wake-read "$codex_skills/"
```

Start a Codex chat and invoke `$wake-read` with the Wake data you want to inspect.

### Read from another application

The token-authenticated financial API publishes accepted accounts, instruments, listings and
activity with stable identities, exact values and complete versioned snapshots. See the
[financial API contract](api/openapi/v1.yaml) and
[independent consumer check](.docs/engineering/development.md#api-consumer-acceptance).
For a running instance, use its endpoint, a private token file and an existing client-state directory:

```sh
mise run api-consumer:sync -- --endpoint http://127.0.0.1:8788 --token-file /absolute/private/client.token --state /absolute/private/client/mirror.db
```

The consumer validates a complete accepted snapshot and atomically saves its mirror
and version in a private SQLite database. Repeat the command to synchronize, or use
`api-consumer:follow` to synchronize when SSE announces publication changes.
Follow reconnects after stream interruptions; conditional reads skip unchanged snapshots.
A different warehouse installs a clearly reported new baseline.

An optional Activity Query ID on the same IBKR connection adds security transfers,
non-trade cash postings, and reported realized P&L and allocated cost for individual
activities. It shares the encrypted Flex token.
Inspect the values on activity detail pages and compare retained P&L evidence through
"Correct accepted data". See [IBKR setup](.docs/providers/ibkr-flex.md#optional-activity-query)
for query fields and reporting limits.

## Contribute

The Go module is `go.trippwill.dev/wake`. Start with [AGENTS.md](AGENTS.md), the
[documentation index](.docs/README.md) and [development guidance](.docs/engineering/development.md).
The [product brief](.docs/product/brief.md) owns Wake's north star, [current coverage](.docs/product/coverage.md)
describes supported behavior and limitations, and [DESIGN.md](DESIGN.md) owns visual design.
