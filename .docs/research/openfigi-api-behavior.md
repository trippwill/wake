# OpenFIGI API budgets and result interpretation

Request budgets and lookup outcomes have separate meanings. A failed request is
not evidence that an instrument has no match, and a batch response needs inspection
at each job's position.

Documentation checked 2026-10-25; one anonymous successful mapping request was also
observed. [Architecture](../engineering/architecture.md) owns Wake's core reference
service boundary; this memo records upstream behavior and its implications.

## Published limits

The [OpenFIGI API documentation](https://www.openfigi.com/api/documentation)
specifies 60-second windows starting with the first request, scoped per API key
or per IP address without a key:

| Budget | Anonymous | API key |
| --- | ---: | ---: |
| Mapping requests per window | 25 | 250 |
| Mapping jobs per request | 10 | 100 |
| Search/filter requests per window | 5 | 20 |

Rate headers expose policy, limit, remaining allowance and reset time. HTTP 429
includes `retry-after`; oversized mapping batches produce HTTP 413. Mapping results
correspond positionally to submitted jobs and can contain `data`, `warning` or
`error`. The documentation uses a warning for no match. Unlisted equities are
excluded unless `includeUnlistedEquities` is enabled.

## Observed successful request

The three-job [Microsoft experiment](openfigi-identity.md#repeated-public-equity-experiment)
returned HTTP 200 and these headers without an API key:

```text
ratelimit-policy: 25;w=60
ratelimit-limit: 25
ratelimit-remaining: 24
ratelimit-reset: 60
```

Three jobs consumed one request in that observed window. The keyed profile,
throttling, invalid credentials and retry behavior were not exercised.

## Engineering implications

These are deductions from the documented contract, not additional API guarantees:

- Callers sharing an IP or key can consume the same upstream allowance; a fresh
  worker or restarted process does not imply a fresh budget.
- Request batching and per-job interpretation need separate accounting. Transport
  success alone cannot establish that every job resolved.
- A cached negative needs successful lookup context. Service failure and no match
  have different evidential value.
- Lookup filters affect the question asked. A negative result with unlisted equities
  excluded cannot establish absence from all reference data.

Recheck the primary documentation when implementing pacing or interpreting new
failure cases. Do not infer a cache lifetime, historical completeness or metadata
redistribution permission from these rate limits.

## Allocation-specific lookup and nullable results

The 2026-11-01 [authority experiment](openfigi-identity.md#how-far-can-figi-authority-extend)
confirmed that ID_BB_GLOBAL is not interchangeable with
ID_BB_GLOBAL_SHARE_CLASS_LEVEL. COMPOSITE_ID_BB_GLOBAL expands a composite's
representations, whereas ID_BB_GLOBAL can return the composite itself. Use the
identifier's corroborated allocation, not a source field spelling or FIGI prefix.
A no-match from the wrong request type does not invalidate a FIGI.

The mapping contract permits absent parent and descriptive fields. Retain the
whole response and distinguish transport outcome, identity selection, descriptive
metadata and refresh health. Successful identity selection must not expire with a
cache entry. No-match remains scoped to the submitted lookup; defaults exclude
unlisted equities. A bounded parent lookup uses the same admission, cache and
retained evidence mechanisms as the original request.
