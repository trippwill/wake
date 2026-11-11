# SQLite indexes for Activity browsing

For Activity histories in the hundreds of thousands of rows, SQLite indexes can reduce
the number of rows Wake examines per interaction. The pinned Go driver already supports
FTS5 trigram search on the tested Linux build. Compiling SQLite as C could reduce the CPU
cost of a query, but Wake has no driver comparison for this workload.

## Observed behavior

The [synthetic warehouse measurements](performance-warehouse.md#observations) recorded a
median of 1.98 s at 20,000 activities and 9.95 s in one 100,000-activity run for a text
search combined with exact-price ordering. The fixture uses two repeating resource
templates and the search matches every activity. These timings describe the store read
for that workload. The default page, selective searches and HTTP latency need their
own measurements.

`EXPLAIN QUERY PLAN` on the [Wake schema](../../internal/persistence/sqlite/schema.sql)
using Python's SQLite 3.51.2 and an empty in-memory database showed
`USE TEMP B-TREE FOR ORDER BY` for the default newest-first page, an account-and-kind
filter and a text search ordered by price. The account-and-kind plan started from the
published Activity range and applied those filters afterward. The plans describe the
chosen access paths under those conditions. The count and page queries each evaluate
the search predicate, so a 50-row page can still involve substantial work across the
history. [Activity SQL](../../internal/persistence/sqlite/admin_queries.sql) ·
[SQLite query plan guide](https://www.sqlite.org/eqp.html)

A Linux smoke test using Wake's `modernc.org/sqlite` v1.48.0 with `CGO_ENABLED=0`
reported SQLite 3.51.3. The following SQL returned one match each for `rk.b` and `divid`,
and zero matches for the single-character term `F`.

```sql
CREATE VIRTUAL TABLE activity_search USING fts5(text, tokenize='trigram');
INSERT INTO activity_search(text) VALUES ('buy BRK.B'), ('buy F'), ('cash dividend');
SELECT count(*) FROM activity_search WHERE activity_search MATCH '"rk.b"';
SELECT count(*) FROM activity_search WHERE activity_search MATCH '"divid"';
SELECT count(*) FROM activity_search WHERE activity_search MATCH '"F"';
```

## Implications for Activity reads

- Composite B-tree indexes on a compact derived Activity table could serve date ordering
  and common account and kind filters from an ordered index range. Storing sort fields in
  that table would reduce JSON extraction during reads. Numeric order needs Wake's exact
  decimal semantics, including the existing currency and unit groups and stable tie-breaks.
  [Query planner](https://www.sqlite.org/queryplanner.html#searching_and_sorting_at_the_same_time) ·
  [Wake read semantics](../engineering/architecture.md#user-and-consumer-boundaries)
- An FTS5 trigram index on precomputed search text could narrow the set of activities to
  count and sort. Preserving current literal substring behavior requires a fallback for
  terms shorter than three characters and parity checks for displayed labels, exact
  values and case handling. Broad terms can still make exact counts expensive.
  [SQLite trigram tokenizer](https://www.sqlite.org/fts5.html#the_trigram_tokenizer)
- The `modernc` maintainers measured 1.3–2.0 times the CPU time of C SQLite on three
  Linux query workloads. The measurements use a different SQLite version and query mix
  from Wake's tests. Wake's Go search and decimal callbacks could also affect a cgo
  comparison. A pinned, bundled C build would control SQLite version and compile options
  across targets and require C toolchains for release builds. Linking system `libsqlite3`
  would make the SQLite version and compile options depend on the target installation.
  [modernc performance notes](https://pkg.go.dev/modernc.org/sqlite#hdr-Performance) ·
  [Native driver build options](https://github.com/mattn/go-sqlite3#compilation)

A useful comparison would time the Activity HTTP endpoint at 100,000 and 500,000 rows
with varied symbols and activity kinds. Measure the newest-first page, account and kind
filters, selective and broad searches, and exact-price ordering independently. Check that
the candidate queries return the same rows, counts and order. If profiles then show SQLite
execution dominates, compare the same fixture and queries with a pinned C build on one target.
