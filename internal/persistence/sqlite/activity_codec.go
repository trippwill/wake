// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import "go.trippwill.dev/wake/internal/application"

func decodeActivityTerms(kind string, data []byte) (application.ActivityTerms, error) {
	switch application.ActivityKind(kind) {
	case application.TradeActivity:
		v, err := decodeTrade(data)
		if err != nil {
			return application.ActivityTerms{}, err
		}

		return application.TradeTerms(v)
	case application.LifecycleActivity:
		v, err := decodeLifecycle(data)
		if err != nil {
			return application.ActivityTerms{}, err
		}

		return application.LifecycleTerms(v)
	case application.ConversionActivity:
		v, err := decodeConversion(data)
		if err != nil {
			return application.ActivityTerms{}, err
		}

		return application.ConversionTerms(v)
	}

	return application.ActivityTerms{}, ErrCorrupt
}

func encodeActivityTerms(value application.ActivityTerms) ([]byte, error) {
	switch value.Kind() {
	case application.TradeActivity:
		return encodeTrade(value.Trade())
	case application.LifecycleActivity:
		return encodeLifecycle(value.Lifecycle())
	case application.ConversionActivity:
		return encodeConversion(value.Conversion())
	}

	return nil, ErrCorrupt
}
