# Run Wake on a Linux server with Podman

The [Quadlet files](../../.packaging/podman/) run Wake and a small admin relay in
one rootful Podman pod. A saved OpenSSH profile carries both browser admin and
financial reads over your LAN or private mesh.
The pod publishes no ports. You need a verified SSH host key and an existing
operator account that permits local Unix socket forwarding.

Use a Linux host with systemd 244 or newer, cgroup v2, and Podman 5.7 or newer
with the Quadlet generator installed. The pod uses the stop timeout support
introduced in [Podman 5.7](https://github.com/containers/podman/blob/v5.7.0/pkg/systemd/quadlet/quadlet.go).
Fedora 44 with SELinux enforcing is the reference environment. The runtime
image includes its own Fedora userspace.

## Build the image

From the root of the Wake source checkout on the server, build the image. The
build stage supplies Go and uses the checked-in generated sources and assets.

```sh
sudo podman build --ignorefile .packaging/podman/containerignore \
    -f .packaging/podman/Containerfile -t localhost/wake:local .
```

The units use the local image tag. Build from the Wake version you want to run.

## Set up the server

Run the setup script as the operator who will connect over SSH. The pod uses
host UID numbering so Wake, the relay and the SSH process see the same UID for
the private sockets.

Pass the three host directories to the script. They can be on
different local filesystems or ZFS datasets. Use dedicated Wake directories so
Podman's `:Z` relabeling applies only to Wake files. The render script accepts
absolute paths containing letters, digits, dots, underscores, and hyphens in
the directory names. Use real directories owned by the operator with mode
`0700`. The setup script creates them if missing.

### ZFS mount ordering

Mount the datasets before creating the directories. For automatic startup, make
the dataset mounts visible to systemd, for example with the
[OpenZFS mount generator](https://openzfs.github.io/openzfs-docs/man/8/zfs-mount-generator.8.html).
Quadlet orders Wake after the mounts needed for its bind sources. To fail startup
if a dataset is absent, add a line like `AssertPathIsMountPoint=/tank/history`
under `[Unit]` in the rendered `wake.container` for each dataset. Use the dataset
mount point even if the Wake directory is a child of it. On SELinux hosts, the
datasets must support SELinux labels. OpenZFS recommends
[`xattr=sa`](https://openzfs.github.io/openzfs-docs/man/7/zfsprops.7.html#xattr).

### Initialize and install

The setup script prepares the private directories, writes an initial Wake
configuration if needed, and renders the Quadlets into a new output directory.
It runs `wake setup --use-config` in the container with the same storage mounts
the Quadlets use. Review the prepared settings, then choose the browser unlock.
If setup is interrupted, rerun with the same host paths and keep the existing
configuration and master key.
If you reuse a configuration, check that its store paths use the intended
container mounts. The script retains the existing settings.

```sh
sh .packaging/podman/setup.sh ./wake \
    /var/lib/wake-podman/config \
    /var/lib/wake-podman/data \
    /var/lib/wake-podman/credentials
```

Review `./wake` and add any ZFS mount assertions. Install the directory as a
rootful Quadlet application. Podman reloads systemd after installing the files.

```sh
sudo podman quadlet install ./wake
sudo systemctl start wake-pod.service wake.service wake-admin-relay.service
sudo systemctl status wake-pod.service wake.service wake-admin-relay.service
```

Use `sudo podman quadlet list` to inspect the installed application. To update
the Quadlets, stop the pod, use `sh .packaging/podman/render.sh` with a fresh
output directory and the same three storage paths, and reapply any
mount assertions. Remove the installed application with
`sudo podman quadlet rm wake.pod`, then install the new directory and start the
units again. The three data directories are host bind mounts and are retained.

The pod starts at boot through the Quadlet's systemd install section. Systemd
creates `/run/wake-podman` when the pod starts and removes it when the pod stops.
The pod prepares the `sockets` subdirectory with mode `0700` and the operator's
UID and GID. The relay mounts only that subdirectory. The config directory holds
the configuration, admin unlock verifier, and generated master key. The
warehouse and encrypted credentials database live in their respective directories.

## Connect from the laptop

Confirm the server's SSH host key fingerprint using a trusted path, then save
the key in your SSH client's known-hosts file. Add this profile to
`~/.ssh/config`. Set `HostName` to the server's LAN name, IP, or mesh
address and set `User` to the operator account used during server setup.

```sshconfig
Host wake-server
    HostName wake-host
    User wake-operator
    SessionType none
    StrictHostKeyChecking yes
    ExitOnForwardFailure yes
    StreamLocalBindMask 0177
    LocalForward 127.0.0.1:8787 /run/wake-podman/sockets/admin.sock
    LocalForward %d/.ssh/wake-finance.sock /run/wake-podman/sockets/wake.sock
```

Run `ssh wake-server` and leave the connection open. Visit
`http://127.0.0.1:8787` in the browser and sign in with the operator unlock.
Use that exact address and port for Wake's Host and Origin checks. To use a
different port, set it in Wake's configuration and use it in the relay target,
the local forward, and the browser URL.

Point your financial client at `~/.ssh/wake-finance.sock`, or check it with the
Wake CLI:

```sh
wake --json doctor --socket "$HOME/.ssh/wake-finance.sock"
wake --json request get --socket "$HOME/.ssh/wake-finance.sock" /v1/snapshot
```

Give the local socket path to trusted readers running under the laptop account
that opened the tunnel. Keep the SSH key and admin unlock with the operator.
Stopping the SSH connection disconnects both forwards and
closes open event streams. To revoke an SSH key, remove it from the server's
authorized keys and terminate any existing connections using it. If a stopped
connection leaves a socket file behind, remove it after confirming the old SSH
process has exited, then run `ssh wake-server` again.

## Operate and back up

Use `sudo journalctl -u wake-pod.service -u wake.service -u wake-admin-relay.service`
for startup and relay errors. A running SSH connection confirms the local
listeners were created. Check `wake doctor` and browser sign-in to confirm the
server-side destinations work. If host SSH cannot open the sockets, check that
its login UID matches the owner and that its SELinux policy permits the
connection. The Fedora reference setup uses the default unconfined operator
login.

Stop the pod with `sudo systemctl stop wake-pod.service` before replacing the
image or backing up. Follow the [managed backup and restore procedure](recovery.md#podman-restore).
After backup, image rebuild, or restore, start the three units using the setup
command above. Wake and the relay recreate their runtime sockets.
