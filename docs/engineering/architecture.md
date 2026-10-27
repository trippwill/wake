# Architecture

Wake is one Go daemon with standard-library HTTP, private SQLite storage and a
templ admin shell with embedded browser assets. The CLI provisions local paths,
browser access and credential-key protection. Daemon startup claims private process
and socket paths, opens storage, checks configured credential protection and serves
the authenticated admin shell. Shutdown cancels and joins requests before releasing
resources. The initialized private socket has no financial routes.

Administrative authority is distinct from socket read access. The deployment
assumes a trusted OS user and private storage; wider access requires an explicit
trust decision. Keys remain operator-provisioned through setup, and configured
missing or invalid keys fail closed. Ordinary diagnostics exclude secrets.

Storage has an explicit pre-release format. Unsupported stores fail closed before
schema or journal changes; startup never converts or deletes them. Preserve backups
and choose fresh paths or explicitly move unsupported stores and sidecars aside.

[Current coverage](../product/coverage.md) describes supported behavior and
limitations; the [product brief](../product/brief.md) describes broader intent.
Source and tests own exact interfaces. [DESIGN.md](../../DESIGN.md) owns visual design.
