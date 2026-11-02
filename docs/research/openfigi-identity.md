# OpenFIGI identity levels and naming

Resolving an equity share class can produce many market representations and ticker
strings. Identity grouping and choosing a display name are separate decisions.
An identifier's source field name does not establish its FIGI allocation level.

Checked 2026-11-01 against OMG FIGI 1.3, current allocation rules and anonymous mapping experiments. Earlier equity observations below are retained with their original conditions.
This is supporting research; [financial policy](../product/domain.md#instruments-and-representations)
owns Wake's selected instrument model.

## Documented allocation and lookup behavior

For equities, a share-class FIGI groups composites across countries/markets; a
composite groups trading-venue FIGIs within a country/market. A venue FIGI identifies
the instrument in a particular trading venue. Equity options have contract-specific
identifiers, with composite/venue levels where applicable. US Treasury reopenings
retain the existing instrument FIGI. These are asset-specific rules, not a universal
equity hierarchy for every instrument. See [OpenFIGI allocation rules](https://www.openfigi.com/about/allocation-rules).

OpenFIGI maps third-party identifiers but does not return proprietary third-party
identifiers, citing redistribution restrictions. Consequently, resolving an ISIN
is different from obtaining a complete ISIN/CUSIP master for the result. See the
[OpenFIGI FAQ](https://www.openfigi.com/about/faq).

## Repeated public equity experiment

One POST to `https://api.openfigi.com/v3/mapping`, without credentials or filters,
contained these jobs:

```json
[
  {"idType":"ID_BB_GLOBAL_SHARE_CLASS_LEVEL","idValue":"BBG001S5TD05"},
  {"idType":"ID_BB_GLOBAL","idValue":"BBG000BPHFS9"},
  {"idType":"ID_ISIN","idValue":"US5949181045"}
]
```

| Input | Result records | Distinct ticker strings | Share-class FIGIs |
| --- | ---: | ---: | --- |
| Microsoft share-class FIGI | 297 | 31 | `BBG001S5TD05` |
| Microsoft venue FIGI | 1 | 1 | `BBG001S5TD05` |
| Microsoft ISIN | 297 | 31 | `BBG001S5TD05` |

All three jobs included this same record:

```json
{
  "figi": "BBG000BPHFS9",
  "compositeFIGI": "BBG000BPH459",
  "shareClassFIGI": "BBG001S5TD05",
  "ticker": "MSFT",
  "name": "MICROSOFT CORP",
  "exchCode": "UW"
}
```

The request returned HTTP 200. To repeat it, POST the JSON above with
`Content-Type: application/json`; the [mapping documentation](https://www.openfigi.com/api/documentation)
describes the endpoint. Count `data` rows and distinct `ticker`/`shareClassFIGI`
values per job, then select the row matching the venue FIGI.

## Interpretation and limits

The experiment confirms a venue-to-share-class relationship and multiple ticker
strings for one share class. Taking the first share-class result would make naming depend on
result order. A returned exchange code describes reference context; it does not
establish where an account's trade executed.

The ISIN result corroborates this Microsoft grouping. It does not establish a
universal one-to-one equivalence between ISIN and share-class FIGI. The experiment
used one equity and no source-provider calls; it proves neither brokerage field
population nor option/debt coverage. Counts and labels can change. No API outage,
missing identifier, delisted security or ambiguous identifier was tested here.

## How far can FIGI authority extend?

Verified FIGI relationships support stronger identity matching than equality of
broker identifiers or labels. They do not establish which instrument a broker
meant to report, nor the terms applicable to a historical transaction.

### Documented rules

The [OMG FIGI 1.3 specification](https://www.omg.org/spec/FIGI/1.3/PDF), sections
2.2 and 6.3, defines uniqueness within an instrument's context: a venue identifier
has at most one composite parent, and a composite has at most one share-class
parent. All allocation levels share the same syntax; the string cannot identify
its allocation level. The [overview](https://www.openfigi.com/about/overview)
describes non-reuse and permanence while distinguishing mutable metadata.

The [allocation rules](https://www.openfigi.com/about/allocation-rules) explicitly
provide composite and venue identifiers for US equity options. An option FIGI can
remain unchanged through adjustments to strike or deliverables. This is evidence
against making one set of terms timeless instrument truth. Debt and other assets
have different allocation rules; the equity hierarchy is not universal.

The [mapping contract](https://www.openfigi.com/api/documentation) defines distinct
FIGI, composite and share-class lookup types. Names, classifications and parent
fields may be null. The [terms](https://www.openfigi.com/docs/terms-of-service)
disclaim guaranteed accuracy. Historical names and contract terms are not supplied
by the mapping contract. A successful mapping is not a corporate-action history.

### Observations on 2026-11-01

[Request payloads and compact results](openfigi-authority-evidence.json) retain the
nine initial public-identifier jobs and thirteen follow-up jobs. No account data,
credentials or query filters were sent. Counts are observations, not guarantees.

- Four underlier ISINs returned multiple listings. SLB, AVGO and AAPL each had one
  share-class identity across all rows. WMT had 201 agreeing rows and one missing
  share-class field.
- WMT's incomplete venue FIGI BBG00ZWGCJP9 identifies composite BBG00ZWGCJN1.
  Looking up that composite returned 18 rows: 17 explicitly linked it to
  BBG001S5XH92, and the same incomplete row. This supplies a parent-chain inference
  grounded in identifiers, rather than a vote or name match.
- Five reported OCC symbols each returned 19 venue/composite rows sharing one
  composite. Direct option venue and composite lookups confirmed that relationship,
  including an expired January 2026 contract.
- Microsoft share-class lookup using ID_BB_GLOBAL_SHARE_CLASS_LEVEL returned 297
  rows, 31 tickers and two names, all with one share-class FIGI. ID_BB_GLOBAL for
  that same share-class string returned no match. Typed composite lookup returned
  21 rows while ordinary FIGI lookup of the composite returned one.
- Three Ford option records said securityType2=Equity while securityType=Equity
  Option. All shared the contract's composite. Individual classification fields
  cannot be treated as infallible.
- One matured Treasury from the API documentation resolved without share-class
  metadata, consistent with its different identity allocation.

To repeat, POST each recorded request in JSON arrays of at most ten jobs to
https://api.openfigi.com/v3/mapping with Content-Type: application/json and no API
key. Respect returned rate limits. Count rows, distinct explicit parents, labels
and classification combinations; inspect incomplete records separately. A changed
result should update this memo rather than become an unexplained test failure.

### Engineering inferences and limits

Group by verified allocation and parent relationships, not response row count.
Missing parents can justify a bounded lookup of a known composite. Conflicting
explicit parents must not be hidden by grouping. Unknown names do not invalidate
established identity; names and source associations need their own policy.

Retain accepted identity through lookup outages and negative refreshes; permanence
does not imply metadata freshness or an immutable API response. Financial terms
remain attributable to their dated observations. These experiments did not test
all asset classes, corrected registry assignments, adjusted historical contracts,
service failures or keyed access. They establish neither universal coverage nor
an API availability guarantee. No operator store was modified.

## Classifying a reported FIGI

IBKR's [Financial Instrument Information reference](https://www.ibkrguides.com/reportingreference/reportguide/financialinstrumentinformationfq.htm)
describes the FIGI field without specifying venue, composite or share-class level.
The larger [IBKR sample study](ibkr-figi-allocation.md) establishes the convention
adopted by its provider: reported stock and equity-option FIGIs are composites,
and supported debt FIGIs are issue-level. Security type alone is insufficient,
but it can be combined with a reproducible source convention. The
[allocation rules, version 30.0](https://www.openfigi.com/docs/figi-allocation-rules.pdf),
sections 1.4, 2.2, 2.7.1 and 2.10, establish the corresponding response-field
meanings and the different allocation rules for US Treasury issues.

On 2026-11-02, one anonymous, unfiltered mapping batch reproduced these public
identifiers. [Compact requests and responses](openfigi-classification-evidence.json)
record HTTP status, row counts and representative records; they contain no account
or trade data.

| Input | Observation | Supported interpretation |
| --- | --- | --- |
| AMD `BBG000BBQCY0`, ordinary FIGI lookup | One row; `figi` and `compositeFIGI` both equal input; `shareClassFIGI=BBG001S5NN36` | The reported value is the composite; it is not the NASDAQ-specific FIGI. |
| AMD `BBG001S5NN36`, typed share-class lookup | 246 rows, including the same composite | Share class and composite identify different levels. Row count is not an identity rule. |
| Ford option `BBG01Z053K47`, ordinary FIGI lookup | One row; `figi=compositeFIGI`; no share class | The reported option value is a composite. |
| Treasury bill `BBG000FWG7T8`, ordinary FIGI lookup | One Government/Bill row, no composite/share-class fields | Consistent with an issue-level FIGI under the documented US Treasury rules. |

Classify by exact equality to explicit share-class/composite fields first. An
equity/option `figi` different from its explicit composite can establish a venue
level. Supported debt metadata and its allocation rules support an issue level.
A lone `figi` field without supporting context does not prove venue allocation.
Conflicting roles, contradictory parents, incompatible subjects or unrelated jobs
in the same batch cannot corroborate a classification. Preserve the source field
and value when applying a provider's interpretation; retain requests/responses
that corroborate or contradict that claim.

These four public examples and the larger sample study support classifying the
reported identifiers; they do not
establish that an IBKR listing label matches an OpenFIGI venue. Filling a listing's
FIGI from the composite or the first share-class result would assert an unsupported
relationship. The adopted convention remains subject to contrary evidence; the
experiments do not cover every debt allocation scheme.
