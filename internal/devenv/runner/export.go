// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"bytes"
	"encoding/json"
	"reflect"

	"go.trippwill.dev/wake/internal/application"
)

func decodeExport(raw json.RawMessage) (any, bool) {
	if !raw.IsValid() {
		return nil, false
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var view any

	err := decoder.Decode(&view)

	return view, err == nil
}

func jsonExportsEqual(got, want json.RawMessage) bool {
	actual, actualOK := decodeExport(got)
	expected, expectedOK := decodeExport(want)

	return actualOK && expectedOK && reflect.DeepEqual(actual, expected)
}

func historyExportsEqual(got, want json.RawMessage) bool {
	actual, actualOK := decodeHistoryExport(got)
	expected, expectedOK := decodeHistoryExport(want)

	return actualOK && expectedOK && reflect.DeepEqual(actual, expected)
}

func decodeHistoryExport(raw json.RawMessage) (map[string]any, bool) {
	decoded, valid := decodeExport(raw)

	view, ok := decoded.(map[string]any)
	if !valid || !ok || view["mode"] != "full" {
		return nil, false
	}

	resources, ok := view["resources"].([]any)
	if !ok {
		return nil, false
	}

	indexed := make(map[application.ResourceKey]any, len(resources))
	for _, resource := range resources {
		fields, ok := resource.(map[string]any)
		if !ok {
			return nil, false
		}

		kind, _ := fields["kind"].(string)
		id, _ := fields["id"].(string)

		key := application.ResourceKey{Kind: kind, ID: id}
		if kind == "" || id == "" || indexed[key] != nil {
			return nil, false
		}

		indexed[key] = resource
	}

	view["resources"] = indexed

	return view, true
}
