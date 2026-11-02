# Financial language

These meanings guide product decisions. They do not prescribe Go types, SQL tables
or wire fields. [The product brief](brief.md) owns intent, the
[current coverage](../product/coverage.md) describes supported behavior, and the
[README](../../README.md) provides descriptive usage help.

## Identity and grouping

| Concept | Meaning |
| --- | --- |
| Institution | The real financial institution holding an account. |
| Provider | The service or format through which Wake obtains evidence. It may differ from the institution. |
| Connection | Configured access to a provider, distinct from the financial account it reports. |
| Discovered account | Source-reported account information awaiting operator selection and confirmation. |
| Account | Wake's identity for a real financial account, independent of its connection, provider or source IDs. |
| Party | A beneficial owner or controller. An account need not represent one person. |
| Portfolio | A user-defined grouping or view; it does not redefine ownership or provenance. |
| Instrument | A financial asset or specific contract, independent of its source representation. |
| Listing | A venue representation linked to one root instrument. It is distinct from execution and routing venue. |
| Source identifier | A reported reference, qualified by issuer, scheme, role and scope. It is not Wake's primary identity. |

Wake identities survive source changes. Ticker text, a matching name or an opaque
broker ID alone does not establish financial identity. Grouping several objects
does not make them the same object. A connection or query change does not create
a different real account or instrument.

### Account source authority

Each account has one operator-designated authoritative connection, provider and
stream, bound to the reported source account. Discovering a candidate or installing
a provider does not grant that authority. Account confirmation establishes the
financial account and binding together; repeating it must not create another account.

Source authority governs broker financial activity. OpenFIGI has a separate role
for instrument identity/naming, and operator assertions remain attributable in their
own right. Authority never bypasses identity, currency or unit checks, erases
conflicts, or silently switches when a source fails. Prior bindings and accepted
history remain explainable after an explicit authority change.

### Instruments and representations

An equity or fund root represents one share class. Listings link to that root;
the root need not enumerate every venue-level identifier. A trade's optional
listing must belong to the same root as the trade. ISIN and share-class FIGI are
useful together but are not a universal one-to-one hierarchy.

Verified share-class FIGIs establish equity/fund identity; supported US equity
options use composite FIGIs and debt issues use instrument FIGIs. Venue FIGIs
identify separate representations. Parent relationships establish grouping even
when a lookup returns many rows or lacks a description. Explicit contradictory
parents or subjects remain reviewable; repeated agreeing rows cannot outweigh them.

Providers interpret reported identifier fields using documented semantics or a
reproducible observed convention, and retain the original field attribution.
That classification remains a source claim, which reference evidence may
corroborate or contradict. Leave the level unclassified when neither basis exists.
A composite or share-class FIGI does not identify a particular listing; an absent
venue FIGI does not make the instrument's established identity unresolved.

Wake automatically combines entries with the same verified identity when active
operator choices agree. The oldest Wake ID survives, old IDs remain aliases, and
original bindings, observations and decisions remain attributable. Conflicting
active choices keep entries separate until review. Matching names, tickers or
financial terms alone never authorize combination.

An equity option is one specific contract, linked to its equity/fund underlier.
The underlier's identifier does not identify the option. Contract adjustments can
preserve identity while changing terms. A premium multiplier is not a deliverable;
an unknown deliverable must not become an assumed 100 shares. Historical activity
retains the terms used to interpret it. Instruments describe identity; option and
debt terms belong to dated observations and accepted activity. Different terms on
distinct transactions do not dispute their shared identity. Conflicting financial
terms for the same activity still require resolution.

Treasuries and gilts represent individual debt issues. Nominal quantity, coupon,
maturity, reported price and accrued interest have separate meanings. Wake retains
the reported debt price without asserting a quotation basis or deriving principal
from quantity and price. Principal, proceeds, trade net cash, accrued interest and
charges remain independent reported roles; trade net cash does not establish a
combined settlement total. A reopening
or a different venue representation does not by itself establish a new issue.
Distinct rights, depositary receipts, share classes and contracts remain distinct.

Supported global identifiers, including ISIN, CUSIP, SEDOL and FIGI, are accepted
at their actual subject/allocation level.
Source-local references stay qualified links. Unverified or disputed identifiers
remain assertions rather than accepted master data. Resolve a reported venue FIGI
to its appropriate root relationship; do not relabel it as a share-class identifier.

OpenFIGI supplies default ticker and name spelling after a suitable reference is
selected. A share-class result can contain several venue names/tickers; the first
row is not a naming policy. Ambiguity retains a provisional/current label. A new
listing does not silently replace an established naming selection. An operator
correction survives replay and reference refresh until superseded or
released. An accepted identity survives lookup failure, a later no-match and cache
expiry; those describe refresh health rather than a changed identity. Missing usable
identifiers wait for new evidence instead of repeating unchanged lookup work.
There is no separate reporting-symbol identity.

## Evidence and accepted views

An evidence artifact is the original retained report or reference response with
its context. A source record locates reported content within that artifact.
Normalization expresses its financial meaning as a source claim; it does not
make that claim an accepted Wake conclusion.

Each successful acquisition records a receipt with its retrieval time, requested
and returned coverage, and actual byte digest. Receipts can reference the same
retained report body. Exact duplicates reuse bytes; a provider may explicitly
version a comparison policy that ignores only report-window fields. Other byte
changes retain a new body even when their financial claims are already known.

A standardized source claim is independent of its structural report location and
of later corrections or reference enrichment. Preserve each appearance and duplicate
row multiplicity. Known claims add provenance without recalculating accepted state;
an operator's corrected value is never the baseline for recognizing a repeated
source claim. Exact values, time precision, qualified identity and unknowns remain
part of that source comparison.

Linking establishes identity and relationships. Reconciliation selects an accepted
view while preserving dissent. An operator decision records who chose what, why
when supplied, and the decision's scope. Corrections add information rather than
rewrite evidence. A wrong link can be superseded without repurposing an old ID.

Provenance, reference evidence, field selections, named/versioned rules and decisions
are linked explanations. Ordinary financial views expose useful values and compact
uncertainty/conflict status; users need not traverse the evidence to learn that a
problem exists. A missing enrichment, a service failure and a contradiction are
different outcomes.

## Financial activity and values

A trade is a purchase/sale at its reported grain. Exchange executions, booked stock
trades, option exercise/assignment/expiration and order membership have distinct
meanings. An order groups fills or legs; it does not merge their identities.
Reported lifecycle relationships do not authorize invented stock deliveries or cash.
An expiry date alone does not establish an outcome.

A cash asset identifies account and currency without asserting a balance. FX activity
preserves reported currencies and amount roles; missing legs stay missing. Attached
charges retain their reporting scope. A summary fee is not copied onto each fill.

Keep exact reported values, units, quotation meanings, currencies and time precision.
Retain trailing decimal scale. Numerically equal observations with different scale
remain compatible; their accepted selection keeps its reported text.
Missing, zero and inapplicable differ. A date is not an invented midnight instant.
Price, gross amount, net cash, accrued interest and charges are not interchangeable.
Calculations and rounded displays do not replace source values.

Charges are compared by their reported field and total/component scope. Reordering
or omitting a charge role does not establish disagreement. Different monetary
values or currencies for the same reported role remain conflicting evidence.

Establish identity through explicit corroboration before combining fields. Weighted
confidence cannot outweigh a strong contradiction. Repeated observations must not duplicate
activity. Compatible evidence can fill a missing value; incompatible claims remain
visible for resolution. Arrival order is not source revision order, and absence
from a later report is not cancellation. Explicit cancellation/replacement preserves
the original history. Core identity and operator-created trades do not require
fabricated broker identifiers or reports.

## Events, observations and continuity

Activity records something that happened. A reported position, balance or valuation
is an observation, not an explanation of how it arose. Derived state is a calculation
from retained information. Never invent an event to make observations and calculated
state agree. Tax-relevant source facts differ from a user's tax treatment.

Changes to accepted values, links, relevant reference data or conflict status matter
to consumers even without a new trade. Stable identities and reproducible views
support restart and recovery. Recalculating a view is different from reconstructing
missing financial history, and explicit warehouse disposal is different from
reinterpreting retained evidence.


Sources explicitly qualify whether an event reference identifies one event within
its authority, kind and scope. Native reference kind names carry no implicit core
policy. Shared aliases can corroborate an observation without collapsing distinct
fills; competing possible matches remain reviewable.

A source cancellation marks its identified activity cancelled without deleting it.
An explicitly identified replacement with a distinct source event receives a new
Wake identity and a link to its superseded activity. Missing targets remain pending;
competing instructions, ambiguous identities and cycles require review. Supported
instructions never acquire authority from arrival order. A retained baseline anchors
selection; mutually compatible observations may fill absent optional values. When
those optional claims compete, the baseline remains visible with disagreement.


An active operator decision controls only its selected fields or link. Superseding
or releasing a decision appends attributable history; release returns that scope
to the retained automatic selection. Selecting financial fields from an observation
does not silently change instrument or listing identity. Decisions preserve dissent,
and stale forms must not overwrite decisions made after the form was read.

Compatible observations contribute charge roles in a deterministic field/scope order. If combining individually valid facts exceeds a constructor bound or produces invalid debt terms, retain the durable baseline with visible disagreement and preserve all supporting observations. Conflicting shared instrument terms mark the automatic instrument selection for review without replacing its reported values.

A retained automatic FIGI selection remains an identity claim while an operator override is active. Competing automatic or operator selections cannot acquire that FIGI for another root; conflicts remain visible and releasing the override can restore its retained selection.

Reported deliverables compare component multiplicity and exact quantities/cash independently of list order. Equivalent reordered components do not establish disagreement; accepted selection preserves the durable baseline order.

Correction preflight accounts for active dependent listing decisions before accepting a different instrument. Candidate comparisons include read-only attached charges with their reported role, total/component qualification, exact amount and currency, including differences that selecting the candidate would replace. Charges remain outside editable field scopes.

Evidence comparisons also retain read-only option effective dates and deliverable components (identities, multiplicity, exact quantities and cash currencies), plus debt type, currency, issue and dated dates, and payment frequency. These terms remain visible independently of editable correction scopes.

Read-only candidate comparison preserves qualified source account, event, order and alias references, reported relationships, trade kind and reported instrument/underlier identities with their identifier schemes and values. Canonical underlier-link decisions remain separate from the reported underlier. Reference candidates compare the accepted FIGI identity selected for the instrument family (including share-class FIGIs for equities), independently of matching names or tickers. Unknown currencies render as Unknown.

Retained instrument, underlier and listing choices include their stable Wake identities in both selectable and current-value labels, so matching names or symbols remain distinguishable and searchable before a link decision is saved.

Published active-decision explanations contain only currently applied overrides. Releasing a scope returns its authority to automatic selection and removes it from the active explanation; the full decision and release history remains retained.

Correction preflight evaluates the complete prospective set of active decisions
against retained source inputs and current bindings, using the same financial
calculation as saving and release. A successful check writes no financial or
operational state. Saving still checks the resource revision in its transaction;
concurrent changes require a fresh review. Releasing a scope recomputes its
automatic value from retained claims while preserving other active decisions.
