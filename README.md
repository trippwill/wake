# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Each instance serves one user. The browser and financial API listen on IPv4
loopback. The operator provides any remote access and transport protection.

This pre-release build serves and processes retained history locally.
Inspect activity, instruments, and available reported positions in the admin
console. Wake retains original reports, exact financial values and conflicts.

### Set up and start

To build Wake from a Linux checkout:

```sh
mise install
mise run build:release
export PATH="$PWD/.cache/release:$PATH"
```

With `wake` on `PATH`, create the configuration and set a browser password:

```sh
wake config init
wake access set password
wake serve
```

Enter and confirm a password when prompted. Use it to sign in at the URL the
command prints. Stop the foreground daemon with Ctrl+C.

Wake selects `config.toml` through `--config`, `WAKE_CONFIG`, or
`${XDG_CONFIG_HOME:-$HOME/.config}/wake/config.toml` in that order. Use the same
file for initialization, serving, and recovery. `config init` creates a private
TOML and fails if the path already exists. Repeat `--set KEY=VALUE` to write
scalar settings listed by `wake config describe`.

```sh
wake config init --set admin.port=8887 --set api.port=8888
```

Omitted settings use the following defaults:

```toml
[storage]
warehouse_path = "wake.db"
connections_path = "connections.db"
sync_path = "sync.db"
unattended_upgrade = false

[credentials]
key_path = "credentials.key"

[paths]
# runtime_dir defaults to a directory under $XDG_RUNTIME_DIR/wake.
# Set a private local directory here if XDG_RUNTIME_DIR is unavailable.

[log]
level = "info"

[admin]
port = 8787
currency_refresh = false

[api]
port = 8788

[polling.defaults]
# Use the registered interval for each provider stream.
```

File and directory settings accept absolute paths or paths relative to the selected
TOML's directory. Wake keeps private browser and client authority in `auth.json` beside
that file. Keep a single TOML per instance directory. The default runtime directory is
scoped to the resolved instance directory.

Both listeners bind to `127.0.0.1` and use distinct ports. To access Wake through
an SSH tunnel, forward the admin port to the same local port on your browser's
machine and use the printed loopback URL.

Omit `--generate-key` to create only the TOML. To provision the key for an
existing TOML or retry a failed key setup, stop Wake and run
`wake access provision credential-key`. Both provisioning paths create a private
key at `credentials.key_path` if it is missing or reuse a valid existing key. Saved
credentials require their original key. For a custom key path, first create its
private parent directory. Set `paths.runtime_dir` if `XDG_RUNTIME_DIR` is
unavailable.

For an existing TOML, run `wake config check` to verify the launch settings.
`wake config get credentials.key_path` prints the expected key path. After
provisioning the key, run `wake config check --check-key` to verify the key
file's size, ownership, and permissions. Local startup does not read that key.
See [operator access and restore](.docs/engineering/architecture.md#operator-access-and-restore)
for the scope of each file.

### Inspect retained history

**Overview** shows local setup and financial review. Use **Activity**,
**Instruments**, **Positions** and **Needs review** to inspect reports and
unresolved questions. Activity, instrument and evidence pages retain a return
path to the originating view within the current tab. Positions shows effective
dates, exact quantities, contract terms and optional reported cost basis. An
empty reported set and unavailable positions have distinct states.

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
units and exact bytes. The measurement covers the warehouse file.
**System → Configuration** identifies the TOML file loaded at startup, when present,
alongside the effective storage and runtime paths.

The admin console requires JavaScript. Activity, instrument, positions and observation tables
load authenticated JSON, with filtering, exact ordering and pagination performed on
the server at 50 rows per page by default. URLs retain filters and page state;
Tabulator retains column preferences. Accepted detail pages offer an explicit refresh
action, while retained observations update independently. Correction drafts survive
validation errors, and changed data requires review before resubmission.

The daemon writes structured operational logs to stderr, including completed work,
failures and recovery actions. Startup reports both listeners. Set `[log] level = "debug"`
in the TOML for stage, validation, pacing, and cache detail.

For a routine backup, run `wake storage snapshot warehouse history.tar` while
Wake runs. To restore, stop Wake, select a configuration with a vacant warehouse
and sidecar path, then run `wake storage restore history.tar`. Start Wake with
local access set up to read the retained history. Restore preserves resource IDs
and gives API consumers a fresh publication baseline.

Use a compatible build for older formats or operational maintenance with saved
credentials. With Wake stopped, `wake storage upgrade` checks the financial format.

To clear financial history or encrypted source access, stop Wake and preview
the files selected by the reset:

```sh
wake storage reset warehouse
wake storage reset warehouse --confirm
```

Choose `credentials` or `warehouse`. Reset is a dry run unless `--confirm` is present
and works with corrupt or unsupported databases. Preserve any files you need before
confirming.

### Find help

```sh
wake help
wake help config init
wake help access provision credential-key
wake config describe
wake config check
```

Use command help for exact options.

Use **System → Settings → Change password** to replace the current password.
If you forget it, run `wake access set password` on the Wake host. The terminal
prompt hides your input. Setting a password from the CLI signs out browser
sessions and preserves API grants. Run `wake access revoke sessions` to sign out
all browsers while keeping the password.

### Read financial data from the CLI

Create a named token in **System → API clients** after signing in.
Save it in a private token file on the client with mode `0600`.

```sh
export WAKE_ENDPOINT=http://127.0.0.1:8788
export WAKE_TOKEN_FILE="$HOME/.config/wake/laptop.token"
wake --json api check
wake --json accounts list
wake --json accounts get "$ACCOUNT_ID"
wake --json activity query --account "$ACCOUNT_ID" --from 2026-10-01 --to 2026-10-31 --family trade
wake --json positions get "$ACCOUNT_ID"
wake --json api get '/v1/activity/ACTIVITY_ID'
```

Set `ACCOUNT_ID` to an ID from `resources[].id` in the accounts list.
For a different endpoint or credential, pass `--endpoint http://127.0.0.1:9788`
or `--token-file /absolute/client.token` on the leaf command before any positional
argument. Flags override `WAKE_ENDPOINT` and `WAKE_TOKEN_FILE`; the default endpoint
is `http://127.0.0.1:8788`. Client reads use those settings independently of daemon
configuration. Tokens grant financial reads and do not grant browser administration.
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

An optional Activity Query ID on the same IBKR connection adds security transfers,
non-trade cash postings, and reported realized P&L and allocated cost for individual
activities. It shares the encrypted Flex token.
Inspect the values on activity detail pages and compare retained P&L evidence through
"Correct accepted data". See [IBKR setup](.docs/providers/ibkr-flex.md#optional-activity-query)
for query fields and reporting limits.

## Contribute

Start in [GitHub Discussions](https://github.com/trippwill/wake/discussions) to ask
questions, propose changes, or offer to help. See [Contributing to Wake](CONTRIBUTING.md)
for contributor access and the issue and pull request workflow.
For suspected vulnerabilities, follow the [security policy](SECURITY.md).

The Go module is `go.trippwill.dev/wake`. Start with [AGENTS.md](AGENTS.md), the
[documentation index](.docs/README.md) and [development guidance](.docs/engineering/development.md).
The [product brief](.docs/product/brief.md) owns Wake's north star, [current coverage](.docs/product/coverage.md)
describes supported behavior and limitations, and [DESIGN.md](DESIGN.md) owns visual design.
