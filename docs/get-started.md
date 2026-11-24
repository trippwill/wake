# Get started with Wake

Wake runs on a Linux machine you control. Use its browser interface on that
machine or connect from another device on your private LAN using SSH.
These instructions use [v0.1.0-rc.2](https://github.com/trippwill/wake/releases/tag/v0.1.0-rc.2),
a prerelease.

## Install the Podman appliance

On a Linux host with systemd and Podman 5.6 or newer, follow the
[rootless appliance quick start](deployment/wake-appliance.md#rootless-quick-start).
The appliance provisions its credential key on first launch. Set the password
from the host and sign in as directed in the quick start.

## Install the native command

In an empty directory on Linux amd64 or arm64, verify and install the release executable:

```sh
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo "Unsupported architecture" >&2; exit 1 ;;
esac
release=https://github.com/trippwill/wake/releases/download/v0.1.0-rc.2
curl -fLO "$release/wake-linux-$arch.tar.gz" &&
  curl -fLO "$release/SHA256SUMS" &&
  sha256sum --check --ignore-missing SHA256SUMS &&
  tar -xzf "wake-linux-$arch.tar.gz" wake &&
  install -Dm755 wake "$HOME/.local/bin/wake" &&
  export PATH="$HOME/.local/bin:$PATH"
```

To build and install the release with Go 1.27 or newer, run:

```sh
GOBIN="$HOME/.local/bin" go install go.trippwill.dev/wake@v0.1.0-rc.2
export PATH="$HOME/.local/bin:$PATH"
```

### Start Wake and sign in

With `wake` on `PATH`, create the configuration and credential key, then set a browser password:

```sh
wake config init --generate-key
wake access set password
wake serve
```

Enter and confirm a password when prompted. Use it to sign in at the URL the
command prints. At the default port, open <http://wake.localhost:8787>. Stop
the foreground daemon with Ctrl+C.

For an existing configuration, use
[credential-key provisioning](deployment/native.md#provision-a-credential-key)
instead of running `config init` again. For a custom port, configuration path,
or SSH connection, see [Operate the native command](deployment/native.md).

## Connect your first account

1. Choose [IBKR Flex](sources/ibkr-flex.md) or [SnapTrade Personal](sources/snaptrade.md)
   and prepare access at the source.
2. In Wake, open **Connections**, choose **Add source connection**, select your
   source, and save the access details.
3. Open the connection’s **Status** and confirm the discovered account you want
   Wake to synchronize. Confirmation grants automatic acquisition for that account.
4. Open **Overview** to check progress and **Reports** to see retained receipts.
   Follow the [history guide](history.md) to inspect what Wake has accepted.

Use one authoritative source connection for each underlying account. If you
connect the same brokerage through both providers, use distinct accounts.
