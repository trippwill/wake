# OpenFIGI identity levels and naming

Resolving an equity share class can produce many market representations and ticker
strings. Identity grouping and choosing a display name are separate decisions.
An identifier's source field name does not establish its FIGI allocation level.

Checked 2026-10-25 against public documentation and one anonymous API request.
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
