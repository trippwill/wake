# Operate the native command

For a fresh installation, follow [Get started](../get-started.md#install-the-native-command).
If you run Wake with Podman, follow the [appliance guide](wake-appliance.md).

## Select a configuration

Wake selects `config.toml` through `--config`, `WAKE_CONFIG`, or
`${XDG_CONFIG_HOME:-$HOME/.config}/wake/config.toml` in that order. Use the same
file for initialization, serving, and recovery. `config init` creates a private
TOML and fails if the path already exists. Repeat `--set KEY=VALUE` to write
scalar settings listed by `wake config describe`.

```sh
wake config init --generate-key --set admin.port=8887 --set api.port=8888
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

Set `paths.runtime_dir` to a private local directory if `XDG_RUNTIME_DIR` is
unavailable. Run `wake config check` to check the selected launch settings.
Use `wake help` and `wake help <command>` for exact command options.

## Provision a credential key

Omit `--generate-key` to create only the TOML. To provision the key for an
existing TOML or retry a failed key setup, stop Wake and run
`wake access provision credential-key`. Both provisioning paths create a private
key at `credentials.key_path` if it is missing or reuse a valid existing key. Saved
credentials require their original key. For a custom key path, first create its
private parent directory.

`wake config get credentials.key_path` prints the expected key path. After
provisioning the key, run `wake config check --check-key` to verify the key
file's size, ownership, and permissions.

## Choose the browser origin

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

## Access Wake on your LAN

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

Keep the SSH session open while using Wake. For a Podman deployment, see
[Access the appliance on your LAN](wake-appliance.md#access-the-appliance-on-your-lan).

## Manage sign-in and recover access

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

## Check the logs

The daemon writes structured operational logs to stderr, including completed work,
failures and recovery actions. Startup reports both listeners. Set `[log] level = "debug"`
in the TOML for stage, validation, pacing, and cache detail.

## Back up or restore Wake

Use a warehouse snapshot to save committed financial history while Wake runs:

```sh
wake storage snapshot history.tar
```

To recover the history, stop Wake and select a configuration with a vacant
financial database path. Run `wake storage restore financial history.tar` with
that configuration. Set up local browser access before starting a new
installation. Restored history keeps financial resource IDs and starts a
fresh publication baseline for API clients. Sessions, client grants, and
source access files live outside the warehouse snapshot.

For a full installation backup, stop Wake before running:

```sh
wake storage backup installation.tar
```

Protect this archive and keep the credential key separately. Restore with
`wake storage restore installation installation.tar` while Wake is stopped,
using the matching credential key if the backup contains encrypted credentials.
Use `--replace` only when replacing an existing installation's databases.
Wake preserves the originals alongside their installed paths and keeps the
current local authentication. In a new installation, set up browser access and
enroll API clients separately.

Restored synchronization starts paused. In **Connections**, use **Review
recovery steps** where offered. Review the account, stream, date range, and
earliest request before approving each restart.

## Update the native executable

1. Stop Wake and [back up the installation](#back-up-or-restore-wake).
2. Install the new standalone executable.
3. Run the new executable's `wake storage upgrade` using the existing
   configuration to check the financial format.
4. Start the new executable with that configuration.

## Recover missing synchronization storage

For an RC1 installation that has only its financial database, stop Wake and
run `wake storage recover-sync` with the existing configuration. Optionally
supply an installation backup from the same financial history to propose
source settings. Review the preview and rerun with the same arguments plus
`--confirm <plan-id>`. After startup, use **Review recovery steps** in
**Connections** to review and approve source restarts.

## Reset a store

To clear financial history or encrypted source access, stop Wake and preview
the files selected by the reset:

```sh
wake storage reset financial
wake storage reset financial --confirm
```

Choose `credentials` or `financial`. Reset is a dry run unless `--confirm` is present
and works with corrupt or unsupported databases. Reset refuses unresolved SQLite
journals. Preserve any files you need before confirming.
