# Connect an app to Wake

## Create a client token

Sign in to Wake, open **System → API clients**, enter a name under **Add a
client**, and choose **Create token**. Copy the token while **Save this token**
is open. For a CLI client, create a private file and paste the token as its only
line. For example, on a Linux client, create a new file for this client:

```sh
install -d -m 0700 "$HOME/.config/wake"
(umask 077; set -C; : > "$HOME/.config/wake/laptop.token")
```

Open that file in a local editor and paste the token. Keep it readable and
writable only by your user (mode `0600`). If the file already exists, choose a
new name or update the intended client's existing file.

## Read financial data from the CLI

```sh
export WAKE_ENDPOINT=http://127.0.0.1:8788
export WAKE_TOKEN_FILE="$HOME/.config/wake/laptop.token"
wake --json api check
wake --json accounts list
```

Set `ACCOUNT_ID` to an ID from `resources[].id` in the accounts list, then run:

```sh
wake --json accounts get "$ACCOUNT_ID"
wake --json activity query --account "$ACCOUNT_ID" --from 2026-10-01 --to 2026-10-31 --family trade
wake --json positions get "$ACCOUNT_ID"
```

For a different endpoint or credential, pass `--endpoint http://127.0.0.1:9788`
or `--token-file /absolute/client.token` on the leaf command before any positional
argument. Flags override `WAKE_ENDPOINT` and `WAKE_TOKEN_FILE`. The default endpoint
is `http://127.0.0.1:8788`. Client reads use those settings independently of daemon
configuration. Tokens grant financial reads and do not grant browser administration.
Use **Revoke access** in **System → API clients** for a lost or retired token.

Account lists return `baseline_id`, `version`, and `resources`. Activity
queries read a complete snapshot for each page and filter it locally. Dates are
inclusive. Results default to accepted activity, newest civil date first, with
up to 100 results per page. For example, a page can return
`{"baseline_id":"baseline","version":"version","resources":[],"next_offset":null}`.
When `next_offset` is an integer, pass it as `--offset` with the same filters.
Compare `baseline_id` and `version` across pages when consistency matters.
Run `wake activity query --help` for the state, family, and limit options.

Exact account and positions reads and raw GETs return the API JSON object.
Resource lookups follow up to five identity redirects within the same resource
kind at the configured origin. To explain a resource, use the ID and revision
from its current lookup. Position views distinguish
an available empty array from an unavailable view with `positions: null` and a reason.
Successful commands write JSON to stdout. With `--json`, errors go to stderr
and preserve recognized API codes, as in
`{"event":"error","code":"not_found","message":"Wake API returned HTTP 404","exit_status":1}`.

## Install the Codex skill

Copy the wake-read skill from a Wake checkout into your Codex skills directory.
For a new installation, run from the checkout root with `CODEX_HOME` set if you
use a custom location.

```sh
codex_skills="${CODEX_HOME:-$HOME/.codex}/skills"
mkdir -p "$codex_skills"
cp -R .agents/skills/wake-read "$codex_skills/"
```

Start a Codex chat and invoke `$wake-read` with the Wake data you want to inspect.

## Read from another application

The read-only financial API publishes accepted accounts, instruments, listings,
activity, and reported positions. Versioned snapshots and change notifications
let a client follow updates, with a fresh snapshot when the requested baseline
is unavailable.

Use the [v0.1.0 OpenAPI contract](https://github.com/trippwill/wake/blob/v0.1.0/api/openapi/v1.yaml)
for endpoints and schemas. For access from another machine on your private LAN,
use [SSH forwarding](deployment/native.md#access-wake-on-your-lan).
