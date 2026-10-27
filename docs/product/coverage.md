# Current coverage

Wake provides a runnable daemon, local configuration and setup, credential-key
provisioning and encryption primitives, authenticated admin sign-in, appearance
preferences, configuration/status inspection, private process/socket ownership
and joined shutdown. The socket has no financial routes.

Financial acquisition, processing, corrections and consumer publishing are not
available in this foundation. The [product brief](brief.md) describes broader
intent; it does not claim implemented coverage.

## Compatibility

The executable targets Linux. Storage has a pre-release development format with
no migration guarantee. Unsupported stores fail closed; preserve a backup and
select fresh paths or explicitly move the stores and sidecars aside. Startup
does not convert or delete unsupported data. Setup provisions credential keys;
startup never generates replacement key material.
