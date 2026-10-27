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

Open **Connections**, enter a connection name, the saved Confirmation Query ID
and the Flex Web Service token. See [IBKR setup](docs/providers/ibkr-flex.md) for
query preparation. Wake encrypts the token and performs one bounded discovery
attempt. It shows the available accounts and any missing profile information.

Select **Confirm account**, enter a display name, and confirm the available
details. Leave an unknown account currency empty. Confirmation activates one
account and starts automatic synchronization; other discovered accounts remain
candidates.

Open **Activity** to inspect security executions, option outcomes, currency
conversions, instruments and their reference status. Execution and instrument pages link to retained evidence when needed.
The pages state the processing coverage; unsupported rows remain in the reports.
Status regions refresh automatically while preserving unfinished forms.

Connections also provides token replacement, Query ID repair and explicit retry.
Its optional OpenFIGI setting uses an encrypted key independently of IBKR access.
A rejected configured key stays visible until repaired or explicitly cleared to
select anonymous access. Reference failures leave provisional financial views
available.

Use **Correct accepted data** on an activity or **Correct instrument data** on an
instrument to retain a field, link, naming or candidate-selection decision. The form
shows its target and revision. Decision history records the authenticated session,
time and optional rationale; supersede a scope with another decision or release it
to return to automatic selection. Source reports remain available for inspection.

Connections provides **Retry processing report** and **Rebuild accepted views**.
Recovery uses retained inputs and decisions; progress resumes after restart without
resetting identities, reference caches or retry eligibility. A failed rebuild keeps
committed progress and offers retry after the problem is repaired.

Each store has its own file setting under `[storage]`: `warehouse_path` for financial
history, `connections_path` for active access and encrypted credentials, and
`operational_path` for execution state. Defaults use the XDG data, config and state
locations respectively. Override individual files in TOML, with `--warehouse-path`,
`--connections-path` or `--operational-path` on `serve`, or through the corresponding
`WAKE_*_PATH` environment variables.

If a companion store is lost, Wake recreates it and pauses upstream work. Retained
history stays inspectable. In **Connections**, repair missing settings and tokens,
review the OpenFIGI setting when requested, then select **Resume upstream work**.
An ordinary restart with intact stores keeps the existing authorization.

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

## Contribute

The Go module is `go.trippwill.dev/wake`. Start with [AGENTS.md](AGENTS.md), the
[documentation index](docs/README.md) and [development guidance](docs/engineering/development.md).
The [product brief](docs/product/brief.md) owns Wake's north star, [current coverage](docs/product/coverage.md)
describes supported behavior and limitations, and [DESIGN.md](DESIGN.md) owns visual design.
