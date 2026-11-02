# Which FIGI level does IBKR report?

The inspected IBKR Flex exports consistently report composite FIGIs for stocks
and US equity options, and an issue-level FIGI for the represented government bond.
That is sufficient experimental support for the provider to assign those typed
identifier fields directly. It need not defer every classification until a lookup
or require an explicit vendor guarantee. Contrary evidence should prompt a focused
provider correction while retaining the original reports and source attribution.

The adopted [IBKR interpretation](../providers/ibkr-flex.md) owns this policy.
[Financial language](../product/domain.md#instruments-and-representations) keeps a
provider's interpretation distinct from reference corroboration and accepted identity.

## Experiment on 2026-11-02

Read-only inspection covered private maximum-field Confirmation and Activity Flex
XML exports. Extract every nonempty `figi`, retain its `assetCategory` and
`subCategory`, and deduplicate by exact FIGI across both exports. The reports overlap:
their repeated appearances are not independent identifier samples.

Map those 56 distinct values using `POST https://api.openfigi.com/v3/mapping`,
`Content-Type: application/json`, no credentials and no filters. Each job contains
only `idType=ID_BB_GLOBAL` and the exact reported `idValue`. Six sequential requests
of at most ten jobs returned HTTP 200 within the documented anonymous rate limit.
Responses align with request order. Compare each reported value with the explicit
`figi`, `compositeFIGI` and `shareClassFIGI` fields in every returned row; examine
asset classification where the equity hierarchy is inapplicable.

| Source family | Distinct reported FIGIs | Observed allocation | Exceptions |
| --- | ---: | --- | ---: |
| Stocks: nine common stocks and one ADR | 10 | Composite | 0 |
| Equity options: 35 calls and ten puts | 45 | Composite | 0 |
| UK government bond | 1 | Instrument/issue | 0 |

All stock and option results explicitly identified the input as `compositeFIGI`.
The government-bond result identified the input as `figi` in the Government sector,
without composite or share-class metadata, consistent with its debt allocation.
There were no unmatched identifiers or contradictory allocation results.

[Aggregate evidence](ibkr-figi-allocation-evidence.json) records counts, criteria
and conditions without committing private reports or a portfolio's identifier set.
The earlier [public representative responses](openfigi-classification-evidence.json)
include AMD and a Ford option as composites, plus a public Treasury-bill issue.
Those public identifiers can be replayed independently; repeating the complete
56-identifier study requires access to the private exports.

## Documentation and scope

IBKR's [Financial Instrument Information reference](https://www.ibkrguides.com/reportingreference/reportguide/financialinstrumentinformationfq.htm)
names the field but does not specify its allocation level. The
[OpenFIGI mapping contract](https://www.openfigi.com/api/documentation) and
[allocation rules, version 30.0](https://www.openfigi.com/docs/figi-allocation-rules.pdf)
explain the distinct fields and asset-specific hierarchies. The conclusion about
IBKR's source convention is experimental, not a quoted documentation guarantee.

The observed stocks were USD listings on NASDAQ/NYSE, including an ADR; options
reported CBOE as their listing exchange. The bond was a UK government issue.
No ETF identifier was present, and the Treasury-bill sample supplied no FIGI.
Applying the stock convention to supported funds and the issue convention to
supported bills is an adopted inference from the source-family convention and
allocation rules, not an additional sample observation. The current supported
families remain bounded by [coverage](../product/coverage.md).

The samples used the dedicated `figi` attribute, with no FIGI-typed `securityID`.
Applying the same convention when that alternate field explicitly declares FIGI
is also an inference about the same identifier meaning, not a separate observation.

Neither the convention nor a successful mapping supplies missing FIGIs, establishes
a venue identifier from a composite, or replaces dated financial terms. Registry
metadata can corroborate or contradict a source claim; a contradiction remains
reviewable. The field stays unclassified for unsupported families. No operator
store, credentials or running daemon was accessed or changed by this experiment.
