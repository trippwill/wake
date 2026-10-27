# Wake

Wake is a local-first, self-hostable financial evidence warehouse and synchronization
daemon. It preserves source reports, reconciles financial activity, and makes that
activity available for review and use by other applications. Licensed under
MPL-2.0 ([LICENSE](LICENSE)).

## Using Wake v0.1.0

Wake provides the administrative foundation. Wake supports
local setup, protected credentials, sign-in, appearance preferences and process
configuration/status. Financial synchronization, imports and financial queries are
unavailable in this build.

### Set up and start

From a Linux source checkout:

```sh
mise install
mise run bin -- setup
mise run serve
```

Follow setup's prompts for local paths and a browser unlock. Keep the generated
credential key. Open the local admin address selected during setup and sign in.
Keep the terminal running; stop Wake with Ctrl+C.

For a separate configuration, pass the same absolute path to both commands:

```sh
mise run bin -- setup --config /absolute/private/path/config.toml
mise run bin -- serve --config /absolute/private/path/config.toml
```

### Find help

```sh
mise run bin -- help
mise run bin -- help setup
mise run bin -- config describe
mise run bin -- config check
```

Use command help for exact options.

To change a forgotten browser unlock, stop Wake and rerun setup with the same
configuration, then restart. Setup retains the credential key; keep that key safe.

## Contribute

The Go module is `go.trippwill.dev/wake`. Start with [AGENTS.md](AGENTS.md), the
[documentation index](docs/README.md) and [development guidance](docs/engineering/development.md).
The [product brief](docs/product/brief.md) owns Wake's north star, [current coverage](docs/product/coverage.md)
describes supported behavior and limitations, and [DESIGN.md](DESIGN.md) owns visual design.
