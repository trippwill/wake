# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Wake connects one IBKR account, automatically acquires Confirmation reports, and
shows exchange equity/ETF executions in the admin console. It retains original
reports, exact financial values, provisional instrument identity and conflicts.

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
backup of its database and sidecars. Explicitly move them aside or select a fresh
data directory before restarting. Startup preserves unsupported data. Use current
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

Open **Activity** to inspect equity executions, instruments and their reference
status. Execution and instrument pages link to retained evidence when needed.
The pages state the processing coverage; unsupported rows remain in the reports.
Status regions refresh automatically while preserving unfinished forms.

Connections also provides token replacement, Query ID repair and explicit retry.
Its optional OpenFIGI setting uses an encrypted key independently of IBKR access.
A rejected configured key stays visible until repaired or explicitly cleared to
select anonymous access. Reference failures leave provisional financial views
available.

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
