# Deploy the Wake appliance

## Contents

- [Rootless quick start](#rootless-quick-start)
- [Optional user lingering](#optional-user-lingering)
- [Custom storage](#custom-storage)
- [Access the appliance on your LAN](#access-the-appliance-on-your-lan)
- [Use an untagged commit or branch](#use-an-untagged-commit-or-branch)
- [Rootful Podman option](#rootful-podman-option)
- [Logs and snapshots](#logs-and-snapshots)
- [Restore a warehouse snapshot](#restore-a-warehouse-snapshot)
- [Update the appliance](#update-the-appliance)
- [Move or back up a stopped installation](#move-or-back-up-a-stopped-installation)
- [Troubleshoot startup](#troubleshoot-startup)

## Rootless quick start

Use rootless Podman on a Linux host with systemd and `podman quadlet install`
(Podman 5.6 or newer). Run the following commands as the user who will own Wake.
The image includes Wake and its runtime dependencies; no checkout or host Wake
executable is needed.

These examples install v0.1.0-rc.2, which preserves the RC1
[compatibility baseline](../../.docs/product/coverage.md#compatibility).
The default installation keeps all persistent files in `~/.local/share/wake`.
For another host directory, complete [Custom storage](#custom-storage) first and
continue at **Install and start Wake** below. For the default location, create its directory.

```sh
install -d -m 0700 "$HOME/.local/share/wake"
```

**Install and start Wake**, then set the browser password when prompted.

```sh
podman quadlet install "https://raw.githubusercontent.com/trippwill/wake/v0.1.0-rc.2/.container/wake.container"
systemctl --user start wake
podman exec -it wake wake access set password
```

[Quadlet installation](https://docs.podman.io/en/latest/markdown/podman-quadlet-install.1.html)
reloads the user service manager automatically. The Quadlet joins the user
manager's `default.target`, so there is no separate `systemctl enable` step.

On first launch, the image initializes the configuration and encryption key.
`systemctl --user start wake` waits for the browser to respond, including the
password-required page. Enter and confirm the password in the terminal, then
open <http://wake.localhost:8787>. To reset a forgotten password while Wake runs, use
the same `podman exec -it wake wake access set password` command. Existing browser
sessions are signed out; API client grants are preserved.
If needed, [set an explicit browser origin](../../README.md#choose-the-browser-origin)
in the appliance TOML and restart Wake.

| Under the chosen host directory | Container location | Contents |
| --- | --- | --- |
| `financial/` | `/data/financial/` | Financial warehouse and SQLite sidecars |
| `config/` | `/data/config/` | TOML, encryption key, browser and client authentication |
| `state/` | `/data/state/` | Connection and synchronization databases when used |
| `backups/` | `/data/backups/` | Requested snapshots and backups |

Runtime files live in an ephemeral `/run/wake` tmpfs. The image filesystem is
read-only. The image sets `WAKE_CONFIG=/data/config/config.toml`, so commands
inside the container use the same installation automatically. Subsequent starts
use the saved configuration and key. The appliance serves retained history with
the local processing capabilities described in [current coverage](../../.docs/product/coverage.md).

## Optional user lingering

If Wake needs to run before login or after you log out, consider enabling
lingering for the account that runs it. Lingering starts that account's systemd
user manager at boot and keeps it running after the last session ends. This
affects the whole account. Other user services and timers can also start before
login or continue after logout. Review those units with
`systemctl --user list-unit-files` and `systemctl --user list-timers --all` before
changing the setting.

Check the current setting with `loginctl show-user "$USER" --property=Linger`.
If you want this behavior for the account, run `loginctl enable-linger "$USER"`
on the relevant machine. Your host may require administrator authentication.
Use `loginctl disable-linger "$USER"` to reverse the account-wide change.

The same setting can keep the SSH tunnel running on the client. The tunnel still
needs SSH credentials available at boot if it starts before you log in.

For details, see systemd's [loginctl manual](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html)
and [user manager documentation](https://www.freedesktop.org/software/systemd/man/latest/user@.service.html).

## Custom storage

A Quadlet drop-in is a local file that overrides settings in the installed Quadlet.
Use one before first launch to mount your chosen directory at `/data`. The file
survives Quadlet updates.

The rootless Quadlet paths below use the default `~/.config/containers/systemd/`.
If you set `XDG_CONFIG_HOME`, use `$XDG_CONFIG_HOME/containers/systemd/` instead
for the Quadlet and its drop-ins.

For example, to keep Wake in `~/Private/Wake data`, create both directories.

```sh
install -d -m 0700 "$HOME/Private/Wake data"
mkdir -p ~/.config/containers/systemd/wake.container.d
```

Create `~/.config/containers/systemd/wake.container.d/storage.conf` with:

```ini
[Container]
Volume=
Volume=%h/Private/Wake data:/data:Z
```

Replace `%h/Private/Wake data` with your chosen host path. `%h` means your home
directory; an absolute path selects another disk or directory. Keep the value
unquoted, including spaces, and write `%%` for a literal `%` in a path. The empty
`Volume=` clears the default mapping. `:Z` gives the installation a private
SELinux label. Use a directory owned by your user; the rootless container creates
files with your host ownership.

## Access the appliance on your LAN

The container uses host networking. Browser administration listens on
`127.0.0.1:8787` and the financial API on `127.0.0.1:8788` on the Wake host.
On a client on the same private LAN, add this entry to `~/.ssh/config`. Set
`HostName` to the Wake host's LAN address and `User` to your SSH login:

```sshconfig
Host wake
    HostName wake-host
    User your-user
    LocalForward 127.0.0.1:8787 127.0.0.1:8787
    LocalForward 127.0.0.1:8788 127.0.0.1:8788
    ExitOnForwardFailure yes
    ServerAliveInterval 60
```

Before connecting for the first time, obtain the Wake host's SSH key
fingerprint from a trusted source, such as the host console. Compare it with
the fingerprint SSH presents before accepting the key.

Start both forwards with:

```sh
ssh -N wake
```

Keep that SSH connection running while using Wake; Ctrl+C closes both forwards.
Open <http://wake.localhost:8787> for the browser and use
<http://127.0.0.1:8788> as the financial API endpoint. Enroll an API token as
described in the [CLI guide](../../README.md#read-financial-data-from-the-cli).
If you change the client-side browser port, set the matching origin under
`[admin]` on the Wake host and restart Wake. See
[Choose the browser origin](../../README.md#choose-the-browser-origin).

### Start the tunnel automatically

After verifying the manual connection, press Ctrl+C to free both ports. The service
needs SSH credentials available without a terminal prompt, including access to
your SSH agent if the key requires one.

On the client, install the [tunnel service](../../.container/wake-tunnel.service) from
the release URL and enable it. The service reads the `Host wake` entry from
`~/.ssh/config`, the only file you need to edit. For an untagged build, replace
`v0.1.0-rc.2` in the URL with the full commit SHA.

```sh
mkdir -p ~/.config/systemd/user
curl --fail --show-error --location \
  https://raw.githubusercontent.com/trippwill/wake/v0.1.0-rc.2/.container/wake-tunnel.service \
  --output ~/.config/systemd/user/wake-tunnel.service &&
  systemctl --user daemon-reload &&
  systemctl --user enable --now wake-tunnel.service
```

The tunnel starts at login and reconnects when the SSH connection closes.
Read connection errors with `journalctl --user -u wake-tunnel.service`.
For startup before login, see [Optional user lingering](#optional-user-lingering).
To turn off automatic forwarding, run
`systemctl --user disable --now wake-tunnel.service`.

## Use an untagged commit or branch

Build the appliance image locally on the Wake host to try an untagged commit.
Use a host directory reserved for development. Prepare it using the
[rootless quick start](#rootless-quick-start) directory command or
[custom storage](#custom-storage) if the default path belongs to a release.
Create the directory for the image override.

```sh
mkdir -p ~/.config/containers/systemd/wake.container.d
```

Set `wake_ref` to the branch name or full commit SHA:

```sh
wake_ref=main
git clone --no-checkout https://github.com/trippwill/wake.git wake-source
cd wake-source
git fetch origin "$wake_ref"
git checkout --detach FETCH_HEAD
mise install
mise run container:build
```

Use `~/.config/containers/systemd/wake.container.d/image.conf` for the local build:

```ini
[Container]
Image=localhost/wake:dev
Pull=never
```

Install the matching Quadlet from the checkout and start it:

```sh
podman quadlet install --replace .container/wake.container
systemctl --user restart wake
```

For a first installation, set the browser password as in the quick start.
Repeat the fetch, checkout and build steps to update the local image, then
reinstall the Quadlet and restart the service. Use this selected image for
maintenance commands as well.

## Rootful Podman option

The same image and Quadlet also support rootful Podman. The system service runs
as root, and persistent files are owned by root. Use `/var/lib/wake` for the
installation and `/etc/containers/systemd/` for the Quadlet and local drop-ins:

```sh
sudo install -d -m 0700 /var/lib/wake
sudo mkdir -p /etc/containers/systemd/wake.container.d
sudoedit /etc/containers/systemd/wake.container.d/rootful.conf
```

Save these contents in `/etc/containers/systemd/wake.container.d/rootful.conf`:

```ini
[Container]
Volume=
Volume=/var/lib/wake:/data:Z

[Install]
WantedBy=
WantedBy=multi-user.target
```

Choose another root-owned host directory by changing the volume source. Install
and start the system service, then set the password:

```sh
sudo podman quadlet install "https://raw.githubusercontent.com/trippwill/wake/v0.1.0-rc.2/.container/wake.container"
sudo systemctl start wake
sudo podman exec -it wake wake access set password
```

`multi-user.target` starts this service at boot; rootful setup does not need
lingering. Use `sudo systemctl` without `--user`, `sudo journalctl -u wake`, and
`sudo podman` for logs, snapshots and updates.

The untagged-build flow uses `/etc/containers/systemd/wake.container.d/image.conf`
in this mode. Rootful Podman has its own image storage: for a locally built image,
run `mise run container:export`, then
`sudo podman load --input .cache/container/wake-amd64.tar` (use `wake-arm64.tar`
on arm64) before starting the system service.

Browser URLs and SSH forwarding are the same in both modes. Run one instance on
the default ports. When moving between modes, stop and uninstall the old service,
copy the stopped installation, and give the destination files to its operating
user: your user for rootless Podman, root for rootful Podman. See
[Podman's Quadlet documentation](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
for system and user unit paths.

## Logs and snapshots

```sh
systemctl --user status wake
journalctl --user -u wake
podman exec wake wake config check --check-key
```

To take a snapshot, choose a new archive filename.

```sh
podman exec wake wake storage snapshot /data/backups/history.tar
```

The snapshot command works while Wake runs. Choose a new filename for each
snapshot. The archive contains financial history and evidence; it excludes the
key, browser password and client grants.

## Restore a warehouse snapshot

To restore into a new installation, choose an empty host directory. These examples use the default rootless
storage path and the release image; substitute your selected path and image if
using a custom installation or an untagged build:

```sh
install -d -m 0700 "$HOME/.local/share/wake-restored" "$HOME/.local/share/wake-restored/backups"
cp "$HOME/.local/share/wake/backups/history.tar" "$HOME/.local/share/wake-restored/backups/"
podman run --rm --network=none --read-only --cap-drop=all \
  --security-opt=no-new-privileges --volume "$HOME/.local/share/wake-restored:/data:Z" \
  ghcr.io/trippwill/wake:v0.1.0-rc.2 config init --generate-key \
  --set storage.financial_path=/data/financial/wake.db \
  --set storage.credentials_path=/data/state/connections.db \
  --set storage.sync_path=/data/state/sync.db \
  --set credentials.key_path=/data/config/credentials.key \
  --set paths.runtime_dir=/run/wake
podman run --rm --network=none --read-only --cap-drop=all \
  --security-opt=no-new-privileges --volume "$HOME/.local/share/wake-restored:/data:Z" \
  ghcr.io/trippwill/wake:v0.1.0-rc.2 storage restore financial /data/backups/history.tar
```

To switch to the restored installation, stop Wake and prepare the override directory.

```sh
systemctl --user stop wake
mkdir -p ~/.config/containers/systemd/wake.container.d
```

Set
`~/.config/containers/systemd/wake.container.d/storage.conf` to:

```ini
[Container]
Volume=
Volume=%h/.local/share/wake-restored:/data:Z
```

Reload the local drop-in, start the restored installation, and set its password:

```sh
systemctl --user daemon-reload
systemctl --user start wake
podman exec -it wake wake access set password
```

Restore requires vacant warehouse and sidecar paths; preserve an existing
installation separately instead of overwriting it.

## Update the appliance

To update, set `wake_version` to the published release you want. Its Quadlet
selects the matching image. Remove an untagged build's `image.conf` override
before returning to a release. Replacing the Quadlet preserves `storage.conf`.
Run `systemctl --user stop wake` and
[back up the stopped installation](#move-or-back-up-a-stopped-installation)
before replacing the Quadlet. For an update from RC1 to RC2 or stable v0.1.0, keep
the same data directory and configuration. If RC1 started with only the financial
store, [recover synchronization](#troubleshoot-startup) to enable source connections.
For a move from a pre-RC development build, preserve its directory and configure
a fresh one for the release.

```sh
wake_version=v0.1.0-rc.2
podman quadlet install --replace \
  "https://raw.githubusercontent.com/trippwill/wake/$wake_version/.container/wake.container"
systemctl --user start wake
```

## Move or back up a stopped installation

To move the complete installation, stop Wake with `systemctl --user stop wake`
and copy the entire host directory, including `config/`, to the new host while
it stays stopped. Preserve private permissions and make the files owned by the
user who will run rootless Podman there. Install the same release on that host,
select the copied directory and start Wake. The saved key,
password, API grants and financial history remain in place. The same stopped
directory copy is a complete installation backup; protect it as private data.

## Troubleshoot startup

On v0.1.0-rc.1, a fresh appliance can start with only the financial store. Saving
a source connection then reports that the provider is unavailable. Check the
startup logs for `upstream_storage_unavailable` and check that both
`state/connections.db` and `state/sync.db` are absent under the appliance data directory.

Recover synchronization on the stopped appliance. For the default rootless
installation, preview the recovery with these commands. Use the image version
and data directory of your installed appliance.

```sh
systemctl --user stop wake
podman run --rm --network=none --read-only --cap-drop=all \
  --security-opt=no-new-privileges --tmpfs /run/wake:rw,nosuid,nodev,noexec,mode=0700 \
  --volume "$HOME/.local/share/wake:/data:Z" \
  ghcr.io/trippwill/wake:v0.1.0-rc.2 storage recover-sync
```

Review the preview, then repeat the `podman run` command with `--confirm` and the
printed `plan_id` appended. This creates the synchronization and connection
stores and preserves the financial warehouse. Start Wake again with
`systemctl --user start wake`. In **Connections**, open **Review recovery steps**
and select **Approve restart plan** before adding the source again.

For a startup failure, read `journalctl --user -u wake`. Initialization and storage
errors retain Wake's CLI guidance. Configuration is at
`config/config.toml` inside your chosen host directory (by default,
`~/.local/share/wake/config/config.toml`); stop Wake before editing it, then restart. If
ports are occupied, choose distinct available `admin.port` and `api.port` values
and use those ports for browser access and SSH forwarding. The health check reads
the configured browser port. Key-provisioning recovery commands can run against
the stopped installation using `podman run` with the same image and volume, as
in the restore example.
