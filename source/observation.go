// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import "go.trippwill.dev/wake/financial"

// InstrumentObservation describes reported security information independently
// of an account or financial event. AsOf is an observation date, not a term's
// effective date; missing source dates remain absent.
type InstrumentObservation struct {
	location   string
	instrument Instrument
	asOf       financial.Date
}

func NewInstrumentObservation(
	location string,
	instrument Instrument,
	asOf financial.Date,
) (InstrumentObservation, error) {
	if !validText(location, 1024) || instrument.Kind() == "" {
		return InstrumentObservation{}, ErrInvalidInstrument
	}

	return InstrumentObservation{location: location, instrument: instrument, asOf: asOf}, nil
}

func (o InstrumentObservation) Location() string       { return o.location }
func (o InstrumentObservation) Instrument() Instrument { return o.instrument }
func (o InstrumentObservation) AsOf() financial.Date   { return o.asOf }
