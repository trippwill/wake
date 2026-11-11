# Synthetic warehouse performance

On the tested Linux laptop, first-time reconciliation of a single synthetic IBKR
confirmation report took substantially longer than repeat processing. A CPU
profile attributed the largest recorded cost to activity projection and SQLite
work; this is a starting point for further investigation, not a general capacity
limit or a claim about other source histories.

## Reproduce

Run `mise run performance:warehouse quick`, `representative`, or `large` on a
quiet machine. The end-to-end fixture uses respectively 1,000, 20,000, or
100,000 activities, each appearing in five, five, or ten distinct reports, and
100, 1,000, or 5,000 reported positions. One complete report contains all
activities. Each activity has one of two high-precision prices; the run also
checks repeat acquisition, idempotent replay, a fresh retained reinterpretation,
conflicting realized results, and a retained operator price decision. It times
real provider normalization, processing, admin queries, snapshots, and an
independently built socket consumer. Fixture generation and compilation precede
the measured phases. Logs contain per-phase elapsed time, Go allocation totals,
warehouse file sizes, and `/usr/bin/time -v` process resource statistics. The
consumer runs as a child: Go allocation totals in its phases describe only the
server process, and peak RSS is not a sum of simultaneous process memory.

`mise run performance:warehouse read-representative` and `read-large` isolate
20,000 and 100,000 activity reads. They copy two real projected resources into a
synthetic read model directly, outside timing, and log a digest of the fixture
specification. They do not measure evidence ingestion, reconciliation, or
positions. The 20,000 case runs five identical admin filter/sort samples and five changed
snapshot synchronizations through the independent consumer; the 100,000 case
runs one sample per phase to bound runtime. Their identical
resource bytes favor unchanged-resource comparisons, so other mixes may differ.

Both tasks require Linux `/usr/bin/time`; they create and remove disposable
warehouses and never need financial data or credentials. Results are diagnostic,
not CI thresholds. Keep output with the environment and each individual sample;
do not infer variance from a single run.

## Observations

On 2026-11-10, a Core Ultra 5 228V machine (8 logical CPUs, Linux x86-64,
Go 1.27.3), the quick end-to-end run took 12.29 s before adding the retained
reinterpretation and socket-consumer phases. Initial processing took 7.46 s,
normalization 0.67–0.72 s per report, and repeated processing 0.20–0.21 s per
report. Peak RSS was 78,776 KiB and the main warehouse, WAL, and shared-memory
files totaled 34,467,840 bytes at the end. These are single observations.

An intermediate run with 2,000 activities, five reports, and 100 positions
included the later phases and took 42.40 s. Initial processing took 20.51 s,
retained reinterpretation 11.96 s, and the three independent consumer phases
0.30 s (initial), 0.36 s (unchanged), and 0.97 s (changed). Peak RSS was
197,156 KiB and the main warehouse files totaled 86,691,840 bytes. The 2,000
case is not a named tier; its exact temporary fixture was the quick tier with
only the activity count changed.

A CPU profile of the 1,000-activity fixture (18.37 s including reinterpretation,
excluding socket consumer) captured 22.45 CPU seconds: `Process` accounted for
14.0 cumulative seconds, `projectArtifact` 12.19, and
`linkActivityObservations` 6.79. These nested values overlap. SQLite execution
for event links and repeated statement preparation appeared in the profile; no
single observation proves a production bottleneck. A candidate that reused
newly created activity terms improved the initial quick run from 7.46–7.55 s
to 7.30 s and was discarded because that did not meet the 20% improvement
criterion.

For 20,000 directly seeded read resources, five comparable samples on fresh
warehouses with the same fixture specification gave median filter/sort latency
2.02 s before and 1.98 s after the identical-decimal shortcut; allocated bytes
fell from about 30.31 MB to 21.29 MB per query (29.8%). The median query
scanned and sorted approximately 9,900 and 10,100 matching rows per second,
respectively. The dataset contains
many equal prices, and separate warehouses have different random seed resource
IDs. The test therefore establishes this allocation benefit for the specified
mix, not a universal latency gain. The corresponding profile fixture digest is
`ab28664cc5dc8ff012779bee53070cb43fac5c45830f7ea159ebcbf7297d0401`.

The validated-resource byte shortcut reduced the median 20,000-resource changed
consumer synchronization from 8.37 s (range 8.17–8.54 s) to 5.62 s (range
5.56–5.64 s), approximately 33%, or from 2,390 to 3,560 resources per second.
Each update changes one resource and retains the other resource bytes. The
shortcut is in the independent example consumer; the measurement includes the
server, socket, validation, and mirror write. The main process recorded about
207 MB of allocations in each synchronization; this excludes allocations inside
the consumer process. Peak per-process RSS for the baseline and final candidate
runs was 402,064 and 400,236 KiB respectively, not combined concurrent memory.

The 100,000-resource seeded read completed once in 49.80 s: exact filter/sort
9.95 s, full snapshot 0.73 s, initial consumer sync 11.26 s, and changed sync
25.64 s. Peak per-process RSS was 1,807,352 KiB; the machine had 32,356,296 KiB
RAM, 8,388,604 KiB swap, and about 440 GiB free in the temporary filesystem.
The run had a 120-second wall-time cap. Its fixture specification digest was
`bde7ed7441d29c64f66652246032fa5eed923090997eee23abc09c5118084d19`.
A single large sample does not establish variance or a before/after speedup.

The complete 1,000-activity end-to-end fixture with both shortcuts took 20.34 s:
initial processing 7.51 s, retained reinterpretation 6.49 s, and changed consumer
sync 0.37 s. Peak per-process RSS was 117,068 KiB; the main warehouse files used
43,405,312 bytes. Full 20,000 and 100,000 activity ingestion/replay tiers were
not run. The 1,000-to-2,000 initial processing measurements increased by about
2.7 times; extrapolation is uncertain, but repeated full-size ingestion was not
practical in the measurement window. The fast-seeded reads are not a substitute
for those missing end-to-end results.

## Activity click-to-render latency

Run `mise run performance:activity-browser` with Chromium or Brave installed.
Set `WAKE_BROWSER` to select the executable. The task starts the authenticated
admin handler on loopback with a disposable store. It seeds 20,000 trades from
two projected resources, then varies dates, quantities and prices. Every trade
matches the broad currency search. Roughly 1% contain the selective exact price.
Each path gets a warm-up and 30 timed requests for a 50-row first page. Sort
samples alternate direction. Searches use the price sort. Timing starts at the
browser click and ends two animation frames after the new rows are installed.
The task prints individual samples, the median and nearest-rank p95.

On 2026-11-10, Brave 157.1.99.55 on the Core Ultra 5 228V machine measured
the following p95s. The same harness ran on baseline `dd0ee52` and on the
query-local JSONB and comparator implementation.

| Activity path | Baseline p95 | Updated p95 |
| --- | ---: | ---: |
| Date sort | 351.0 ms | 372.1 ms |
| Quantity sort | 563.5 ms | 424.9 ms |
| Price sort | 728.6 ms | 424.8 ms |
| Broad currency search | 2357.2 ms | 558.4 ms |
| Selective exact-price search | 3579.6 ms | 636.7 ms |

The 150 ms p95 investigation target distinguishes paths that need additional
work. Query-local JSONB and direct decimal comparison still leave the sort
queries scanning candidates to build the first page. Prioritize an indexed date
order for the default Activity view and Kind-filtered views, then exact quantity
and price orders for the tested numeric columns. Their keys need to preserve unit groups, nulls last
in both directions and stable ID ties. Measure other sort fields on fixtures
with enough matching values to make the result meaningful.

Search needs separate work. A selective exact-price substring still examines
the full candidate set, so test an index that can narrow substring candidates
and recheck the existing matches. For broad matches, reduce the work of
building searchable fields and exact counts before judging the value of a sort
index. The browser fixture measures a warm, idle warehouse on loopback. Cold
starts, concurrent synchronization and remote browser latency require separate
measurements.

### Activity Kind filtering

Run `mise run performance:activity-browser kind` to measure the Kind selector
with the default date order on a separate 20,000-row fixture. It copies one
published projection per family and varies the dates. The fixture contains
12,000 trades, 5,000 cash entries, 2,000 security transfers, 900 lifecycle
events and 100 conversions. Each timed selection starts from All kinds.
Clearing the filter starts from Trade. The test triggers the select element's
change event and uses the same render timing as the sort benchmark. It checks
the URL and displayed result count after each selection. Append a selection
such as `kind all` to run one case by itself.

The full run completed 30 samples for each named Kind before its overall
timeout during the clearing case. A separate run collected 30 clearing samples
on the same fixture. The task now allows six minutes for the full run.

| Selection | Matches | p95 |
| --- | ---: | ---: |
| Trade | 12,000 | 697.1 ms |
| Cash | 5,000 | 599.0 ms |
| Security transfer | 2,000 | 556.6 ms |
| Lifecycle | 900 | 513.6 ms |
| Conversion | 100 | 494.7 ms |
| All kinds | 20,000 | 1025.2 ms |

A schema-only plan probe in SQLite 3.51.2 showed that the optional Kind
predicate kept the publication scan with a standalone `executions(kind, id)`
index. A dedicated Kind equality predicate used that index for the candidates.
Both plans still sorted the matching rows and scanned the unfiltered total.
Test a Kind-plus-date index with a dedicated filtered query alongside the
global date index. Measure the counts and page together against the 150 ms
target, including both a common kind and a rare kind.

### Indexed date order and Activity rendering

The date indexes cut a sampled Kind request to about 11 ms on loopback,
including both counts. Chromium's performance metrics still showed roughly
350 ms of style recalculation on the 50-row grid. Tabulator inserted resize
handles into cells between row-height reads. Moving those handles to the
headers reduced layout passes from 56 to 7 in a short trace. Sizing Activity
rows in CSS let Chromium defer work on rows outside the viewport while keeping
the loaded page available for scrolling, focus and selection.

Run `mise run performance:activity-browser sort date` and
`mise run performance:activity-browser kind` to reproduce the full interactions.
The first run alternates date directions on the 20,000-trade fixture. The second
uses the 20,000-activity mixed fixture and includes the matching and total counts.

| Interaction | Previous p95 | Indexed and rendering p95 |
| --- | ---: | ---: |
| Date sort | 372.1 ms | 75.9 ms |
| Trade | 697.1 ms | 48.0 ms |
| Cash | 599.0 ms | 48.2 ms |
| Security transfer | 556.6 ms | 49.3 ms |
| Lifecycle | 513.6 ms | 47.6 ms |
| Conversion | 494.7 ms | 49.3 ms |
| Clear to All kinds | 1025.2 ms | 50.0 ms |

The final runs used 30 samples per interaction in Brave on loopback. The
results cover date ordering with a Kind selection or no selection, on the
default 50-row page.

### Activity Kind and search during ingestion

Run `mise run performance:activity-browser ingest date` and
`mise run performance:activity-browser ingest price`. Each run starts with the
20,000-row mixed fixture and repeatedly processes synthetic IBKR Flex
confirmation reports with 200 new trades each. The new trades use a separate
instrument and dates outside the two active searches. The broad search
`2026-` matches every seeded activity. The selective search `2026-12-31`
matches about 1%. The browser retains the submitted search and descending Date
or Price / rate order while cycling through Kind selections and clearing to
All kinds. Each case measures 30 warmed selections to a rendered 50-row first
page. The benchmark verifies the filter and sort state, the matched count and
the total count in whole committed batches.

Sharing the writer connection delayed reads behind processing. A dedicated
reader removed that wait during ordinary ingestion work. Broad queries still
spent time extracting and folding the same published fields. A derived index
stores those fields and the exact price text. The read still applies live labels
and sorts prices with the decimal collation. The CPU profile also showed
SQLite recompiling the prepared statement when Kind changed. Binding Kind
through a one-row relation retained the index seek and let SQLite reuse the
compiled statement.

Brave 157.1.99.56 on loopback measured the following p95 values on 2026-11-11.
Every case passed the 150 ms target. Each run committed 1,600 additional trades,
and 719 of 720 timed grid requests began during ingestion.

| Selection | Date, broad | Date, selective | Price / rate, broad | Price / rate, selective |
| --- | ---: | ---: | ---: | ---: |
| Trade | 89.7 ms | 86.0 ms | 105.3 ms | 86.6 ms |
| Cash | 75.6 ms | 54.4 ms | 73.7 ms | 62.2 ms |
| Security transfer | 50.8 ms | 51.8 ms | 63.4 ms | 66.4 ms |
| Lifecycle | 48.9 ms | 49.2 ms | 51.5 ms | 50.4 ms |
| Conversion | 49.0 ms | 49.1 ms | 60.9 ms | 53.9 ms |
| Clear to All kinds | 115.3 ms | 100.7 ms | 118.2 ms | 106.5 ms |

The results cover a warm local warehouse and this report size. Searches still
scan candidates to calculate exact counts, and the reader shares SQLite's
rollback-journal lock at commit. Larger warehouses, longer write transactions
and first-open index build time need separate measurements.

## Financial contract variants

On 2026-11-11, rerunning the synthetic workloads on the Core Ultra 5 228V Linux host
with Go 1.27.4 produced the following comparisons against the flat contract at
`009316a`. The byte count sums serialized resources and excludes the outer snapshot
envelope. The changed sync column uses the median of five observations for the
representative read and one observation for the other workloads.

| Workload | Snapshot resource bytes | Materialize snapshot | Changed consumer sync | Peak RSS (KiB) |
| --- | ---: | ---: | ---: | ---: |
| Quick | 1,132,713 → 1,087,713 | 9.4 → 13.4 ms | 0.359 → 0.349 s | 156,660 → 159,340 |
| Representative read | 22,520,608 → 21,620,608 | 147 → 195 ms | 5.82 → 5.95 s | 363,088 → 358,768 |
| Large read | 112,600,608 → 108,100,608 | 0.767 → 0.939 s | 26.23 → 26.68 s | 1,863,036 → 1,528,200 |

Resource bytes fell by about 4% in these trade-heavy fixtures. The server spent more
time materializing the typed variants. Its allocations for a changed sync rose from
207 to 237 MB in the representative read and from 1,011 to 1,158 MB in the large read.
The consumer still completed the large read within the existing 128 MiB response bound.
These are local comparisons of the specified fixtures. The RSS samples do not measure
combined server and consumer memory or establish a general capacity limit.
