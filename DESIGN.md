---
name: Wake admin UI
description: Steel, Copper and Sage visual rules for Wake administration.
colors:
  base-100: "#FAFBFC"
  base-200: "#B0B6BC"
  base-300: "#8D98A3"
  base-content: "#293039"
  primary: "#DC8E57"
  primary-content: "#293039"
  secondary: "#293039"
  secondary-content: "#FAFBFC"
  accent: "#E0E5E9"
  accent-content: "#293039"
  neutral: "#293039"
  neutral-content: "#FAFBFC"
  info: "#DFE5EA"
  info-content: "#384D5C"
  success: "#D1D8BD"
  success-content: "#3B4A32"
  warning: "#EEE3BA"
  warning-content: "#65501B"
  error: "#EAD9D5"
  error-content: "#713E38"
  wake-canvas: "#EDF0F2"
  wake-link: "#805028"
  wake-sidebar: "#C6CDD3"
  wake-field: "#EDF0F2"
  wake-muted: "#3D4650"
  wake-control-border: "#687581"
  wake-selection-edge: "#805028"
typography:
  headline-narrow:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1.6rem"
    fontWeight: 700
    lineHeight: 1.25
  specimen:
    fontFamily: "system-ui, sans-serif"
    fontSize: "2rem"
    fontWeight: 650
    lineHeight: 1.2
  specimen-narrow:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1.65rem"
    fontWeight: 650
    lineHeight: 1.2
  headline:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1.8rem"
    fontWeight: 700
    lineHeight: 1.25
    letterSpacing: "-0.02em"
  title:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1.15rem"
    fontWeight: 650
    lineHeight: 1.4
  body:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.5
  label:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 550
    lineHeight: 1.5
  button:
    fontFamily: "system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 600
    lineHeight: 1.5
  navigation:
    fontFamily: "system-ui, sans-serif"
    fontSize: "0.95rem"
    fontWeight: 400
    lineHeight: 1.5
  help:
    fontFamily: "system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.5
  status:
    fontFamily: "system-ui, sans-serif"
    fontSize: "0.8rem"
    fontWeight: 400
    lineHeight: 1.2
rounded:
  selector: "0.375rem"
  field: "0.375rem"
  box: "0.625rem"
spacing:
  quarter-rem: "0.25rem"
  half-rem: "0.5rem"
  three-quarter-rem: "0.75rem"
  one-rem: "1rem"
  one-quarter-rem: "1.25rem"
  one-half-rem: "1.5rem"
  two-rem: "2rem"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.primary-content}"
    typography: "{typography.button}"
    rounded: "{rounded.field}"
    padding: "0 1rem"
    height: "2.5rem"
  button-default:
    backgroundColor: "{colors.base-200}"
    textColor: "{colors.base-content}"
    typography: "{typography.button}"
    rounded: "{rounded.field}"
    padding: "0 1rem"
    height: "2.5rem"
  input:
    backgroundColor: "{colors.wake-field}"
    textColor: "{colors.base-content}"
    typography: "{typography.body}"
    rounded: "{rounded.field}"
    padding: "0 0.75rem"
    height: "2.5rem"
    width: "100%"
  navigation-current:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.base-content}"
    rounded: "{rounded.field}"
    padding: "0.375rem 0.75rem"
  status-badge:
    backgroundColor: "transparent"
    textColor: "{colors.base-content}"
    typography: "{typography.status}"
    rounded: "{rounded.selector}"
    padding: "0.25rem calc(0.75rem - 1px)"
  card:
    backgroundColor: "{colors.base-100}"
    textColor: "{colors.base-content}"
    rounded: "{rounded.box}"
    padding: "1.5rem"
  disclosure:
    backgroundColor: "{colors.base-100}"
    textColor: "{colors.base-content}"
    rounded: "{rounded.box}"
    padding: "1rem 1.25rem"
---

# Design System: Wake

## Purpose

DESIGN.md is Wake's visual-design standard. Its structured frontmatter is the
portable design baseline. CSS, assets and templates implement that standard;
inspect their source for exact rendered behavior rather than maintaining another
component inventory. Keep the baseline aligned with approved design changes.
The [product brief](docs/product/brief.md) and [financial language](docs/product/domain.md)
own product intent and meaning.

Wake is a calm, precise workspace for financial evidence. Steel and deep-steel
surfaces, sparse Copper actions and sage-inspired status colors support readable
work. Use system typography, flat bordered regions and native controls. Keep audit
metadata secondary while making uncertainty and the next useful action clear.

## Identity and assets

The name evokes a ship's visible wake. Diverging water trails and a lowercase
geometric wordmark form the identity. Spell the product **Wake** and use
**Financial evidence** as its descriptor.

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

## Color and appearance

Use shared light/dark semantic roles rather than page-specific palettes. Steel
anchors the workspace; Copper emphasizes primary actions sparingly. Body text and keyboard focus use the foreground role. Underlined links use a
Copper text role: `#805028` in light appearance and `#E6A675` in dark appearance.
Filled primary actions retain Copper with dark text; secondary actions use visible
borders and distinct hover/focus states. Selection has a distinct edge as
well as background and text emphasis. Panels, fields and chrome remain distinct
without decorative depth. Both themes retain the same layout and density.

Status combines color, wording and shape. Information uses Steel, success Sage,
warning muted ochre and errors clay. Active/disabled connection states are neutral;
being connected is not proof of successful synchronization. Failure, malformed
and blocked treatments remain visibly distinct. Errors include a next action.
Never communicate a difference or outcome through color alone.

Support Light, Dark and System appearance on sign-in and authenticated pages.
System follows the browser preference. Appearance belongs to the browser, not
source configuration. Keep the choice usable when storage is unavailable. Honor
reduced motion.

## Typography and icons

Use system UI typography and a clear heading hierarchy. Keep financial values
readable with tabular numerals. Exact terms and identifiers remain selectable;
rounded summaries never replace full comparison values. Reserve the logo's custom
lettering for identity and avoid decorative or condensed evidence typography.

The [icon sprite](internal/transport/admin/assets/brand/icons.svg) supplies simple
line icons alongside visible labels. Icons inherit foreground color and are hidden
from assistive technology when decorative. Never substitute an icon for a control's
accessible name or decorate every heading and value.

## Layout and task hierarchy

The first viewport explains the task and shows its relevant status or work.
Keep navigation compact, financial context readable and identifiers secondary.
Lead with connection maintenance before new setup; disclose lengthy forms and
audit metadata on demand. Plain task labels take priority over internal vocabulary.

Use bounded forms, restrained spacing and flat bordered cards to identify tasks,
financial summaries, comparisons and configuration sections. A light steel canvas
(`#EDF0F2`) carries light cards; a deep steel canvas (`#2B3036`) carries dark cards
(`#363D44`). Group related fields within each card rather than treating individual
fields or table rows as separate cards. Keep daemon
contact compact in the header and full work status in page content. Toolbar
preferences remain grouped and reachable when status text wraps. On narrow screens,
stack simple regions, provide dismissible keyboard-accessible navigation and keep
genuinely two-dimensional tables locally scrollable. Avoid whole-page overflow.

Overview orients the operator and surfaces specific next actions. Activity owns
inspection, actionable financial/identity review and instrument browsing.
Connections owns source authority, synchronization, reports and access; System
owns installation status, effective configuration and advanced maintenance.
Full-page details retain breadcrumbs and a return path to their originating view.
Applied corrections and ordinary reference gaps remain distinct from review tasks.

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
Retain intentional filter, ordering and page state in URLs, and selected column
layout preferences through Tabulator persistence. Exact pixel-scroll restoration
is unnecessary. Native submissions give pending feedback and prevent accidental
duplicates. Financial history search, filtering, sorting and pagination operate on the complete
matching warehouse set, with 50 rows per page by default. Bound other evidence
views explicitly and clearly label any local interaction scope.
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
Use the pinned, embedded Tabulator distribution for activity, instrument and
observation datasets. Filtering, ordering and pagination use bounded server queries,
with 50 rows per page by default. Preserve keyboard sorting/resizing, focused records,
visible loading and clear error/retry or sign-in states. Compare decimal values on
the server without floating-point conversion, keeping currency and unit distinctions.
Short tables and decision controls remain native; exact, unrounded choices remain
available in every appearance.

## Evidence and decisions

Lead review pages with readable account, investment and date context. Show the
important differences beside the choice on wide screens, then stack them on narrow
screens. Keep complete source and accepted values aligned, with explicit difference
labels and local scrolling. Preserve all candidates and exact terms.

Separate field correction, retained-evidence comparison and decision management.
Keep the chosen field's accepted value beside its editor, compare differing fields
first and disclose unchanged fields and technical context. Missing dates are Unknown.
Use field-specific controls: calendar dates, exact decimals with visible units and
currency, explicit enumerated choices, and searchable retained-record selectors
for links. Filter link choices by compatible family and parent instrument. Preserve
fractional seconds and timezone information when editing timestamps. Check syntax
as the user edits and check financial constraints against the current record before
enabling save; checks do not retain decisions or advance revisions. Keep native
field selection and ordinary HTTP form submission explicit, with in-place controls
and preflight feedback provided by JavaScript.
Require an explicit choice; disclose optional reasons and retain them after errors.
Observation tables show source location and acceptance rule/version directly in
their cells, with wrapping and local scrolling for long values.
Keep evidence/history reachable and distinguish saved decisions from checks still
in progress. Use named source/context labels before technical references.

## Verification

Review both themes, narrow layouts, keyboard use, feedback and evidence comparisons
with synthetic data. Inspect logos on their intended surfaces and favicons at
small sizes after asset changes. Screenshots supplement structure/data checks;
they do not establish full accessibility certification. The
[development guide](docs/engineering/development.md#browser-and-frontend-verification)
explains check selection. UI polish has its own horizontal delivery slice.
