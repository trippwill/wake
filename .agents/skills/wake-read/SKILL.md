---
name: wake-read
description: Read accounts, financial activity, and reported positions from a running local Wake daemon. Use when the user wants to inspect or analyze their Wake data using the installed wake CLI.
---

# Read Wake data

1. Run `command -v wake`, then `wake --json doctor`. Use the same Unix user
   as the daemon. The private financial socket needs no token or browser login.
   If the daemon uses a different profile, pass `--config /absolute/config.toml`
   or `--socket /absolute/wake.sock` on each leaf command, before any positional
   argument.
2. List accounts and take the exact Wake ID from `resources[].id`. IDs are scoped
   to the warehouse.
3. Query activity with a narrow date range and family when possible. Activity
   defaults to accepted records. Follow `next_offset` if needed and compare
   `warehouse_id` and `version` between pages. Restart the query if they change
   and the task needs consistent results. Each page reads a complete snapshot.
4. Read the reported position view for the chosen account. Check `status`,
   `as_of` and `reason` before using the rows. An unavailable view is distinct
   from an available view with zero positions. Keep monetary amounts and
   quantities as exact decimal text.

Examples after choosing a Wake account ID:

```sh
wake --json accounts list
wake --json activity query --account "$ACCOUNT_ID" --from 2026-10-01 --to 2026-10-31 --family trade
wake --json positions get "$ACCOUNT_ID"
```

Use `wake --json help` to discover commands. For an exact API read that lacks a
dedicated command, use `wake --json request get '/v1/activity/ACTIVITY_ID'`.
Successful reads emit JSON on stdout and errors go to stderr. Ask for explicit
authorization before changing Wake configuration, starting acquisition, changing
financial records, or sending private financial data to another service.
