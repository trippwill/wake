# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Wake acquires IBKR Flex reports, cached SnapTrade Personal trade history, and
reported Fidelity US positions through SnapTrade.
Confirm one account per connection and inspect activity, instruments and available
reported positions in the admin console. Wake retains original reports, exact
financial values and conflicts.

### Set up and start

From a Linux source checkout:

```sh
mise install
mise run bin -- setup
mise run serve
```

Follow setup's prompts for local paths and a browser unlock. Keep the generated
credential key. Open the local admin address selected during setup and sign in.
Keep the terminal running; stop Wake with Ctrl+C.

For a separate configuration, pass the same absolute path to both commands:

```sh
mise run bin -- setup --config /absolute/private/path/config.toml
mise run bin -- serve --config /absolute/private/path/config.toml
```

If startup reports an unsupported development store, stop Wake and preserve a
backup of its stores and sidecars. Explicitly move the profile aside or select
a fresh warehouse path before restarting. Keep the encrypted access store to reuse
credentials. The new warehouse establishes new financial IDs. The old warehouse
holds its original evidence and corrections. Startup preserves unsupported data. Use current
settings from `config describe`; retired acquisition settings are rejected.

#### Run as a Linux user service

On Linux with a systemd user session, install Wake as the account that runs it.
From a source checkout, build the standalone binary with `mise install` and
`mise run build:release`:

```sh
sh .packaging/install.sh .cache/release/wake
```

You can also copy the installer and a prebuilt Wake binary to the target machine
and pass that binary's path to the installer. The installer puts `wake` in
`~/.local/bin` and `wake.service` in the user's XDG data directory.

For a fresh profile, run setup and then enable and start the service:

```sh
"$HOME/.local/bin/wake" setup --config "${XDG_CONFIG_HOME:-$HOME/.config}/wake/config.toml"
systemctl --user enable --now wake.service
```

Use the same XDG paths in your shell and systemd user manager. To move an existing
profile to the service, stop the foreground daemon and reuse its configuration.
Carry any command-line or shell-only path overrides into the TOML file. If the
file is at a different path, run `systemctl --user edit wake.service` and add
this drop-in with your absolute path before enabling the service:

```ini
[Service]
Environment="WAKE_CONFIG=/absolute/private/path/config.toml"
```

Check the service and find the admin address in its startup log:

```sh
systemctl --user status wake.service
journalctl --user -u wake.service -f
```

After reinstalling, run `systemctl --user daemon-reload` and
`systemctl --user restart wake.service` to load the update. The enabled service
starts with your user manager, normally at login. Run `loginctl enable-linger`
if you want it to start at boot and keep running after logout.

#### Run on a Linux server with Podman

Use the [Podman setup guide](.docs/engineering/podman.md) to start Wake at boot
and reach browser admin and the financial API through one saved SSH profile.

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

The two `[storage]` file settings are `warehouse_path` for financial history and
operational state, and `connections_path` for active access and encrypted credentials.
Defaults use the XDG data and config locations respectively. Override files in
TOML, with `--warehouse-path` or `--connections-path` on `serve`, or through the
corresponding `WAKE_*_PATH` environment variables.

If the access store is lost, Wake recreates it and pauses upstream work. Retained
history stays inspectable. In **Connections → Recovery**, follow the ordered
source-access and reference review steps, then select **Resume upstream work**.
An ordinary restart with intact stores keeps the existing authorization.

The admin console requires JavaScript. Activity, instrument, positions and observation tables
load authenticated JSON, with filtering, exact ordering and pagination performed on
the server at 50 rows per page by default. URLs retain filters and page state;
Tabulator retains column preferences. Accepted detail pages offer an explicit refresh
action, while retained observations update independently. Correction drafts survive
validation errors, and changed data requires review before resubmission.

The daemon writes structured operational logs to stderr, including completed work,
failures and recovery actions. Startup always reports the admin binding and port,
or reports that admin is disabled. Use `--log-level debug` with `serve` for stage,
validation, pacing and cache detail.

For a supported storage upgrade, stop Wake and run `wake upgrade` with the new
binary. Review the plan and any rule release notes, run the confirmation command
it prints, then start Wake. See [forward upgrades](.docs/product/coverage.md#forward-upgrades)
for unattended upgrades. Run `wake backup` with Wake stopped to make a paired
SQLite snapshot archive. See [backup and recovery](.docs/engineering/recovery.md)
for restore and source access review.

To clear credentials or financial history, stop Wake and preview the configured files:

```sh
mise run bin -- reset warehouse
mise run bin -- reset warehouse --confirm
```

Choose `credentials` or `warehouse`. Reset is a dry run unless `--confirm` is present,
and works with corrupt or unsupported databases. A warehouse reset also clears its
operational state and retains credentials. Start Wake, select **Resume upstream
work**, then confirm the rediscovered accounts. A credentials reset retains financial
history; repair access in **Connections** and resume. Configuration, keys and browser
access are retained. See the
[development guide](.docs/engineering/development.md#workspaces-and-tools) for details.

### Find help

```sh
mise run bin -- help
mise run bin -- help setup
mise run bin -- config describe
mise run bin -- config check
```

Use command help for exact options.

If you already have a prepared configuration, `wake setup --use-config` reviews
its launch settings and prompts for the operator unlock. Use `--config` to
select a file outside the default location.

To change a forgotten browser unlock, stop Wake and rerun setup with the same
configuration, then restart. Setup retains the credential key; keep that key safe.

### Read financial data from the CLI

Run these commands as the same Unix user as the Wake daemon. From a source
checkout, run `mise install` and `mise run build:release`, then put
`.cache/release/wake` on your PATH. The [Linux user service installer](#run-as-a-linux-user-service)
also installs the binary. Once `wake` is on PATH, the read commands work from any
directory.

```sh
wake --json doctor
wake --json accounts list
wake --json accounts get "$ACCOUNT_ID"
wake --json activity query --account "$ACCOUNT_ID" --from 2026-10-01 --to 2026-10-31 --family trade
wake --json positions get "$ACCOUNT_ID"
wake --json request get '/v1/activity/ACTIVITY_ID'
```

Set `ACCOUNT_ID` to an ID from `resources[].id` in the accounts list.
The private socket requires no token or browser login. For a different instance,
pass `--config /absolute/config.toml` or `--socket /absolute/wake.sock` on the
leaf command, before any positional argument. Profile selection uses `--config`,
`WAKE_CONFIG`, then the default Wake configuration file. The socket is
`wake.sock` inside the runtime directory selected by `WAKE_RUNTIME_DIR`,
`paths.runtime_dir`, or `$XDG_RUNTIME_DIR/wake`, in that order.

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
redirects within the same resource kind on the configured socket. To explain a
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

The private Unix socket publishes accepted accounts, instruments, listings and
activity with stable identities, exact values and complete versioned snapshots. See the
[financial API contract](api/openapi/v1.yaml) and
[independent consumer check](.docs/engineering/development.md#socket-consumer-acceptance).
For a running instance, use its socket path and an existing client-state directory:

```sh
mise run socket-consumer:sync -- --socket /absolute/private/runtime/wake.sock --state /absolute/private/client/mirror.db
```

The consumer validates a complete accepted snapshot and atomically saves its mirror
and version in a private SQLite database. Repeat the command to synchronize, or use
`socket-consumer:follow` to synchronize when SSE announces publication changes.
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
