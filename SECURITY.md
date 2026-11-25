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

Report issues affecting the current release or the `main` branch.
Include the version tag or the exact commit for a development build.

## Security scope

Wake is a local, single-user daemon. The browser and financial API listen only
on IPv4 loopback. The supported deployment scope covers same-machine access
and SSH forwarding within a private LAN. The account that runs Wake, host root,
and the operator's SSH transport are trusted. Host root can read all Wake data,
including the credential key used for unattended operation.

Report suspected flaws that let someone cross an access boundary, expose
credentials or financial data, alter retained financial evidence, or materially
disrupt Wake. Examples include financial API reads without a valid client token,
browser administration without operator authorization, failures to revoke client
access, and exposure of stored sync credentials. Source responses and imported
files are untrusted input.

See the [Wake threat model](.docs/engineering/threat-model.md) for deployment
assumptions, security controls, and operator responsibilities.
