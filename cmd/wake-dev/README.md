# Development adapter protocol

The runner starts the executable with one JSON request on stdin. Protocol version 1 uses
`sync` for an online read and `export` for the locally committed state. The runner uses
`follow` to synchronize until interrupted. Profiles are `history` and `positions`.

```json
{"protocol":1,"operation":"sync","profile":"history","state_dir":"/private/client","endpoint":"http://127.0.0.1:12345","token_file":"/private/client.token"}
```

Positions requests include the public `account_id`. An export receives no endpoint or token.
Progress phases are `connecting`, `following`, `retrying`, and `idle`.
Report progress and installed versions as JSON Lines on stdout. Successful operations end with
a terminal event and exit zero. Send human-readable diagnostics to stderr.

```json
{"protocol":1,"event":"progress","phase":"connecting"}
{"protocol":1,"event":"committed","baseline_id":"example-baseline","version":"example-version"}
{"protocol":1,"event":"complete"}
```

For export, include `view` on the terminal event with the complete published snapshot or positions
view. Preserve exact scalar strings and null values. Failure emits a terminal `failed` event
and exits nonzero.

The runner interrupts a transfer for a newer publication and exports the previously
committed view before retrying. The follower keeps the same endpoint across a daemon
restart. It must reconnect, commit the new publication, and export that view after shutdown.
