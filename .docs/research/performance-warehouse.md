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
