# Wake threat model

Wake is a local, single-user financial daemon. Its browser and financial API
listeners bind only to IPv4 loopback. This model covers same-machine access
and SSH forwarding within a private LAN. Native Linux and Podman deployments
each run one daemon.

Wake reads reports and provider responses, preserves source evidence,
reconciles financial records, and publishes the accepted model. The browser
administers the instance. Financial clients read the published model through
a separate interface.

## Trusted host and storage

**Root can read all Wake data on the host**, including the financial warehouse,
retained reports, local authentication files, and the credential key used for
unattended operation. Root can use the key to decrypt saved provider access.
The operating system account that runs Wake has the same access to the
installation. Anyone who controls that account, the host, or the executable
can also change Wake's data and behavior.

Use a host and OS account you trust. Keep the installation, key, runtime files,
and client token files in private locations. Wake checks ownership and private
permissions on sensitive files and checks database files and their sidecars
before use. Keep writable storage under a trusted directory hierarchy.
Disk or volume encryption protects a locked volume. An unlocked volume is
accessible to host root. The financial warehouse, evidence, and exports
contain readable financial data.

The rootless Podman appliance uses host networking and maps container root
to the invoking host account. The rootful variant runs as host root. The
appliance uses a read-only image filesystem, a private SELinux label on the
writable data mount, a transient runtime mount, dropped capabilities, and
no-new-privileges. Its data mount includes the credential key and the stores
the daemon needs. These settings limit the container's access to the host
while Wake retains access to the mounted installation. See the
[appliance deployment guide](../../docs/deployment/wake-appliance.md) and
[shipped Quadlet](../../.container/wake.container).

## Browser and financial clients

Other processes and local accounts can reach loopback, so each HTTP listener
enforces its own authorization. On a private LAN, the operator can forward
both ports over SSH to the Wake host. The client-side binds stay on loopback,
and the operator verifies the host key. The SSH account and host control
what the tunnel can reach. The Wake browser and API still require
their own credentials.

| Interface | Authority and controls |
| --- | --- |
| Browser administration | The operator signs in with a password or enrolled passkey. The admin listener checks Host. Mutations require a matching browser origin, and authenticated mutations require the session's CSRF proof. HTTPS browser origins receive a Secure session cookie. Passkeys use short-lived, single-use challenges and user verification. |
| Financial API | An operator issues a separate bearer token for each client. The private auth file stores token verifiers. Grants authorize read access across the instance. The API checks the current grant on each admission, including conditional reads and event streams. Revocation persists the grant change and cancels admitted requests. |
| OS administration | The owning account can manage local credentials, configuration, and recovery using the CLI. Browser recovery rotates session authority while preserving financial grants. A browser session ends on sign-out or daemon restart. |

Browsers share Wake's host-only session cookie across ports under the same
hostname. Wake defaults to `wake.localhost` for the browser. Reserve it for
Wake. This separates the cookie from other local apps opened at
`localhost` or `127.0.0.1` and scopes WebAuthn enrollment to Wake's hostname.
Services browsed at other ports on `wake.localhost` still share its cookie scope.
See [Choose the browser origin](../../docs/deployment/native.md#choose-the-browser-origin).

Use separate client token files and revoke a lost token. Closing an SSH connection drops its forwarded
connections. Remove SSH access and close existing SSH sessions to revoke
transport access. Revoke Wake credentials independently when they may have
been exposed. Previously transmitted data remains with its recipients.
See [Trust and recovery](architecture.md#trust-and-recovery).

## Providers, evidence, and recovery

Provider responses and imported files are untrusted data. Wake validates input
before deriving financial state, retains the evidence required to explain
accepted records, and appends operator corrections. Providers can still
report inaccurate data. Use retained evidence and reconciliation to investigate
discrepancies. Wake encrypts provider credentials at rest and binds the
ciphertext to the credential identity. The operator provisions the key
explicitly. Wake keeps plaintext access scoped to the work that needs it.
An unavailable key can stop upstream access while retained history remains
available.

Treat backups, snapshots, client mirrors, and other copies as financial data.
Keep the original credential key separately for recovery. A financial snapshot
carries history and evidence. Installation restore verifies the archive and
associated databases and preserves the destination's local authority. Wake
checks storage identities, sidecars, and pending maintenance before activation.
Keep the original files and recovery intent together if a maintenance operation
is interrupted. Use the documented procedure with an archive from a trusted
source. Archive checksums verify file integrity. Trust in the archive's
origin comes from the operator's custody of the backup. See [Backup and recovery](../product/coverage.md#backup-and-recovery).

## Release trust

The operator trusts the selected executable or image, its build and publication
workflow, and the dependencies included in it. Verify the native release
archive against the published checksums. Obtain the checksums from the
authenticated project release. The appliance build pins its base image by
digest and resolves additional OS packages at build time. The release workflow
verifies the candidate revision and checks Linux amd64 and arm64 build artifacts
before publication. Record verification against the exact candidate and check
the published release separately. See [Publish a version](development.md#publish-a-version).
