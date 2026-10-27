# Wake product brief

Wake exists to make financial history trustworthy, explainable, and usable.
It is a standalone, local-first, self-hostable financial evidence
warehouse that takes responsibility for acquiring evidence, understanding source
claims, and reconciling what they mean.

Wake should be useful on its own for inspecting financial history and resolving
uncertainty. Applications that use its data should be able to work with financial
concepts without each becoming an expert in brokerage APIs, source formats,
credentials, pacing, or duplicate detection.

This is Wake's north star: product intent and principles for making decisions as
its capabilities grow. The [domain vocabulary](domain.md) defines the concepts.
The [README](../../README.md) provides descriptive usage help; the
[current coverage](../product/coverage.md) describes supported behavior and limitations. Broader
ambitions here do not imply that a feature already exists.

## Core responsibilities

Wake's job is to:

1. **Acquire and preserve.** Automatically synchronize authorized sources and
   retain their original data and retrieval context. Admit supplied evidence
   through the same preservation boundary.
2. **Normalize.** Map source fields into financial entities, activity,
   observations and relationships expressed as source claims in Wake's vocabulary.
3. **Link.** Apply rules to connect source identities and records to the financial
   accounts, instruments, contracts and activity they describe.
4. **Reconcile.** Apply rules to overlapping or conflicting claims, producing
   coherent canonical views while retaining uncertainty and disagreement.
5. **Apply operator decisions.** Retain attributable, replayable choices that
   resolve uncertainty through linking and reconciliation.
6. **Serve consumers.** Make canonical views available through stable identities,
   a resumable canonical-change cursor, lookups and queries.

Operator decisions feed linking and reconciliation; they are part of the
explanation of the result. Normalized claims remain distinguishable from canonical
conclusions. Changes caused by corrections, rules or decisions must be available
to consumers even when no new financial activity occurred.

## Product principles

- **Wake owns the friction.** Guide operators from access and configuration to
  usable evidence, and through interruptions and recovery. Handle provider
  prerequisites with clear progress, safe defaults, retained input, and useful next
  actions. Automate routine work within existing authority. Ask for judgment when
  identity, disagreement, or new access authority requires it.
- **Evidence stays intact.** Preserve what sources actually supplied and the
  context needed to interpret it. Corrections add attributable information;
  they never rewrite source history. Every accepted conclusion should be
  explainable through its supporting evidence, dissent, rules, and decisions.
  Additional source metadata may remain opaque and source-scoped without a
  canonical equivalent.
- **Uncertainty stays visible.** Distinguish what a source reported, what Wake
  inferred, and what remains unresolved. Acquisition success, processing,
  freshness, and reconciliation answer different questions. Independent work
  should continue while a disagreement awaits judgment.
- **Financial meaning survives source changes.** A real account, instrument, or
  event has meaning beyond the connection used to discover it. Source identifiers
  are scoped claims; a matching name or ticker does not establish identity.
  Adding, replacing, or removing a source must preserve retained history and the
  ability to understand it.
- **Reconciliation belongs in Wake.** Consumers should receive coherent financial
  views and an honest account of their uncertainty. Wake owns duplicate recognition,
  conflicting claims, and explainable resolution. Each account has one operator-
  designated authoritative connection/provider/stream for financial activity.
  Instrument reference data and retained operator decisions have their own roles;
  source authority never erases evidence or uncertainty.
- **Repeated work is safe.** Repeated acquisition, imports, interrupted work, and
  recovery must preserve identity and avoid duplicate financial activity.
  Interpretations and decisions must be reproducible. Improvements to rules must
  remain distinguishable from what was previously believed and why.
- **The user controls the system.** Users choose where Wake runs and retain control
  of their data and access authority. A workstation, home server, NAS, or private
  server should express the same product. Local-first permits remote clients;
  deployment choices should not change financial meaning or create dependence on
  one hosting service.
- **Useful on its own, open to other applications.** Wake helps users inspect and
  understand their financial history directly. Other applications can build on its
  financial identities and views, choosing their own navigation, analysis, and
  presentation. Wake's purpose and core model remain independent of any particular
  client application.

## Financial meaning

Keep institutions, acquisition mechanisms, real accounts, and beneficial ownership
separate. One account may be visible through several sources. An account need not
belong to one person, and a user-defined portfolio does not redefine ownership or
provenance. Instruments and their listings are distinct; symbols are contextual.

Users should be able to understand completed financial activity and holdings in
terms of the instrument or contract, institution and account. Preserve the meaning
of an action, including source-reported opening/closing qualification and
monetary terms. Contract-based activity must retain its premiums and distinct
lifecycle events, including reported exercise, assignment and expiration.
Venue, routing and source-specific representations support that understanding
without forcing the user's financial view to fragment along those boundaries.
Preserve distinctions that change the
actual financial instrument, rights or contract terms; the
[domain vocabulary](domain.md#identity-and-grouping) owns these identity meanings.

Events, reported observations, and derived state are different kinds of knowledge.
A cash asset belongs to an account and currency independently of a balance;
financial activity can refer to it without establishing portfolio state.
A reported position of 100 shares does not explain how those shares were acquired.
If event history supports 95, preserve both claims and the unexplained difference
of five. The same principle applies to cash. Never invent an event to make an
observation agree with a calculation.

Keep exact financial values, original currencies, and the source's time precision.
A date remains a date; missing execution time is not permission to invent one.
Currency conversion, rounding, and other calculations produce derived values
without replacing the evidence they explain.

Additional kinds of evidence should extend these principles while retaining their
own financial meaning. User-supplied corrections must be attributable and remain
distinct from source claims. Wake should provide canonical lots and their
financial history for downstream tools, with source-reported costs and allocations
distinguishable from reconstructions and decisions. Preserve tax-relevant source
facts without requiring a personal tax-exposure profile. Downstream tools own
the user's tax context, jurisdiction-specific treatment and tax calculations.

## Source access and authority

Direct integrations, intermediary services, and imported documents retain their
source meaning without defining separate financial models. Each account's
designated source supplies its financial activity; installing a provider or
acquiring another report does not make that source authoritative for the account.
OpenFIGI remains the primary instrument identity and naming reference.

Operators control which sources and accounts Wake may acquire. Discovery does not
grant that authority, and restoring data does not restore permission to contact its
sources. Changes in access or identity require explicit review where prior authority
no longer applies. Wake should guide the user through upstream prerequisites while
respecting the limits of the access they granted.

Provider availability, history limits, and caching must remain visible in Wake's
claims about completeness and freshness. Respect source constraints, retain useful
progress, and recover without turning interruptions into repeated manual work or
uncontrolled request bursts.

## Trust and continuity

Protect credentials and private financial evidence throughout their lifetime.
Consumers should not need upstream secrets. Data reads and administrative authority
are separate capabilities; reachability alone grants neither. Future remote clients
should have individually revocable access rather than one shared credential whose
rotation disrupts every consumer.

Financial history must remain portable independently of access credentials and
transient runtime state. Moving or recovering it should preserve financial identity,
evidence, decisions, and the ability to explain prior conclusions. An installation
with no current source access should still be useful for inspection and consumption.

Derived views should be reconstructable from retained history. Repair and recovery
must preserve the original evidence and make changes in interpretation explicit.
Consumers need stable identities and understandable change semantics so that
restarts, corrections, and recovery do not silently corrupt their own view of Wake.

## Product boundaries

Wake owns financial history, source-reported observations, and their reconciliation.

Trading and order execution, brokerage gateways, a live market-data platform, full
tax preparation, general accounting, and a UI-first portfolio tracker are outside
Wake's purpose. Hosted multi-tenancy, billing, and distributed replication are not
requirements for its initial scope.

## Success

Wake succeeds when an operator can understand what evidence exists, what Wake
currently believes, how current and complete it is, and which unresolved questions
need their judgment. The product should make the next useful action clear.

Consumers should be able to rely on that financial foundation without knowing the
provider protocols or reconciliation machinery behind it. New sources and broader
financial coverage should strengthen the same foundation while preserving the
user's history, control, and ability to understand it.
