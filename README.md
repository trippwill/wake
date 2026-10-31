# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Wake connects one IBKR account, automatically acquires Confirmation reports, and
shows security executions, option outcomes and reported FX conversions in the
admin console. It retains original reports, exact financial values, provisional
instrument identity and conflicts.

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
fresh database paths before restarting. Startup preserves unsupported data. Use current
settings from `config describe`; retired acquisition settings are rejected.

### Connect and inspect

Open **Connections → Add source connection**, enter a connection name, the saved
Confirmation Query ID and the Flex Web Service token. See [IBKR setup](docs/providers/ibkr-flex.md) for
query preparation. Wake encrypts the token and performs one bounded discovery
attempt. It shows the available accounts and any missing profile information.

Select **Confirm account**, enter a display name, and confirm the available
details. Leave an unknown account currency empty. Confirmation activates one
account and starts automatic synchronization; other discovered accounts remain
candidates.

**Overview** shows setup steps, financial review and source work. In **Activity**,
use **All activity**, **Needs review** or **Instruments** to inspect the records you
need. Activity, instrument and evidence pages retain a return path to the originating
view. Unresolved financial and identity conflicts remain distinct from ordinary
reference gaps and source failures.

**Connections** groups each source's **Status**, **Reports** and **Access**. Repair
its Query ID or token beside the affected source. **Instrument reference** manages
the optional encrypted OpenFIGI key independently of IBKR access. A rejected key
stays visible until repaired or explicitly cleared for anonymous access.

Use **Correct accepted data** or **Correct instrument data** to correct a field/link,
compare retained evidence or manage decisions. The editor shows the current value;
dates, choices and links use field-specific controls, and edits receive live checks.
Search compatible retained records when correcting a link. Comparisons lead with
differences. Decisions retain the authenticated session, time
and optional rationale. Release a decision to return to automatic selection.

A source's **Reports** view provides **Retry processing report**. **System →
Maintenance** provides **Rebuild accepted views** and recovery history. Rebuild uses
retained inputs and decisions and resumes from committed progress after restart.

The two `[storage]` file settings are `warehouse_path` for financial history and
operational state, and `connections_path` for active access and encrypted credentials.
Defaults use the XDG data and config locations respectively. Override files in
TOML, with `--warehouse-path` or `--connections-path` on `serve`, or through the
corresponding `WAKE_*_PATH` environment variables.

If the access store is lost, Wake recreates it and pauses upstream work. Retained
history stays inspectable. In **Connections → Recovery**, follow the ordered
source-access and reference review steps, then select **Resume upstream work**.
An ordinary restart with intact stores keeps the existing authorization.

Activity search and filters select the displayed records. Enhanced tables provide
exact sorting and pagination and retain their view through updates; native tables
remain readable without JavaScript. Correction drafts survive validation errors,
and changed data requires review before resubmission.

The daemon writes structured operational logs to stderr, including completed work,
failures and recovery actions. Startup always reports the admin binding and port,
or reports that admin is disabled. Use `--log-level debug` with `serve` for stage,
validation, pacing and cache detail.

To clear credentials or financial history, stop Wake and preview the configured files:

```sh
mise run bin -- reset warehouse
mise run bin -- reset warehouse --confirm
```

Choose `credentials` or `warehouse`. Reset is a dry run unless `--confirm` is present,
and works with corrupt or unsupported databases. A warehouse reset also clears its
operational state and retains credentials. Start Wake, select **Resume upstream
work**, then confirm the rediscovered account. A credentials reset retains financial
history; repair access in **Connections** and resume. Configuration, keys and browser
access are retained. See the
[development guide](docs/engineering/development.md#workspaces-and-tools) for details.

### Find help

```sh
mise run bin -- help
mise run bin -- help setup
mise run bin -- config describe
mise run bin -- config check
```

Use command help for exact options.

To change a forgotten browser unlock, stop Wake and rerun setup with the same
configuration, then restart. Setup retains the credential key; keep that key safe.

### Read from another application

The private Unix socket publishes accepted accounts, instruments, listings and
activity with stable identities, exact values and resumable changes. See the
[financial API contract](api/openapi/v1.yaml) and
[standalone consumer scenarios](docs/engineering/development.md#socket-consumer-acceptance).
For a running instance, use its socket path and an existing client-state directory:

```sh
mise run socket-consumer:sync -- --socket /absolute/private/runtime/wake.sock --state /absolute/private/client/mirror.json
```

The consumer saves a durable mirror and cursor. Repeat the command to resume, or
use `socket-consumer:follow` to keep reading. Rebuild explicitly starts a new baseline;
ordinary daemon restarts and companion-store recovery preserve the cursor.


## Contribute

The Go module is `go.trippwill.dev/wake`. Start with [AGENTS.md](AGENTS.md), the
[documentation index](docs/README.md) and [development guidance](docs/engineering/development.md).
The [product brief](docs/product/brief.md) owns Wake's north star, [current coverage](docs/product/coverage.md)
describes supported behavior and limitations, and [DESIGN.md](DESIGN.md) owns visual design.
