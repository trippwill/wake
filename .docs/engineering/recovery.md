# Back up and restore Wake

## Create a backup

Stop Wake and wait for shutdown to finish. Run the same build that last opened
the stores, with the same configuration and storage path overrides.

```sh
wake backup --config /absolute/private/config.toml
```

Copy the resulting `.backup.tar` to protected backup storage. Each run replaces
the previous archive at that path after verifying the new pair. Save the
configuration and record the Wake release or build used. Keep the matching
master key separately protected. The archive contains financial history and
encrypted credentials. It contains two standalone SQLite snapshots and
requires no SQLite sidecars.

Wake also creates `.upgrade-backup.tar` beside the warehouse before an upgrade
modifies either store. The same restore steps apply to that archive. If an upgrade
is interrupted and both original stores are available, restart the matching
upgrade build at the original paths. Leave its intent, archive and any SQLite
journals in place. Wake verifies and reuses that archive before continuing.

## Restore into a stopped profile

Use the build that wrote the backed-up formats and financial rule. Start with
empty destination paths. For an existing installation, stop Wake and set the
displaced databases and any rollback journal, WAL, shared-memory file,
super-journal and upgrade intent aside together. Keep the archive intact.

Extract the two named entries from the trusted archive in a private staging
directory. Use the same archive for both files.

```sh
set -e
umask 077
stage=$(mktemp -d)
tar -xf /absolute/private/wake.backup.tar -C "$stage" \
  wake-upgrade/warehouse.db wake-upgrade/access.db
install -d -m 0700 /absolute/private/data /absolute/private/access
for path in /absolute/private/data/wake.db /absolute/private/access/connections.db; do
  test ! -e "$path"
  test ! -L "$path"
done
install -m 0600 "$stage/wake-upgrade/warehouse.db" /absolute/private/data/wake.db
install -m 0600 "$stage/wake-upgrade/access.db" /absolute/private/access/connections.db
```

Prepare the destination directories with mode `0700` and ownership matching
the Wake process. Point the recovered configuration at the two files and at a
private runtime directory. Restore or reprovision browser unlock material
separately. Start Wake once with the recovery flag.

```sh
wake serve --config /absolute/private/restored.toml --restored
```

Wake checks the store identities and formats and persists an upstream pause
before it starts serving. Subsequent starts can use the regular command. Open
admin to inspect evidence, decisions, and source readiness. Start the financial
consumer with a fresh local mirror and let it install a full snapshot before
following changes. A restored warehouse can revisit publication counters from
the discarded timeline.

For inspection without the matching key, use a recovery configuration with
the master-key setting unset. Leave the encrypted access snapshot in place.
A configured missing or malformed key stops startup. A well-formed wrong key
fails decryption during source access review. Restore the correct key, or
repair source access through admin, then use **Resume upstream work** after
review. When only the warehouse is available, start it in an isolated profile
with a missing access path to enter Wake's existing access reconstruction flow.
Keep the warehouse and recoverable backups while repairing access.

If either snapshot is damaged or an older build refuses a newer format, keep
the failed files for diagnosis and restore a complete earlier pair with its
compatible build and key. Use the exact build that targeted a pending history
rebuild when restoring a snapshot that already contains its approval checkpoint.

## Podman restore

Use the same steps with a stopped Wake pod and empty bind-mounted storage
directories. Install both snapshots on the host as the UID used by Wake, with
the permissions above. Supply the matching configuration and separately
protected key through the deployment's private mounts. Apply the deployment's
SELinux volume labels to the restored paths. Keep the runtime directory empty.
Start Wake with `serve --restored` for the first launch, using the same image
and mounts as the deployment. Remove that flag for ordinary restarts after the
recovery pause is recorded.

Use the deployment's private SSH forwards to inspect admin and read a full
financial snapshot from the restored pod. Compare it with the snapshot saved
before backup. Confirm the upstream pause survives a restart before reviewing
and resuming source access. See [Podman deployment #5](https://github.com/trippwill/wake/issues/5)
for the reference deployment.
