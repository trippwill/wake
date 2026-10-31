// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"

	"go.trippwill.dev/wake/source"
)

type CorrectionValueKind uint8

const (
	TextCorrection CorrectionValueKind = iota
	DecimalCorrection
	MoneyCorrection
	DateCorrection
	TimestampCorrection
	ChoiceCorrection
	LinkCorrection
	ObservationCorrection
)

type DecimalBound uint8

const (
	SignedDecimal DecimalBound = iota
	NonnegativeDecimal
	PositiveDecimal
)

// FieldPolicy owns financial input semantics. Presentation owns labels and help.
type FieldPolicy struct {
	scope    string
	kind     CorrectionValueKind
	optional bool
	bound    DecimalBound
	limit    int
	kinds    uint8
	family   source.InstrumentKind
}

func (p FieldPolicy) Scope() string                  { return p.scope }
func (p FieldPolicy) ValueKind() CorrectionValueKind { return p.kind }
func (p FieldPolicy) Optional() bool                 { return p.optional }
func (p FieldPolicy) DecimalBound() DecimalBound     { return p.bound }
func (p FieldPolicy) Limit() int                     { return p.limit }

const (
	tradeFields uint8 = 1 << iota
	lifecycleFields
	conversionFields
	instrumentFields
)

var correctionPolicies = []FieldPolicy{
	{"quantity", DecimalCorrection, false, PositiveDecimal, 256, tradeFields | lifecycleFields, ""},
	{"price", MoneyCorrection, false, SignedDecimal, 256, tradeFields, ""},
	{"side", ChoiceCorrection, false, SignedDecimal, 2048, tradeFields, ""},
	{
		"trade_date",
		DateCorrection,
		false,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | conversionFields,
		"",
	},
	{
		"executed_at",
		TimestampCorrection,
		true,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | conversionFields,
		"",
	},
	{"settlement_date", DateCorrection, true, SignedDecimal, 2048, tradeFields, ""},
	{"gross", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"proceeds", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"net", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"net_billable", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"accrued_interest", MoneyCorrection, true, SignedDecimal, 256, tradeFields, ""},
	{"position_effect", ChoiceCorrection, true, SignedDecimal, 2048, tradeFields, ""},
	{"quotation_basis", ChoiceCorrection, true, SignedDecimal, 2048, tradeFields, source.Debt},
	{
		"option.strike",
		MoneyCorrection,
		false,
		NonnegativeDecimal,
		256,
		tradeFields | lifecycleFields,
		source.Option,
	},
	{
		"option.multiplier",
		DecimalCorrection,
		true,
		PositiveDecimal,
		256,
		tradeFields | lifecycleFields,
		source.Option,
	},
	{"option.right", ChoiceCorrection, true, SignedDecimal, 2048, tradeFields | lifecycleFields, source.Option},
	{"option.expiry", DateCorrection, true, SignedDecimal, 2048, tradeFields | lifecycleFields, source.Option},
	{"debt.coupon", DecimalCorrection, true, NonnegativeDecimal, 256, tradeFields, source.Debt},
	{"debt.maturity", DateCorrection, true, SignedDecimal, 2048, tradeFields, source.Debt},
	{"instrument", LinkCorrection, false, SignedDecimal, 2048, tradeFields | lifecycleFields, ""},
	{"listing", LinkCorrection, true, SignedDecimal, 2048, tradeFields, ""},
	{"kind", ChoiceCorrection, false, SignedDecimal, 2048, lifecycleFields, ""},
	{"sent_amount", DecimalCorrection, true, NonnegativeDecimal, 256, conversionFields, ""},
	{"received_amount", DecimalCorrection, true, NonnegativeDecimal, 256, conversionFields, ""},
	{"rate", DecimalCorrection, false, PositiveDecimal, 256, conversionFields, ""},
	{"name", TextCorrection, false, SignedDecimal, 1024, instrumentFields, ""},
	{"ticker", TextCorrection, true, SignedDecimal, 256, instrumentFields, ""},
	{"underlier", LinkCorrection, true, SignedDecimal, 2048, instrumentFields, source.Option},
	{"reference", ObservationCorrection, false, SignedDecimal, 2048, instrumentFields, ""},
	{
		"observation",
		ObservationCorrection,
		false,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | conversionFields,
		"",
	},
}

func CorrectionFieldPolicy(scope string) (FieldPolicy, bool) {
	i := slices.IndexFunc(correctionPolicies, func(p FieldPolicy) bool { return p.scope == scope })
	if i < 0 {
		return FieldPolicy{}, false
	}

	return correctionPolicies[i], true
}

func activityMask(kind ActivityKind) uint8 {
	switch kind {
	case TradeActivity:
		return tradeFields
	case LifecycleActivity:
		return lifecycleFields
	case ConversionActivity:
		return conversionFields
	}

	return 0
}

func ActivityFieldPolicy(kind ActivityKind, scope string) (FieldPolicy, bool) {
	p, ok := CorrectionFieldPolicy(scope)
	return p, ok && p.kinds&activityMask(kind) != 0
}

func EditableActivityFields(kind ActivityKind, family source.InstrumentKind) []FieldPolicy {
	out := []FieldPolicy{}
	for _, p := range correctionPolicies {
		if p.kinds&activityMask(kind) != 0 && p.kind != ObservationCorrection &&
			(p.family == "" || p.family == family) {
			out = append(out, p)
		}
	}

	return out
}

func EditableInstrumentFields(family source.InstrumentKind) []FieldPolicy {
	out := []FieldPolicy{}
	for _, p := range correctionPolicies {
		if p.kinds&instrumentFields != 0 && p.kind != ObservationCorrection &&
			(p.family == "" || p.family == family) {
			out = append(out, p)
		}
	}

	return out
}
