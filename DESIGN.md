---
name: Wake admin UI
description: Wake report reading, source inspection and browser appearance standards.
colors:
  steel-and-copper:
    canvas: "#EDF0F2"
    canvas-dark: "#2B3036"
    ink: "#293039"
    copper: "#DC8E57"
    link: "#805028"
    link-dark: "#E6A675"
  source-reader:
    paper: "#F8F8EF"
    paper-dark: "#15241E"
    masthead: "#18342D"
    masthead-dark: "#0F211C"
    navigation-selected: "#D9EF89"
typography:
  system:
    fontFamily: "system-ui, sans-serif"
  editorial:
    fontFamily: "STIX Two Text, Georgia, serif"
  body:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1rem"
    lineHeight: 1.5
rounded:
  field: "0.25rem"
  page: "0"
---

# Wake admin design

## Reading sequence

Wake opens with the state of the accounts, then the reports that support that
reading. Put the affected record and latest report together, followed by each
stream’s last successful retrieval and next attempt. Keep a repair beside its blocker.
Identify the source, the period it reported and any
decision still to make. Keep retrieval time, processing and the effective time
of positions distinct. A quiet opening needs supporting facts.

The core interaction is an annotated report. Enter through a current record or
through a source. Keep the selected entry, its exact terms and its relationship
to the record together. Open the original content for a closer reading. On a
narrow display, place the current record immediately after the selected terms
so that checking a difference takes precedence over scrolling through raw data.

## Identity and assets

The name evokes a ship's visible wake. Diverging water trails and a lowercase
geometric wordmark form the identity. Spell the product **Wake**. The application
chrome uses the logo on its own, without a tagline.

Use the [primary logo](internal/transport/admin/assets/brand/wake-logo.svg) on light
surfaces and the [reversed logo](internal/transport/admin/assets/brand/wake-logo-reversed.svg)
on dark surfaces. The [mark](internal/transport/admin/assets/brand/wake-mark.svg)
serves compact contexts where Wake is already identified. Preserve proportions,
keep the mark upright, and allow clear space of at least one quarter of its width.
Use the complete logo at 120 CSS pixels or larger; use the mark/favicon below that.
Avoid stretching, shadows and gradients. The logo's accessible name is Wake and
never substitutes for a page title.

Keep the [SVG favicon](internal/transport/admin/assets/brand/favicon.svg),
[ICO fallback](internal/transport/admin/assets/brand/favicon.ico) and
[touch icon](internal/transport/admin/assets/brand/apple-touch-icon.png) coherent
with the mark. Export raster variants from the vector source.
Assets stay local and embedded; no external font/icon service is needed.

## Type, color and space

Steel & Copper is the default theme. Use steel surfaces, underlined copper links,
copper primary actions, sage success, ochre warning and clay errors. Source Reader
uses an evergreen masthead, a citron navigation mark and ruled paper surfaces.
Both themes provide distinct light and dark roles for surfaces, text, controls,
selection and browser chrome. New themes supply these roles to the same components.
Keep an outline or edge alongside the selection color and a label alongside each status.

System sans is Steel & Copper’s default typography. Source Reader uses locally
embedded STIX Two Text for editorial headings and featured financial values.
Keep controls, table labels and supporting text in system sans. Give reported
and accepted values equal typographic weight, preserve exact precision and use
tabular numerals for comparisons.

Settings offers independent Theme, Mode and Typography choices. A fresh browser
uses Steel & Copper, System mode and Theme default typography. An explicit font
choice persists across theme changes. System mode tracks the device in either
theme. Apply preferences before first paint on sign-in and authenticated pages,
keep open tabs in sync, and honor reduced motion.

## Navigation and workspaces

The horizontal masthead gives direct access to Overview, Reports, Activity,
Instruments, Positions and Needs review. Connections and System group the
operator controls. A keyboard-dismissible Menu replaces the full navigation on
smaller screens. Keep session contact and sign-out reachable there in the lower
bar. Contact describes the browser's connection to Wake. Give an expired session
a visible sign-in action.

Reports lists receipts across sources. Lead with receipt time, the report's
financial contribution and its reported period. A repeated report may refer to
a body Wake already has. Identify the retrieval separately. Activity,
Instruments and Positions support independent financial browsing. Lead an
instrument with its ticker when available, followed by its description.
Positions presents each account set's availability and reporting time. Needs
review identifies decisions the operator can make.

Connections owns account authority, synchronization, source-specific reports,
access and recovery. Favor maintenance of existing sources before new setup.
System owns installation status, configuration and settings. Show the loaded
TOML or its absence. Show storage size in binary units and exact bytes, scoped
to the financial file.

Use ordinary links for drill-down. A direct link has a meaningful canonical
home. In one tab, retain the originating workspace, applied filters and return
trail when opening a record, a report or a correction. Back, Forward and reload
preserve that context. Store navigation metadata only. Keep large financial
tables locally scrollable. Bounded forms, readable margin notes and ruled
sections carry the same visual language through sign-in, setup and recovery.

## Interaction and live feedback

Use native labeled fields, buttons, disclosures and validation. Keep visible focus,
skip navigation, usable touch targets and keyboard dismissal/return focus.
Administration requires JavaScript. Navigation uses real links with visible focus
and keyboard access. Empty states explain what exists and offer an applicable action.

Activity should be purposeful and visible. Show recorded scope and elapsed time;
leave unknown totals indeterminate. Acquisition, processing, freshness and financial
conflict labels retain their separate meanings. Quiet contact feedback must not
mask failed refreshes or sign-in requirements. Animate running work or meaningful
changes; keep unchanged and waiting/blocked states steady.

Preserve drafts, focus/caret, selections and disclosures through live updates.
Apply category selections immediately and group the Search button with its text
field. A category change keeps any search draft and filters with the submitted
query. Keep the previous results visible during the request. Successful filter
changes start at page one and retain the current sort and page size.
Retain intentional filter, ordering, page and page-size state in URLs, and selected
column layout and page-size preferences through Tabulator persistence. Restore column order,
widths and visibility while taking headings from the current application. Remember
page size separately for activity, positions, instruments and retained observations in
the current browser. A supported URL size takes precedence. When returning from a
financial record to a selected report entry, restore the reader’s place. Native
submissions give pending feedback and prevent accidental duplicates. Financial history search,
filtering, sorting and pagination operate on the complete
matching warehouse set, with 50 rows per page by default and a Rows per page selector
offering 10, 25, 50, 100 and 200. Bound other evidence
views explicitly and clearly label any local interaction scope.
Use compact vertical cell spacing and let grids grow with the viewport so more
rows fit on screen, while keeping full values readable and scrolling local to the table.
Keep filters, column headings and pagination visible while the current page's rows
scroll beneath them, including after resizing the viewport. Compact the filter and
status area, and disclose keyboard guidance on request, to preserve useful row space
at laptop heights. On very short screens the table workspace may extend beyond the
viewport; keep its rows and pagination reachable through their local and page scrollers.
Use embedded htmx and its SSE extension for authenticated updates to independent
source/status regions. Keep commands ordinary HTTP submissions. Tabulator owns
interactive grid DOM and interactions, loading authenticated JSON and checking for
changed results on SSE hints or polling. Keep displayed rows while downloading;
unchanged responses leave the current rows in place. When results differ, announce
available updates and offer an explicit refresh; apply them only at the user’s request.
Render each interactive dataset once;
parallel native tables, DOM scraping/cloning and htmx grid morphing are prohibited.
Protect active/dirty forms and selections. Accepted detail pages, decision forms
and immutable raw-evidence pages are explicit read snapshots with refresh/navigation
actions; their contact feedback remains live. Retained observation grids refresh
independently. Do not cache financial page HTML in browser history storage.
Use the pinned, embedded Tabulator distribution for activity, instrument, positions and
observation datasets. Filtering, ordering and pagination use bounded server queries,
with 50 rows per page by default. Preserve keyboard sorting/resizing, focused records,
visible loading and clear error/retry or sign-in states. Compare decimal values on
the server without floating-point conversion, keeping currency and unit distinctions.
Short tables and decision controls remain native; exact, unrounded choices remain
available in every appearance.
Positions presents reported time and availability at account-set level, with exact signed
quantities, explicit units and long/short wording. Label reported per-unit cost basis as
“Unit cost basis”. Use “Not provided” for missing amounts and preserve explicit zero. Contract
disclosures show reported option/debt terms without substituting current instrument terms.
Use “Not provided” for applicable contract terms that were not reported.
Refresh the metadata and rows together, and distinguish an outdated displayed report from the
latest available set.

## Evidence and decisions

Lead review pages with readable account, investment and date context. Show the
important differences beside the choice on wide screens, then stack them on narrow
screens. Keep complete source and accepted values aligned, with explicit difference
labels and local scrolling. Preserve all candidates and exact terms.

Separate field correction, retained-evidence comparison and decision management.
Keep the chosen field's accepted value beside its editor, compare differing fields
first and disclose unchanged fields and technical context. For debt issue terms,
use “Not provided” for absent values. Show no coupon payments for Treasury bills;
omit absent coupon-only terms while retaining explicitly reported values, including
zero. Listing tables distinguish an unestablished venue FIGI from resolved root
identity. Evidence disclosures expose separately labelled, readable request and
response content through one keyboard-accessible action.
Use field-specific controls: calendar dates, exact decimals with visible units and
currency, explicit enumerated choices, and searchable retained-record selectors
for links. Filter link choices by compatible family and parent instrument. Preserve
fractional seconds and timezone information when editing timestamps. Check syntax
as the user edits and check financial constraints against the current record before
enabling save; checks do not retain decisions or advance revisions. Keep native
field selection and ordinary HTTP form submission explicit, with in-place controls
and preflight feedback provided by JavaScript.
Require an explicit choice; disclose optional reasons and retain them after errors.
Report entries identify the trade, broker trade ID and execution ID. On narrow
screens group identity, exact terms and effect vertically. Include the price basis
for debt quotes wherever they appear. Keep the
interpretation rule, version and locator available on the selected entry.
Use a paginated report index and keep the selected entry separately addressable.
Preserve its report identity, interpretation version and location. Keep original
content available alongside readable financial terms. When an entry has several
appearances or lacks a settled match, describe its actual relationship.
Account-level positions links lead to the source's reports. Table column
preferences for reports remain separate from other observation tables.
Keep evidence/history reachable and distinguish saved decisions from checks still
in progress. Use named source/context labels before technical references.

## Verification

Review both palettes in light and dark mode with each type treatment. Check
narrow layouts, keyboard use, feedback and evidence comparisons
with synthetic data. Inspect logos on their intended surfaces and favicons at
small sizes after asset changes. Screenshots supplement structure/data checks;
they do not establish full accessibility certification. The
[development guide](.docs/engineering/development.md#browser-and-frontend-verification)
explains check selection.
