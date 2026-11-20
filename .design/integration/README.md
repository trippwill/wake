# Source Reader build review

Wake’s redesigned admin opens in Steel & Copper with system typography and follows the device’s
light or dark preference. Settings lets the operator change the palette, mode, or typography
independently. Both palettes use the same report reader, trade details, Overview and
synchronization actions.

The [contest report and Taste critique](../review-report.md) records the three proposals. The
screenshots below show the integrated build with synthetic accounts and reports.

## Visual comparison

| Matched captures | What to inspect |
| --- | --- |
| [Steel & Copper before and after](steel-comparison.webp) | The incumbent and redesigned Overview use the same fixture, viewport and device mode. Nine sampled color roles matched in both modes, including canvas, text, copper actions and links, and status colors. |
| [Eight appearances](eight-appearances.webp) | The same Overview rendered in each palette, resolved mode and type treatment. Compare the headline and numerals against the navigation and control labels. |
| [Desktop workflows](desktop-workflows.webp) | The affected record and latest receipt, the first MSFT fill, the bill’s exact terms, and the three appearance controls at 1360 × 900. |
| [Phone workflows](phone-workflows.webp) | The same four workflows at 390 × 844. A report row groups the fill’s identity, terms, effect and record link vertically. |
| [200% browser zoom](zoom-workflows.webp) | A 1280-pixel browser window resolved to a 640 CSS-pixel viewport. The focus outline identifies the active link or setting. |
| [Saved price correction](saved-correction.webp) | The report shows the original 450 USD alongside the accepted 402.0000 USD, the operator correction and the retained source row. |

## What passed

- A fresh browser followed the device in Steel & Copper with system sans. Stored Light and Dark
  choices survived. Palette, mode and typography applied before styles on sign-in and
  authenticated pages. Reload, device changes, open-tab updates and an explicit font override
  preserved the intended settings.
- The eight combinations passed on Overview, Settings, the selected report entry and trade at
  all three viewport treatments. Across 96 page checks the body stayed within the viewport, the
  exact bill price fit on one line on the entry and trade, and keyboard focus remained visible.
  The lowest measured Overview assessment text contrast was 6.01:1.
- The two MSFT fills shared the price **400.123456789 USD**. Broker trade IDs **900000001** and
  **900000002** and their execution IDs distinguished them. Following each link opened the
  matching original row and accepted record. Back restored focus to the originating link. Grid
  filters and selection survived the round trip.
- Both Treasury bill confirmations reached the same trade. The report and trade agreed on
  **10000 nominal**, **99.1234567 USD per 100 face value** and **9912.35 USD reported
  principal**. The repeated entry identified its earlier appearance.
- The saved correction kept both prices readable. Quantity, gross amount and trade date were
  paired with their accepted values at desktop and phone widths. Go cases exercised differing
  nominal quantities and principals, field-specific realized amounts and dates, and a currency
  mismatch. An unlinked entry retained its source context.
  With synchronization unavailable, retained reports and fills stayed readable. Those journeys
  also passed at 390 px and 200% zoom.
- Pending processing, failed processing, a processed report with zero activity and a search with
  zero matches each had distinct feedback. Sync tests covered eligible work beyond Overview’s 50
  displayed sources, paused acquisition, missing credentials and provider cooldown.

## Large-history timings

The synthetic warehouse held 1,204 retained artifacts, 20,458 retained records and 20,455
report-entry appearances. One Treasury bill appeared 1,203 times. Measurements used one
authenticated Chromium client and a local daemon on loopback. Each row reports the first timed
request or navigation and the median of five immediate repeats. The machine and operating-system
caches were already in use.

| Operation | First timed run | Median repeat |
| --- | ---: | ---: |
| Open a report until its 17 entry rows are usable | 267 ms | 168 ms |
| Open a trade until its first page of report appearances is usable | 300 ms | 245 ms |
| Retrieve report HTML | 323 ms | 1.9 ms |
| Retrieve report-entry data | 98 ms | 60 ms |
| Reverse lookup of the trade’s appearances, first 50 rows | 108 ms | 112 ms |

The browser timings include navigation, data retrieval and grid rendering. The HTTP rows measure
the endpoints separately and use a different first request. The report HTML was 9,514 bytes. Its
31,343-byte original document was available on demand. These measurements characterize a history
with many overlapping small reports. They do not establish timings for a single very large
statement, a remote client or other browser engines.
