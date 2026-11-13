# Back up and restore Wake

## Create a backup

Stop Wake and wait for shutdown. Use the same configuration and storage paths
as the daemon.

```sh
wake backup --config /absolute/private/config.toml
```

Wake reports the archive path beside the warehouse. Its filename includes the
UTC time and a random suffix. The archive contains both standalone SQLite
snapshots and a manifest. Routine backups accumulate. Copy them to protected
backup storage and keep the launch configuration, build version, and matching
master key separately protected.

Wake uses the same archive format for pre-upgrade backups. An interrupted
upgrade verifies and reuses its recorded archive. Restart the matching build
with the original files in place to finish that upgrade. After the upgrade
transaction commits, Wake records the new latest archive and removes only the
previous archive it recorded. Routine backups and copied archives are kept.

## Restore a stopped profile

Use a restore-capable build that supports the archive's formats and history rule.
Prepare the configured database parent directories with private permissions
and Wake's process ownership. Stop Wake, then preview the restore.

```sh
wake restore --config /absolute/private/config.toml --archive /absolute/private/backup.tar
```

Review both destination paths and the files Wake will preserve. Repeat the
command with the plan ID printed by Wake.

```sh
wake restore --config /absolute/private/config.toml --archive /absolute/private/backup.tar --confirm PLAN_ID
wake serve --config /absolute/private/config.toml
```

An existing profile's databases, journals, and upgrade intent are preserved in
private recovery directories printed by the restore command. If restore
stops, rerun the same command with the recorded plan ID. Keep the archive and
the recovery directories in place until it completes. Use the restore-capable
build throughout the operation, since older builds cannot detect an interrupted
restore. If an older format requires an older build, use a restore-capable build
for that format. For older builds without the command, use their documented
stopped-profile recovery procedure in an isolated profile.

The restored profile starts with upstream work paused. Open admin to inspect
retained history and review source access before selecting **Resume upstream
work**. For inspection without the matching key, unset the configured key path
while leaving the encrypted access database intact. A configured missing or
malformed key still stops startup. When only the warehouse is available, Wake's
existing access reconstruction flow can open it in an isolated profile with a
missing access path.

The restored warehouse receives a new identity. Consumers replace their
snapshots and positions on reconnect and should invalidate cached explanations
from the previous warehouse. Financial resource IDs and retained evidence and
decisions carry over.

## Podman restore

Stop the [Wake pod](podman.md#operate-and-back-up). Run the matching image with
the deployment's Wake UID, private volumes and SELinux labels, passing
`backup` or `restore` in place of `serve`. Use paths as seen inside the
container. If the archive is outside the configured data mount, bind its
directory read-only at a private container path. Run preview and confirmation
with the same mounts and arguments.

Restart the deployment normally. Use the private SSH forwards to inspect
admin and read a full financial snapshot. Check the recovered baseline and
review upstream access before resuming.
