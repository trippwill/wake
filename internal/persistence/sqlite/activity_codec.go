// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import "go.trippwill.dev/wake/internal/application/calculation"

func decodeActivityTerms(kind string, data []byte) (calculation.ActivityTerms, error) {
	switch calculation.ActivityKind(kind) {
	case calculation.CashActivity:
		o, err := decodeObservation(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		v, err := o.CashActivity()
		if err != nil {
			return calculation.ActivityTerms{}, ErrCorrupt
		}

		return calculation.CashTerms(v)
	case calculation.SecurityTransferActivity:
		o, err := decodeObservation(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		v, err := o.SecurityTransfer()
		if err != nil {
			return calculation.ActivityTerms{}, ErrCorrupt
		}

		return calculation.SecurityTransferTerms(v)
	case calculation.TradeActivity:
		v, err := decodeTrade(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		return calculation.TradeTerms(v)
	case calculation.LifecycleActivity:
		v, err := decodeLifecycle(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		return calculation.LifecycleTerms(v)
	case calculation.ConversionActivity:
		v, err := decodeConversion(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		return calculation.ConversionTerms(v)
	}

	return calculation.ActivityTerms{}, ErrCorrupt
}

func encodeActivityTerms(value calculation.ActivityTerms) ([]byte, error) {
	switch value.Kind() {
	case calculation.CashActivity:
		return encodeObservation(value.Observation())
	case calculation.SecurityTransferActivity:
		return encodeObservation(value.Observation())
	case calculation.TradeActivity:
		return encodeTrade(value.Trade())
	case calculation.LifecycleActivity:
		return encodeLifecycle(value.Lifecycle())
	case calculation.ConversionActivity:
		return encodeConversion(value.Conversion())
	}

	return nil, ErrCorrupt
}
