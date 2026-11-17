// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
)

func decodeActivityTerms(kind string, data []byte) (calculation.ActivityTerms, error) {
	switch calculation.ActivityKind(kind) {
	case calculation.CashActivity:
		o, err := recordcodec.DecodeObservation(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		v, err := o.CashActivity()
		if err != nil {
			return calculation.ActivityTerms{}, ErrCorrupt
		}

		return calculation.CashTerms(v)
	case calculation.SecurityTransferActivity:
		o, err := recordcodec.DecodeObservation(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		v, err := o.SecurityTransfer()
		if err != nil {
			return calculation.ActivityTerms{}, ErrCorrupt
		}

		return calculation.SecurityTransferTerms(v)
	case calculation.TradeActivity:
		v, err := recordcodec.DecodeTrade(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		return calculation.TradeTerms(v)
	case calculation.LifecycleActivity:
		v, err := recordcodec.DecodeLifecycle(data)
		if err != nil {
			return calculation.ActivityTerms{}, err
		}

		return calculation.LifecycleTerms(v)
	case calculation.ConversionActivity:
		v, err := recordcodec.DecodeConversion(data)
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
		return recordcodec.EncodeObservation(value.Observation())
	case calculation.SecurityTransferActivity:
		return recordcodec.EncodeObservation(value.Observation())
	case calculation.TradeActivity:
		return recordcodec.EncodeTrade(value.Trade())
	case calculation.LifecycleActivity:
		return recordcodec.EncodeLifecycle(value.Lifecycle())
	case calculation.ConversionActivity:
		return recordcodec.EncodeConversion(value.Conversion())
	}

	return nil, ErrCorrupt
}
