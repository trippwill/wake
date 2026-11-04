// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func applyMovementField(o source.ActivityObservation, d FieldSelection) (source.ActivityObservation, error) {
	policy, ok := CorrectionFieldPolicy(d.Scope)
	if !ok {
		return source.ActivityObservation{}, ErrInvalid
	}

	if strings.HasPrefix(d.Scope, "option.") || strings.HasPrefix(d.Scope, "debt.") {
		old, _ := o.Field(source.FieldInstrument)

		instrument, e := applyInstrumentTerm(old.Fields().Instrument, d.Scope, d.Text, d.Currency)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		field, e := source.NewActivityField(
			source.ActivityFieldInput{Name: source.FieldInstrument, Instrument: instrument},
		)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		in := o.Fields()
		for i, v := range in.Fields {
			if v.Name() == source.FieldInstrument {
				in.Fields[i] = field
			}
		}

		return source.NewActivityObservation(in)
	}

	f := source.ActivityFieldInput{Name: source.ActivityFieldName(d.Scope)}

	var err error
	switch policy.ValueKind() {
	case TextCorrection, ChoiceCorrection:
		f.Text = d.Text
		f.Unknown = d.Text == "" && policy.Optional()
	case DateCorrection:
		f.Date, err = optionalDate(d.Text)
		f.Unknown = d.Text == "" && policy.Optional()
	case TimestampCorrection:
		f.Timestamp, err = optionalTimestamp(d.Text)
		f.Unknown = d.Text == "" && policy.Optional()
	case DecimalCorrection:
		f.Quantity, err = financial.NewQuantity(d.Text)
		if old, exists := o.Field(source.FieldQuantity); exists {
			f.QuantityUnit = old.Fields().QuantityUnit
		}
	case MoneyCorrection:
		if f.Name == source.FieldTransferPrice {
			if d.Text == "" && d.Currency == "" {
				f.Unknown = true
			} else {
				var c financial.Currency

				c, err = financial.ParseCurrency(d.Currency)
				if err == nil {
					f.Price, err = financial.NewPrice(d.Text, c)
				}
			}
		} else {
			f.Money, err = optionalMoney(d.Text, d.Currency)
			f.Unknown = d.Text == "" && policy.Optional()
		}
	default:
		return source.ActivityObservation{}, ErrInvalid
	}

	if err != nil {
		return source.ActivityObservation{}, ErrInvalid
	}

	value, err := source.NewActivityField(f)
	if err != nil {
		return source.ActivityObservation{}, ErrInvalid
	}

	in := o.Fields()
	i := slices.IndexFunc(in.Fields, func(v source.ActivityField) bool { return v.Key() == value.Key() })
	if i < 0 {
		in.Fields = append(in.Fields, value)
	} else {
		in.Fields[i] = value
	}

	return source.NewActivityObservation(in)
}
