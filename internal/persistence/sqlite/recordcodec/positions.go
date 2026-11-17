// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package recordcodec

import (
	"encoding/json"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

type positionData struct {
	Instrument    instrumentData
	Quantity      string
	UnitCostBasis *moneyData `json:",omitempty"`
	Unit          string     `json:",omitempty"`
}

type positionSetData struct {
	Location           string
	Account, Reference refData
	Supersedes         []refData
	Date, At           string
	TimeBasis          source.PositionTimeBasis `json:",omitempty"`
	Complete           bool
	Reason             string
	Positions          []positionData
}

type positionInstrumentData struct {
	Account    refData
	Instrument instrumentData
}

func positionSetWire(o source.PositionSetObservation) positionSetData {
	f := o.Fields()
	d := positionSetData{
		Location:  f.Location,
		Account:   refWire(f.Account),
		Reference: refWire(f.Reference),
		At:        f.At.String(),
		TimeBasis: f.TimeBasis,
		Complete:  f.Complete,
		Reason:    f.Reason,
	}
	if !f.Date.IsZero() {
		d.Date = f.Date.String()
	}

	for _, r := range f.Supersedes {
		d.Supersedes = append(d.Supersedes, refWire(r))
	}

	for _, p := range f.Positions {
		row := positionData{
			Instrument: instrumentWire(p.Instrument()), Quantity: p.Quantity().Value(),
		}
		if unitCost := p.UnitCostBasis(); unitCost.Amount() != "" {
			row.UnitCostBasis = &moneyData{unitCost.Amount(), unitCost.Currency().String()}
		}

		if p.Instrument().Kind() == source.Debt && p.Unit() == "units" {
			row.Unit = p.Unit()
		}

		d.Positions = append(d.Positions, row)
	}

	return d
}

func DecodePositionSet(data []byte) (source.PositionSetObservation, error) {
	var d positionSetData
	if err := json.Unmarshal(data, &d); err != nil {
		return source.PositionSetObservation{}, err
	}

	f := source.PositionSetInput{
		Location:  d.Location,
		Date:      sourceDate(d.Date),
		TimeBasis: d.TimeBasis,
		Complete:  d.Complete,
		Reason:    d.Reason,
	}

	var err error

	if d.Date != "" && f.Date.IsZero() {
		return source.PositionSetObservation{}, ErrCorrupt
	}

	f.Account, err = d.Account.reference()
	if err != nil {
		return source.PositionSetObservation{}, err
	}

	f.Reference, err = d.Reference.reference()
	if err != nil {
		return source.PositionSetObservation{}, err
	}

	if d.At != "" {
		f.At, err = financial.ParseTimestamp(d.At)
		if err != nil {
			return source.PositionSetObservation{}, err
		}
	}

	for _, r := range d.Supersedes {
		v, e := r.reference()
		if e != nil {
			return source.PositionSetObservation{}, e
		}

		f.Supersedes = append(f.Supersedes, v)
	}

	for _, p := range d.Positions {
		i, e := p.Instrument.instrument()
		if e != nil {
			return source.PositionSetObservation{}, e
		}

		q, e := financial.ParseQuantity(p.Quantity)
		if e != nil {
			return source.PositionSetObservation{}, e
		}

		var price financial.Price
		if p.UnitCostBasis != nil {
			currency, e := financial.ParseCurrency(p.UnitCostBasis.Currency)
			if e != nil {
				return source.PositionSetObservation{}, e
			}

			price, e = financial.ParsePrice(p.UnitCostBasis.Amount, currency)
			if e != nil {
				return source.PositionSetObservation{}, e
			}
		}

		v, e := source.NewPosition(i, q, price)
		if e != nil {
			return source.PositionSetObservation{}, e
		}

		switch p.Unit {
		case "":
		case "units":
			v, e = v.WithReportedUnits()
			if e != nil {
				return source.PositionSetObservation{}, e
			}
		default:
			return source.PositionSetObservation{}, ErrCorrupt
		}

		f.Positions = append(f.Positions, v)
	}

	return source.NewPositionSetObservation(f)
}

func EncodePositionSet(o source.PositionSetObservation) ([]byte, error) {
	return json.Marshal(positionSetWire(o))
}

// PositionBinding retains a position row's wire values until a caller needs source validation.
type PositionBinding struct{ data positionInstrumentData }

func EncodePositionBinding(account source.Reference, instrument source.Instrument) ([]byte, error) {
	return json.Marshal(positionInstrumentData{refWire(account), instrumentWire(instrument)})
}

func DecodePositionBinding(data []byte) (PositionBinding, error) {
	var d positionInstrumentData
	if err := json.Unmarshal(data, &d); err != nil {
		return PositionBinding{}, err
	}

	return PositionBinding{data: d}, nil
}

func (b PositionBinding) Account() (source.Reference, error) { return b.data.Account.reference() }
func (b PositionBinding) Instrument() (source.Instrument, error) {
	return b.data.Instrument.instrument()
}

// PositionBindingKey uses the persisted instrument representation before domain validation.
func PositionBindingKey(data []byte) (string, source.InstrumentKind, error) {
	b, err := DecodePositionBinding(data)
	if err != nil {
		return "", "", err
	}

	return positionInstrumentKey(b.data.Instrument), b.data.Instrument.Kind, nil
}

func PositionInstrumentKey(i source.Instrument) string {
	return positionInstrumentKey(instrumentWire(i))
}

func positionInstrumentKey(d instrumentData) string {
	canonicalInstrument(&d)
	b, _ := json.Marshal(d)

	return string(b)
}
