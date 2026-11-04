// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"
	"strings"

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
	transferFields
	cashFields
)

var correctionPolicies = []FieldPolicy{
	{"delivering_broker", TextCorrection, true, SignedDecimal, 1024, transferFields, ""},
	{"available_for_trading_date", DateCorrection, true, SignedDecimal, 2048, cashFields, ""},
	{"cash_category", ChoiceCorrection, false, SignedDecimal, 128, cashFields, ""},
	{"cash_amount", MoneyCorrection, false, SignedDecimal, 256, cashFields, ""},
	{"description", TextCorrection, true, SignedDecimal, 1024, cashFields, ""},
	{"ex_date", DateCorrection, true, SignedDecimal, 2048, cashFields, ""},
	{"direction", ChoiceCorrection, false, SignedDecimal, 32, transferFields, ""},
	{"transfer_method", TextCorrection, true, SignedDecimal, 1024, transferFields, ""},
	{"transfer_company", TextCorrection, true, SignedDecimal, 1024, transferFields, ""},
	{"counterparty_account", TextCorrection, true, SignedDecimal, 1024, transferFields, ""},
	{"counterparty_name", TextCorrection, true, SignedDecimal, 1024, transferFields, ""},
	{"report_date", DateCorrection, true, SignedDecimal, 2048, transferFields | cashFields, ""},
	{"transfer_price", MoneyCorrection, true, SignedDecimal, 256, transferFields, ""},
	{"position_value", MoneyCorrection, true, SignedDecimal, 256, transferFields, ""},
	{"transfer_cost", MoneyCorrection, true, SignedDecimal, 256, transferFields, ""},
	{"transfer_pnl", MoneyCorrection, true, SignedDecimal, 256, transferFields, ""},
	{
		"quantity",
		DecimalCorrection,
		false,
		PositiveDecimal,
		256,
		tradeFields | lifecycleFields | transferFields,
		"",
	},
	{"price", MoneyCorrection, false, SignedDecimal, 256, tradeFields, ""},
	{"side", ChoiceCorrection, false, SignedDecimal, 2048, tradeFields, ""},
	{
		"trade_date",
		DateCorrection,
		false,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | transferFields | cashFields | conversionFields,
		"",
	},
	{
		"executed_at",
		TimestampCorrection,
		true,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | transferFields | cashFields | conversionFields,
		"",
	},
	{
		"settlement_date",
		DateCorrection,
		true,
		SignedDecimal,
		2048,
		tradeFields | transferFields | cashFields,
		"",
	},
	{"gross", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"proceeds", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"net", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"net_billable", MoneyCorrection, true, SignedDecimal, 256, tradeFields | conversionFields, ""},
	{"accrued_interest", MoneyCorrection, true, SignedDecimal, 256, tradeFields, ""},
	{"position_effect", ChoiceCorrection, true, SignedDecimal, 2048, tradeFields, ""},
	{
		"option.strike",
		MoneyCorrection,
		false,
		NonnegativeDecimal,
		256,
		tradeFields | lifecycleFields | transferFields,
		source.Option,
	},
	{
		"option.multiplier",
		DecimalCorrection,
		true,
		PositiveDecimal,
		256,
		tradeFields | lifecycleFields | transferFields,
		source.Option,
	},
	{
		"option.right",
		ChoiceCorrection,
		true,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | transferFields,
		source.Option,
	},
	{
		"option.expiry",
		DateCorrection,
		true,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | transferFields,
		source.Option,
	},
	{"debt.coupon", DecimalCorrection, true, NonnegativeDecimal, 256, tradeFields, source.Debt},
	{"debt.maturity", DateCorrection, true, SignedDecimal, 2048, tradeFields, source.Debt},
	{
		"instrument",
		LinkCorrection,
		false,
		SignedDecimal,
		2048,
		tradeFields | lifecycleFields | transferFields | cashFields,
		"",
	},
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
		tradeFields | lifecycleFields | transferFields | cashFields | conversionFields,
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
	case CashActivity:
		return cashFields
	case SecurityTransferActivity:
		return transferFields
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
	if strings.HasPrefix(scope, "evidence/") {
		return FieldPolicy{
			scope: scope,
			kind:  ObservationCorrection,
			kinds: tradeFields | lifecycleFields | transferFields | cashFields | conversionFields,
		}, activityMask(
			kind,
		) != 0
	}

	p, ok := CorrectionFieldPolicy(scope)

	return p, ok && p.kinds&activityMask(kind) != 0
}

func EditableActivityFields(kind ActivityKind, family source.InstrumentKind) []FieldPolicy {
	out := []FieldPolicy{}
	for _, p := range correctionPolicies {
		if p.scope == "instrument" && family == "" {
			continue
		}

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
