// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"
	"fmt"

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

func decodePositionSet(data []byte) (source.PositionSetObservation, error) {
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

func (c *claims) PutPositionSet(ctx context.Context, o source.PositionSetObservation) error {
	if o.IsZero() {
		c.failed = source.ErrInvalidPositions
		return c.failed
	}

	f := o.Fields()

	data, err := json.Marshal(positionSetWire(o))
	if err != nil {
		return err
	}

	if err = c.put(ctx, f.Location, "positions", data); err != nil {
		return err
	}

	c.counts.PositionSets++
	for i, p := range f.Positions {
		data, err = json.Marshal(positionInstrumentData{refWire(f.Account), instrumentWire(p.Instrument())})
		if err != nil {
			return err
		}

		if err = c.put(
			ctx,
			fmt.Sprintf("%s/instruments[%d]", f.Location, i+1),
			"position-instrument",
			data,
		); err != nil {
			return err
		}

		c.counts.Instruments++
	}

	return nil
}
