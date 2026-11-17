// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package recordcodec

import (
	"bytes"
	"encoding/json"
	"slices"
)

// WithLocation replaces the appearance's locator while preserving retained field order.
func WithLocation(kind string, data []byte, location string) ([]byte, error) {
	return claimWire(kind, data, location, false)
}

// ClaimKeyData orders structural sets for the hash while preserving multiplicity,
// exact values, unknowns and qualified references.
func ClaimKeyData(kind string, data []byte) ([]byte, error) { return claimWire(kind, data, "", true) }

func claimWire(kind string, data []byte, location string, orderSets bool) ([]byte, error) {
	var value any
	switch kind {
	case "positions":
		var d positionSetData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Location = location
		if orderSets {
			orderClaimSet(d.Supersedes)

			for i := range d.Positions {
				canonicalInstrument(&d.Positions[i].Instrument)
			}

			orderClaimSet(d.Positions)
		}

		value = d
	case "position-instrument":
		var d positionInstrumentData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		if orderSets {
			canonicalInstrument(&d.Instrument)
		}

		value = d
	case "correction":
		var d correctionData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Location = location
		value = d
	case "instrument":
		var d instrumentData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		if orderSets {
			canonicalInstrument(&d)
		}

		value = d
	case "activity":
		var d observationData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Location = location
		if orderSets {
			orderClaimSet(d.EventAliases)

			for i := range d.Fields {
				if d.Fields[i].Instrument != nil {
					canonicalInstrument(d.Fields[i].Instrument)
				}
			}

			orderClaimSet(d.Fields)
		}

		value = d
	case "rejected":
		return data, nil
	default:
		return nil, ErrCorrupt
	}

	return json.Marshal(value)
}

// Set ordering is structural; entries and their multiplicity remain intact.
func orderClaimSet[T any](values []T) {
	slices.SortStableFunc(values, func(a, b T) int {
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(b)

		return bytes.Compare(left, right)
	})
}

func canonicalInstrument(d *instrumentData) {
	orderClaimSet(d.Identifiers)

	if d.Option != nil {
		orderClaimSet(d.Option.Deliverables)

		if d.Option.Underlier != nil {
			canonicalInstrument(d.Option.Underlier)
		}
	}
}

func EncodeRejection(reason string) ([]byte, error) { return json.Marshal(reason) }

func DecodeRejection(data []byte) (string, error) {
	var reason string

	err := json.Unmarshal(data, &reason)

	return reason, err
}
