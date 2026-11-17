// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package recordcodec

import (
	"encoding/json"

	"go.trippwill.dev/wake/source"
)

// ActivitySelection carries persisted field values and their selection metadata.
type ActivitySelection struct {
	Baseline, Fields          []source.ActivityField
	Sources                   map[string][]string
	Conflicts, OperatorFields []string
}

type activitySelectionData struct {
	Fields                    []activityFieldData
	Sources                   map[string][]string
	Conflicts, OperatorFields []string
}

func encodeActivityFields(fields []source.ActivityField) ([]byte, error) {
	out := make([]activityFieldData, 0, len(fields))
	for _, f := range fields {
		out = append(out, activityFieldWire(f))
	}

	return json.Marshal(out)
}

func decodeActivityFields(data []byte) ([]source.ActivityField, error) {
	var values []activityFieldData
	if json.Unmarshal(data, &values) != nil {
		return nil, ErrCorrupt
	}

	out := make([]source.ActivityField, 0, len(values))
	for _, v := range values {
		f, e := v.field()
		if e != nil {
			return nil, ErrCorrupt
		}

		out = append(out, f)
	}

	return out, nil
}

func EncodeActivitySelection(value ActivitySelection) (baseline, selected []byte, err error) {
	base, err := encodeActivityFields(value.Baseline)
	if err != nil {
		return nil, nil, err
	}

	d := activitySelectionData{
		Sources:        value.Sources,
		Conflicts:      value.Conflicts,
		OperatorFields: value.OperatorFields,
	}
	for _, f := range value.Fields {
		d.Fields = append(d.Fields, activityFieldWire(f))
	}

	data, err := json.Marshal(d)

	return base, data, err
}

func DecodeActivitySelection(baseline, selected []byte) (ActivitySelection, error) {
	var d activitySelectionData
	if json.Unmarshal(selected, &d) != nil {
		return ActivitySelection{}, ErrCorrupt
	}

	out := ActivitySelection{
		Sources:        d.Sources,
		Conflicts:      d.Conflicts,
		OperatorFields: d.OperatorFields,
	}

	var err error

	out.Baseline, err = decodeActivityFields(baseline)
	if err != nil {
		return out, err
	}

	for _, v := range d.Fields {
		f, e := v.field()
		if e != nil {
			return out, ErrCorrupt
		}

		out.Fields = append(out.Fields, f)
	}

	return out, nil
}
