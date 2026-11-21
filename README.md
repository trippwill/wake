# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Each instance serves one user. The browser and financial API listen on IPv4
loopback. Use Wake on the same machine or over SSH forwarding on a private LAN.

The installation examples pin v0.1.0-rc.1. This release candidate serves and
processes retained history locally and sets the first
[compatibility baseline](.docs/product/coverage.md#compatibility).
Inspect activity, instruments, and available reported positions in the admin
console. Wake retains original reports, exact financial values and conflicts.

### Install the appliance

Follow the [rootless Podman quick start](docs/deployment/wake-appliance.md#rootless-quick-start)
on a Linux host with systemd and Podman 5.6 or newer. Wake keeps the complete installation
in `~/.local/share/wake` by default. You can choose another host directory before the first start.
Once Wake starts, set the browser password in the running container.

### Install and run the native command

To install the release candidate with Go, run:

```sh
GOBIN="$HOME/.local/bin" go install go.trippwill.dev/wake@v0.1.0-rc.1
export PATH="$HOME/.local/bin:$PATH"
```

For the packaged release on Linux amd64 or arm64, run these commands in an
empty directory to verify and install the executable:

```sh
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo "Unsupported architecture" >&2; exit 1 ;;
esac
release=https://github.com/trippwill/wake/releases/download/v0.1.0-rc.1
curl -fLO "$release/wake-linux-$arch.tar.gz" &&
  curl -fLO "$release/SHA256SUMS" &&
  sha256sum --check --ignore-missing SHA256SUMS &&
  tar -xzf "wake-linux-$arch.tar.gz" wake &&
  install -Dm755 wake "$HOME/.local/bin/wake"
export PATH="$HOME/.local/bin:$PATH"
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
financial_path = "wake.db"
credentials_path = "connections.db"
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

#### Choose the browser origin

Both listeners bind to `127.0.0.1` and use distinct ports. Open
<http://wake.localhost:8787> for the browser at the default admin port. Browsers
share Wake's session cookie across ports under the same hostname. The dedicated
name separates it from other local apps you open at `localhost` or `127.0.0.1`
and gives Wake its own passkey scope. Reserve `wake.localhost` for Wake.

If the name fails to resolve to loopback in your browser, set an explicit
origin in the TOML and restart Wake. For local password sign-in at the default
admin port:

```toml
[admin]
allowed_origins = ["http://127.0.0.1:8787"]
```

For SSH access across your private LAN, put the browser's tunnel port in the
origin. To use port 18787 on your laptop while Wake listens on port 8787 on the
LAN host, configure that host with `allowed_origins = ["http://wake.localhost:18787"]` and run this
command on your laptop:

```sh
ssh -N -L 127.0.0.1:18787:127.0.0.1:8787 user@wake-host
```

Open <http://wake.localhost:18787> and sign in. The list replaces the default.
Include each browser origin you use, with its scheme and port. The first entry
supplies the printed sign-in URL.

For financial clients, an optional host list restricts the HTTP Host header.
For example, set `allowed_hosts = ["localhost:18788"]` under `[api]` and add
`-L 127.0.0.1:18788:127.0.0.1:8788` to the same SSH command. Set your
client endpoint to `http://localhost:18788`. Each entry is a hostname or IP
address with its optional port. Omitting the list accepts any Host header.
Clients still present their own bearer token.

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

Open **Overview** to see which source or account needs attention and what Wake
last received. The latest report shows its receipt time and reporting period.
Check each stream’s last successful retrieval and next attempt or retry. Follow
its repair link if access needs attention. **Request sync** brings eligible work
forward. **Reports** opens the receipt history across sources.

Open a report and select an entry to read the source values alongside its
relationship to a current record. Follow the related activity to inspect its
financial terms. The reader pairs comparable reported and accepted values, keeping quantity
units and currencies with their amounts. Where Wake can identify the report behind the price, follow
the price back to the selected entry. The source stays available after a
correction. Use **Read the full original report** to inspect the retained content.
A repeated entry can point to an existing trade, and some entries
have no settled match. A trade’s source reports show the appearances behind it.
For a Treasury bill, compare the face amount, quoted price per 100 of face value
and principal reported by the source.

Use **Activity**, **Instruments**, **Positions** and **Needs review** for direct
financial browsing. Positions shows the account's reporting time, quantities,
contract terms and available cost basis. An empty reported set and unavailable
positions have distinct states. Use the source's reports when the available
link applies to the account as a whole.

Use **Correct accepted data** or **Correct instrument data** to correct a field/link,
compare retained evidence or manage decisions. The editor shows the current value;
dates, choices and links use field-specific controls, and edits receive live checks.
Search compatible retained records when correcting a link. Comparisons lead with
differences. Decisions retain the authenticated session, time
and optional rationale. Release a decision to return to automatic selection.

Use **Connections → select a source → Reports** to retry a report or inspect the
source-specific processing details.

**System → Status** shows the current financial database file size in readable
units and exact bytes. The measurement covers the warehouse file.
**System → Configuration** identifies the TOML file loaded at startup, when present,
alongside the effective storage and runtime paths.

In **System → Settings → Appearance**, choose **Steel & Copper** or **Source Reader**.
**Mode** selects System, Light or Dark. **Typography** follows the theme by default
or can keep System sans or Editorial serif across themes. Changes take effect
immediately and are remembered in this browser, including on the sign-in page.

The admin console requires JavaScript. Activity, instrument, positions and observation tables
load authenticated JSON, with filtering, exact ordering and pagination performed on
the server at 50 rows per page by default. URLs retain filters and page state;
Tabulator retains column preferences. Accepted detail pages offer an explicit refresh
action, while retained observations update independently. Correction drafts survive
validation errors, and changed data requires review before resubmission.

The daemon writes structured operational logs to stderr, including completed work,
failures and recovery actions. Startup reports both listeners. Set `[log] level = "debug"`
in the TOML for stage, validation, pacing, and cache detail.

Run `wake storage snapshot history.tar` to save the financial database while
Wake runs. To restore, stop Wake, select a configuration with a vacant financial
database path, then run `wake storage restore financial history.tar`. Start Wake with
local access set up to read the retained history. Restore preserves resource IDs
and gives API consumers a fresh publication baseline.

With Wake stopped, `wake storage backup installation.tar` saves a complete
installation. Keep the credential key separately and use
`wake storage restore installation installation.tar` to restore the backup.
See [backup and recovery](.docs/product/coverage.md#backup-and-recovery) for
replacement and source review. When updating a native installation, stop Wake,
back up the installation, and run the new executable's `wake storage upgrade`
with the existing configuration to check its financial format. Start the new
executable with the same configuration.

To clear financial history or encrypted source access, stop Wake and preview
the files selected by the reset:

```sh
wake storage reset financial
wake storage reset financial --confirm
```

Choose `credentials` or `financial`. Reset is a dry run unless `--confirm` is present
and works with corrupt or unsupported databases. Reset refuses unresolved SQLite
journals. Preserve any files you need before confirming.

### Find help

```sh
wake help
wake help config init
wake help access provision credential-key
wake config describe
wake config check
```

Use command help for exact options.

After signing in with the initial password, open **System → Settings → Browser access**
and choose **Add a passkey**. Confirm your password, then save the passkey with your
browser, password manager, or security key. You can label it and enroll a backup
using either your current password or an existing passkey.

Use the [default loopback hostname](#choose-the-browser-origin) for passkeys.
Wake scopes a passkey to the hostname you use at enrollment. It works on each
allowed port for that hostname and installation. If you move from `localhost`
to `wake.localhost`, use password sign-in to enroll a passkey on the new name.
If password sign-in is disabled, restore it before switching. If your password
manager declines the HTTP loopback origin, choose a FIDO security key in the
browser prompt. At a plain HTTP IP address, use password sign-in.

To use passkeys exclusively, choose **Disable password sign-in** and approve a
fresh passkey prompt. You can set a password again in Settings with a passkey,
or run `wake access set password` on the host at any time. Host recovery signs
out browser sessions and preserves enrolled passkeys and financial API grants.
Run `wake access revoke sessions` to sign out all browsers.

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

Account lists return `baseline_id`, `version`, and `resources`. Activity
queries read a complete snapshot for each page and filter it locally. Dates are
inclusive. Results default to accepted activity, newest civil date first, with
up to 100 results per page. For example, a page can return
`{"baseline_id":"baseline","version":"version","resources":[],"next_offset":null}`.
When `next_offset` is an integer, pass it as `--offset` with the same filters.
Compare `baseline_id` and `version` across pages when consistency matters.
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
activity with stable identities, exact values and versioned snapshots. See the
[financial API contract](api/openapi/v1.yaml) and
[independent consumer check](.docs/engineering/development.md#api-consumer-acceptance).
Clients receive changes from a retained baseline or a full snapshot when needed.

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
