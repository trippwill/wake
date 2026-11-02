# What can IBKR debt reports establish?

The inspected Confirmation and Activity Flex reports supply enough information to
retain nominal face quantities, exact reported prices and separate monetary roles.
They do not establish a universal quotation convention. Trade net cash can exclude
separately reported accrued interest. A combined settlement total requires its own
reported meaning; it cannot be assumed from a field named net cash.

Reviewed 2026-11-01. These findings support the adopted
[debt value policy](../product/domain.md#instruments-and-representations) and
[IBKR interpretation](../providers/ibkr-flex.md). They do not expand supported
Activity Flex ingestion.

## Documented facts

- The [Confirmation configuration reference](https://www.ibkrguides.com/reportingreference/reportguide/trade%20confirmation%20configuration.htm)
  describes quantity, price, amount, proceeds and charges. It does not document a
  separate par-value or quotation-basis selection. Its generic amount equation
  does not explain the scaling seen in the inspected debt executions.
- The [Activity Trades reference](https://www.ibkrguides.com/reportingreference/reportguide/tradesfq.htm)
  defines net cash as proceeds plus taxes and commissions. It supplies no universal
  debt-price convention.
- The [standard fixed-income confirmation reference](https://www.ibkrguides.com/reportingreference/reportguide/fixedincome_tradeconfirm.htm)
  separately describes par value, principal, accrued interest and final money.
  That is a different reporting interface: its fields and final-money formula do
  not prove that corresponding Flex query selections exist or define Flex net cash.

## Sample observations

Read-only inspection of two private maximum-field XML exports, one TCF and one AF,
covered a conventional UK gilt and a US Treasury bill. No private rows, identifiers
or financial amounts are reproduced here.

- Neither export's debt records contained a dedicated face/par/nominal/denomination
  attribute. Quantity carried the nominal amount; a bill maturity payment in the
  Activity export independently corroborated that interpretation.
- Both instruments' reported principal amounts were consistent with price times
  nominal quantity divided by 100, allowing for reported principal precision.
  This is evidence about these samples, not a guarantee for all debt.
- Confirmation and Activity trade rows supplied accrued interest and trade net
  cash independently. The gilt's nonzero accrued interest was excluded from net
  cash; the bill explicitly reported zero accrued interest. Proceeds plus
  commissions and taxes agreed with net cash in both examples.
- Activity also reported the gilt purchase's accrued interest as a separate cash
  transaction. Adding it to an assumed trade settlement total risks double counting.
- The actual Confirmation rows included accrued-interest and net-cash attributes
  absent from the published configuration field list. That list is incomplete for
  these exports; its omissions cannot prove a field unavailable.

## Inferences and limits

The samples support retaining debt quantity as nominal face amount and preserving
all reported money independently. They do not justify attaching a quotation-basis
claim, deriving missing principal, or reconstructing a final settlement total.
No live configuration editor was inspected, so the research cannot rule out other
query selections. Nothing here establishes all debt products' quotation conventions,
indexation, clean/dirty pricing or denomination rules.

The synthetic regression cases in
[debt reporting tests](../../internal/persistence/sqlite/debt_reporting_test.go)
use invented issues, dates and values. They exercise normalization, persistence,
publication, corrections, rollback and repeated reports without requiring private
reports or live credentials.
