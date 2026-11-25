# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Read the [Wake user docs](https://trippwill.github.io/wake/) for source setup,
reviewing financial history, backups, and client access. The guides are also
[browsable in this repository](docs/index.md).

The quick start installs [v0.1.0](https://github.com/trippwill/wake/releases/tag/v0.1.0)
for Linux. Each Wake instance serves one user. Use it on the same machine or over
SSH forwarding on a private LAN.

### Install the appliance

Follow the [rootless Podman quick start](docs/deployment/wake-appliance.md#rootless-quick-start)
on a Linux host with systemd and Podman 5.6 or newer.

### Install and run the native command

With Go 1.27 or newer installed, run:

```sh
GOBIN="$HOME/.local/bin" go install go.trippwill.dev/wake@v0.1.0 &&
  export PATH="$HOME/.local/bin:$PATH" &&
  wake config init --generate-key &&
  wake access set password &&
  wake serve
```

Sign in with that password at the URL Wake prints. The
[native install guide](docs/get-started.md#install-the-native-command) includes
verified Linux release archives and the next steps for connecting a source.

## Contribute

Start in [GitHub Discussions](https://github.com/trippwill/wake/discussions) to ask
questions, propose changes, or offer to help. See [Contributing to Wake](CONTRIBUTING.md)
for contributor access and the issue and pull request workflow.
For suspected vulnerabilities, follow the [security policy](SECURITY.md).

The Go module is `go.trippwill.dev/wake`. Start with [AGENTS.md](AGENTS.md), the
[documentation index](.docs/README.md) and [development guidance](.docs/engineering/development.md).
The [product brief](.docs/product/brief.md) owns Wake's north star, [current coverage](.docs/product/coverage.md)
describes supported behavior and limitations, and [DESIGN.md](DESIGN.md) owns visual design.
