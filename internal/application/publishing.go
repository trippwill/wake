// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import "slices"

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
	Role  string         `json:"role"`
	Scope string         `json:"scope"`
	Money PublishedMoney `json:"money"`
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
	Coupon          *string `json:"coupon"`
	PaymentsPerYear *int    `json:"payments_per_year"`
}
type PublishedActivity struct {
	Family          string                  `json:"family"`
	AccountID       string                  `json:"account_id"`
	InstrumentID    *string                 `json:"instrument_id"`
	ListingID       *string                 `json:"listing_id"`
	State           string                  `json:"state"`
	Conflict        bool                    `json:"conflict"`
	Date            string                  `json:"date"`
	At              *string                 `json:"at"`
	Order           *PublishedReference     `json:"order"`
	Relationships   []PublishedRelationship `json:"relationships"`
	TradeKind       *string                 `json:"trade_kind"`
	Side            *string                 `json:"side"`
	PositionEffect  *string                 `json:"position_effect"`
	Quantity        *PublishedQuantity      `json:"quantity"`
	Price           *PublishedMoney         `json:"price"`
	PriceBasis      *string                 `json:"price_basis"`
	SettlementDate  *string                 `json:"settlement_date"`
	LifecycleKind   *string                 `json:"lifecycle_kind"`
	Sent            *PublishedAmount        `json:"sent"`
	Received        *PublishedAmount        `json:"received"`
	RateBase        *string                 `json:"rate_base"`
	Rate            *PublishedMoney         `json:"rate"`
	Gross           *PublishedMoney         `json:"gross"`
	Proceeds        *PublishedMoney         `json:"proceeds"`
	Net             *PublishedMoney         `json:"net"`
	NetWithBillable *PublishedMoney         `json:"net_with_billable"`
	AccruedInterest *PublishedMoney         `json:"accrued_interest"`
	Charges         []PublishedCharge       `json:"charges"`
	// Terms retain the contract/issue terms used for this activity, independently of reference refresh.
	Option *PublishedOption `json:"option"`
	Debt   *PublishedDebt   `json:"debt"`
}
type PublishedIdentity struct {
	FIGI       *string `json:"figi"`
	Allocation *string `json:"allocation"`
	Reason     *string `json:"reason"`
	Rule       string  `json:"rule"`
}
type Explanation struct {
	Identity    *PublishedIdentity    `json:"identity"`
	ID          string                `json:"id"`
	Kind        string                `json:"kind"`
	Revision    int64                 `json:"revision"`
	Selection   string                `json:"selection"`
	Conflict    bool                  `json:"conflict"`
	Corrections []PublishedCorrection `json:"corrections"`
	Decisions   []PublishedDecision   `json:"decisions"`
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
