# Security policy

## Report a vulnerability

Use [GitHub's private vulnerability report form](https://github.com/trippwill/wake/security/advisories/new)
to report a suspected vulnerability. Anyone with a GitHub account can report.
Keep vulnerability details out of public Issues, Discussions, and pull requests.

Include the affected Wake commit or version, the relevant configuration and
deployment conditions, and the possible impact. Provide enough detail to
reproduce the issue with synthetic data. Keep real financial records, API tokens,
operator unlocks, provider credentials, and encryption keys out of the report.

The repository owner can follow up in the private report and coordinate any
fix and disclosure there.

## Supported versions

Wake is in pre-release development. Reports against the current `main` branch
are welcome. Include the exact commit if you are running a development build.
Wake has no maintained release branches at this stage.

## Security scope

Wake serves one user per instance. Its supported deployment scope is local or
private LAN use without internet exposure. The operating system account that
runs Wake, host root, and any front end terminating encrypted traffic are
trusted. On the network, operators choose transport protection for Wake's HTTP
listeners. Plain HTTP exposes requests and responses to network observers.

Report suspected flaws that let someone cross an access boundary, expose
credentials or financial data, alter retained financial evidence, or materially
disrupt Wake. Examples include financial API reads without a valid client token,
browser administration without operator authorization, failures to revoke client
access, and exposure of stored sync credentials. Source responses and imported
files are untrusted input.

See [Trust and recovery](.docs/engineering/architecture.md#trust-and-recovery)
for the supported trust model and recovery behavior.
