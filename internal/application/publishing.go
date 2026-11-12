// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"bytes"
	"encoding/json"
	"slices"

	"go.trippwill.dev/wake/provider"
)

const ErrNotFound Error = "financial resource not found"

type CanonicalResource struct {
	ID         string               `json:"id"`
	Kind       string               `json:"kind"`
	Revision   int64                `json:"revision"`
	Account    *PublishedAccount    `json:"account,omitempty"`
	Instrument *PublishedInstrument `json:"instrument,omitempty"`
	Listing    *PublishedListing    `json:"listing,omitempty"`
	Activity   *PublishedActivity   `json:"activity,omitempty"`
}
type PublishedAccount struct {
	Name     string  `json:"name"`
	Currency *string `json:"currency"`
}
type PublishedInstrument struct {
	Family          string                `json:"family"`
	Name            *string               `json:"name"`
	Ticker          *string               `json:"ticker"`
	FIGI            *string               `json:"figi"`
	ReferenceStatus string                `json:"reference_status"`
	FIGIAllocation  *string               `json:"figi_allocation"`
	ReferenceReason *string               `json:"reference_reason"`
	UnderlierID     *string               `json:"underlier_id"`
	Identifiers     []PublishedIdentifier `json:"identifiers"`
}
type PublishedIdentifier struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type PublishedListing struct {
	InstrumentID string  `json:"instrument_id"`
	Venue        *string `json:"venue"`
	Ticker       *string `json:"ticker"`
	Currency     string  `json:"currency"`
	FIGI         *string `json:"figi"`
}
type PublishedMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}
type PublishedAmount struct {
	Amount   *string `json:"amount"`
	Currency string  `json:"currency"`
}
type PublishedCharge struct {
	Role       string            `json:"role"`
	Category   string            `json:"category"`
	Total      *PublishedMoney   `json:"total"`
	Components []PublishedCharge `json:"components"`
}

type PublishedQuantity struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}
type PublishedReference struct {
	Authority string `json:"authority"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	Value     string `json:"value"`
}
type PublishedRelationship struct {
	Kind           string              `json:"kind"`
	TargetID       *string             `json:"target_id"`
	ReportedTarget *PublishedReference `json:"reported_target"`
}
type PublishedDeliverable struct {
	Security *PublishedReference `json:"security"`
	Quantity *string             `json:"quantity"`
	Cash     *PublishedMoney     `json:"cash"`
}
type PublishedOption struct {
	Right         *string                `json:"right"`
	Expiry        *string                `json:"expiry"`
	Strike        *PublishedMoney        `json:"strike"`
	Multiplier    *string                `json:"multiplier"`
	EffectiveFrom *string                `json:"effective_from"`
	Deliverables  []PublishedDeliverable `json:"deliverables"`
}
type PublishedDebt struct {
	Type            *string `json:"type"`
	Currency        *string `json:"currency"`
	Maturity        *string `json:"maturity"`
	IssueDate       *string `json:"issue_date"`
	DatedDate       *string `json:"dated_date"`
	CouponRate      *string `json:"coupon_rate"`
	PaymentsPerYear *int    `json:"payments_per_year"`
}

// PublishedInstrumentTerms describes the security at the activity or position date.
type PublishedInstrumentTerms struct {
	Family string           `json:"family"`
	Option *PublishedOption `json:"-"`
	Debt   *PublishedDebt   `json:"-"`
}

func (t PublishedInstrumentTerms) MarshalJSON() ([]byte, error) {
	type family struct {
		Family string `json:"family"`
	}
	switch t.Family {
	case "option":
		if t.Option == nil || t.Debt != nil {
			return nil, ErrInvalid
		}

		return json.Marshal(struct {
			family
			*PublishedOption
		}{family{t.Family}, t.Option})
	case "debt":
		if t.Debt == nil || t.Option != nil {
			return nil, ErrInvalid
		}

		return json.Marshal(struct {
			family
			*PublishedDebt
		}{family{t.Family}, t.Debt})
	case "equity", "fund", "unclassified":
		if t.Option != nil || t.Debt != nil {
			return nil, ErrInvalid
		}

		return json.Marshal(family{t.Family})
	default:
		return nil, ErrInvalid
	}
}

// publishedFamily reads the discriminator without decoding the full body twice
// on storage reads. Wake writes the family first. Other field orders and
// escaped strings use the ordinary JSON decoder.
func publishedFamily(data []byte) (string, error) {
	if rest, ok := bytes.CutPrefix(data, []byte(`{"family":"`)); ok {
		if family, _, found := bytes.Cut(rest, []byte{'"'}); found && !bytes.ContainsRune(family, '\\') {
			return string(family), nil
		}
	}

	var header struct {
		Family string `json:"family"`
	}

	err := json.Unmarshal(data, &header)

	return header.Family, err
}

func (t *PublishedInstrumentTerms) UnmarshalJSON(data []byte) error {
	family, err := publishedFamily(data)
	if err != nil {
		return err
	}

	out := PublishedInstrumentTerms{Family: family}

	switch out.Family {
	case "option":
		out.Option = new(PublishedOption)
		if err := json.Unmarshal(data, out.Option); err != nil {
			return err
		}
	case "debt":
		out.Debt = new(PublishedDebt)
		if err := json.Unmarshal(data, out.Debt); err != nil {
			return err
		}
	case "equity", "fund", "unclassified":
	default:
		return ErrInvalid
	}

	*t = out

	return nil
}

type PublishedTransfer struct {
	PublishedSecurity
	InstrumentTerms     PublishedInstrumentTerms `json:"instrument_terms"`
	Quantity            *PublishedQuantity       `json:"quantity"`
	SettlementDate      *string                  `json:"settlement_date"`
	DeliveringBroker    *string                  `json:"delivering_broker"`
	Direction           string                   `json:"direction"`
	Method              *string                  `json:"method"`
	Company             *string                  `json:"company"`
	CounterpartyAccount *string                  `json:"counterparty_account"`
	CounterpartyName    *string                  `json:"counterparty_name"`
	ReportDate          *string                  `json:"report_date"`
	Price               *PublishedMoney          `json:"price"`
	PositionValue       *PublishedMoney          `json:"position_value"`
	ReportedCost        *PublishedMoney          `json:"reported_cost"`
	ReportedPnL         *PublishedMoney          `json:"reported_pnl"`
}
type PublishedCash struct {
	PublishedSecurity
	SettlementDate          *string             `json:"settlement_date"`
	Charges                 []PublishedCharge   `json:"charges"`
	AvailableForTradingDate *string             `json:"available_for_trading_date"`
	Category                string              `json:"category"`
	Amount                  PublishedMoney      `json:"amount"`
	ReportedType            *string             `json:"reported_type"`
	Description             *string             `json:"description"`
	Code                    *string             `json:"code"`
	ReportDate              *string             `json:"report_date"`
	ExDate                  *string             `json:"ex_date"`
	Action                  *PublishedReference `json:"action"`
}
type PublishedActivityHeader struct {
	Family              string                  `json:"family"`
	AccountID           string                  `json:"account_id"`
	State               string                  `json:"state"`
	Conflict            bool                    `json:"conflict"`
	Date                string                  `json:"date"`
	At                  *string                 `json:"at"`
	Relationships       []PublishedRelationship `json:"relationships"`
	ReportedRealizedPnL *PublishedRealizedPnL   `json:"reported_realized_pnl"`
}

// PublishedActivity contains the one accepted variant selected by Family.
type PublishedActivity struct {
	PublishedActivityHeader
	Trade      *PublishedTrade
	Lifecycle  *PublishedLifecycle
	Conversion *PublishedConversion
	Transfer   *PublishedTransfer
	Cash       *PublishedCash
}

type PublishedSecurity struct {
	InstrumentID *string `json:"instrument_id"`
	ListingID    *string `json:"listing_id"`
}

type PublishedTradeCash struct {
	GrossAmount         *PublishedMoney `json:"gross_amount"`
	Proceeds            *PublishedMoney `json:"proceeds"`
	NetCash             *PublishedMoney `json:"net_cash"`
	NetCashWithBillable *PublishedMoney `json:"net_cash_with_billable"`
}

type PublishedTrade struct {
	PublishedSecurity
	PublishedTradeCash
	InstrumentTerms PublishedInstrumentTerms `json:"instrument_terms"`
	Order           *PublishedReference      `json:"order"`
	TradeKind       *string                  `json:"trade_kind"`
	Side            *string                  `json:"side"`
	PositionEffect  *string                  `json:"position_effect"`
	Quantity        *PublishedQuantity       `json:"quantity"`
	Price           *PublishedMoney          `json:"price"`
	QuotationBasis  *string                  `json:"quotation_basis"`
	SettlementDate  *string                  `json:"settlement_date"`
	AccruedInterest *PublishedMoney          `json:"accrued_interest"`
	Charges         []PublishedCharge        `json:"charges"`
}

type PublishedLifecycle struct {
	PublishedSecurity
	InstrumentTerms PublishedInstrumentTerms `json:"instrument_terms"`
	Order           *PublishedReference      `json:"order"`
	LifecycleKind   *string                  `json:"lifecycle_kind"`
	Quantity        *PublishedQuantity       `json:"quantity"`
}

type PublishedConversion struct {
	PublishedTradeCash
	Order    *PublishedReference `json:"order"`
	Sent     *PublishedAmount    `json:"sent"`
	Received *PublishedAmount    `json:"received"`
	RateBase *string             `json:"rate_base"`
	Rate     *PublishedMoney     `json:"rate"`
	Charges  []PublishedCharge   `json:"charges"`
}

// InstrumentID returns the linked instrument, when the activity has one.
func (a PublishedActivity) InstrumentID() *string {
	switch a.Family {
	case "trade":
		return a.Trade.InstrumentID
	case "lifecycle":
		return a.Lifecycle.InstrumentID
	case "security-transfer":
		return a.Transfer.InstrumentID
	case "cash":
		return a.Cash.InstrumentID
	default:
		return nil
	}
}

func (a PublishedActivity) Quantity() *PublishedQuantity {
	switch a.Family {
	case "trade":
		return a.Trade.Quantity
	case "lifecycle":
		return a.Lifecycle.Quantity
	case "security-transfer":
		return a.Transfer.Quantity
	default:
		return nil
	}
}

func (a PublishedActivity) Charges() []PublishedCharge {
	switch a.Family {
	case "trade":
		return a.Trade.Charges
	case "conversion":
		return a.Conversion.Charges
	case "cash":
		return a.Cash.Charges
	default:
		return nil
	}
}

func (a PublishedActivity) MarshalJSON() ([]byte, error) {
	if a.variantCount() != 1 || !a.matchesFamily() {
		return nil, ErrInvalid
	}

	switch a.Family {
	case "trade":
		return json.Marshal(struct {
			PublishedActivityHeader
			*PublishedTrade
		}{a.PublishedActivityHeader, a.Trade})
	case "lifecycle":
		return json.Marshal(struct {
			PublishedActivityHeader
			*PublishedLifecycle
		}{a.PublishedActivityHeader, a.Lifecycle})
	case "conversion":
		return json.Marshal(struct {
			PublishedActivityHeader
			*PublishedConversion
		}{a.PublishedActivityHeader, a.Conversion})
	case "security-transfer":
		return json.Marshal(struct {
			PublishedActivityHeader
			*PublishedTransfer
		}{a.PublishedActivityHeader, a.Transfer})
	case "cash":
		return json.Marshal(struct {
			PublishedActivityHeader
			*PublishedCash
		}{a.PublishedActivityHeader, a.Cash})
	}

	return nil, ErrInvalid
}

func (a *PublishedActivity) UnmarshalJSON(data []byte) error {
	family, err := publishedFamily(data)
	if err != nil {
		return err
	}

	var out PublishedActivity

	switch family {
	case "trade":
		out.Trade = new(PublishedTrade)
		if err := json.Unmarshal(data, &struct {
			*PublishedActivityHeader
			*PublishedTrade
		}{&out.PublishedActivityHeader, out.Trade}); err != nil {
			return err
		}
	case "lifecycle":
		out.Lifecycle = new(PublishedLifecycle)
		if err := json.Unmarshal(data, &struct {
			*PublishedActivityHeader
			*PublishedLifecycle
		}{&out.PublishedActivityHeader, out.Lifecycle}); err != nil {
			return err
		}
	case "conversion":
		out.Conversion = new(PublishedConversion)
		if err := json.Unmarshal(data, &struct {
			*PublishedActivityHeader
			*PublishedConversion
		}{&out.PublishedActivityHeader, out.Conversion}); err != nil {
			return err
		}
	case "security-transfer":
		out.Transfer = new(PublishedTransfer)
		if err := json.Unmarshal(data, &struct {
			*PublishedActivityHeader
			*PublishedTransfer
		}{&out.PublishedActivityHeader, out.Transfer}); err != nil {
			return err
		}
	case "cash":
		out.Cash = new(PublishedCash)
		if err := json.Unmarshal(data, &struct {
			*PublishedActivityHeader
			*PublishedCash
		}{&out.PublishedActivityHeader, out.Cash}); err != nil {
			return err
		}
	default:
		return ErrInvalid
	}

	if out.Family != family {
		return ErrInvalid
	}

	*a = out

	return nil
}

func (a PublishedActivity) variantCount() int {
	n := 0
	if a.Trade != nil {
		n++
	}

	if a.Lifecycle != nil {
		n++
	}

	if a.Conversion != nil {
		n++
	}

	if a.Transfer != nil {
		n++
	}

	if a.Cash != nil {
		n++
	}

	return n
}

func (a PublishedActivity) matchesFamily() bool {
	return a.Family == "trade" && a.Trade != nil ||
		a.Family == "lifecycle" && a.Lifecycle != nil ||
		a.Family == "conversion" && a.Conversion != nil ||
		a.Family == "security-transfer" && a.Transfer != nil ||
		a.Family == "cash" && a.Cash != nil
}

type PublishedRealizedPnL struct {
	Amount              *PublishedMoney `json:"amount"`
	CostBasis           *PublishedMoney `json:"cost_basis"`
	ReportDate          *string         `json:"report_date"`
	CostBasisReportDate *string         `json:"cost_basis_report_date"`
	Conflict            bool            `json:"conflict"`
	Selection           string          `json:"selection"`
}

type PublishedRealizedExplanation struct {
	Rule      string `json:"rule"`
	Selection string `json:"selection"`
	Conflict  bool   `json:"conflict"`
}
type PublishedIdentity struct {
	FIGI       *string `json:"figi"`
	Allocation *string `json:"allocation"`
	Reason     *string `json:"reason"`
	Rule       string  `json:"rule"`
}
type FieldAbsenceReason string

const (
	FieldNotReported         FieldAbsenceReason = "not_reported"
	FieldReportedUnavailable FieldAbsenceReason = "reported_unavailable"
	FieldUnresolved          FieldAbsenceReason = "unresolved"
	FieldNotApplicable       FieldAbsenceReason = "not_applicable"
	FieldOperatorWithheld    FieldAbsenceReason = "operator_withheld"
)

type PublishedFieldDetail struct {
	Path           string                     `json:"path"`
	Reason         FieldAbsenceReason         `json:"reason"`
	Expectation    *provider.FieldExpectation `json:"expectation"`
	Interpretation *string                    `json:"interpretation"`
}

type Explanation struct {
	CalculationRule string                        `json:"calculation_rule,omitempty"`
	Fields          []PublishedFieldDetail        `json:"fields"`
	RealizedPnL     *PublishedRealizedExplanation `json:"reported_realized_pnl,omitempty"`
	Identity        *PublishedIdentity            `json:"identity"`
	ID              string                        `json:"id"`
	Kind            string                        `json:"kind"`
	Revision        int64                         `json:"revision"`
	Selection       string                        `json:"selection"`
	Conflict        bool                          `json:"conflict"`
	Corrections     []PublishedCorrection         `json:"corrections"`
	Decisions       []PublishedDecision           `json:"decisions"`
}
type PublishedCorrection struct {
	Kind           string   `json:"kind"`
	Status         string   `json:"status"`
	TargetIDs      []string `json:"target_ids"`
	ReplacementIDs []string `json:"replacement_ids"`
}
type PublishedDecision struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Action string `json:"action"`
}

// FinancialSnapshot is one complete, consistent accepted warehouse view. Version
// is opaque and changes only when accepted resources or explanations change.
type FinancialSnapshot struct {
	WarehouseID string              `json:"warehouse_id"`
	Version     string              `json:"version"`
	Resources   []CanonicalResource `json:"resources"`
}
type SnapshotRead struct {
	Snapshot  FinancialSnapshot
	Unchanged bool
}

// SnapshotCondition keeps an HTTP wildcard distinct from literal opaque versions.
// Its zero value does not match any publication.
type SnapshotCondition struct {
	wildcard bool
	versions []string
}

func AnySnapshotVersion() SnapshotCondition { return SnapshotCondition{wildcard: true} }
func MatchingSnapshotVersions(versions ...string) SnapshotCondition {
	return SnapshotCondition{versions: slices.Clone(versions)}
}

func (condition SnapshotCondition) Matches(version string) bool {
	return condition.wildcard || slices.Contains(condition.versions, version)
}
