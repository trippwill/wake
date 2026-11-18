// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoryExportsEqual(t *testing.T) {
	t.Parallel()

	const (
		first  = `{"id":"a","kind":"accounts","revision":9007199254740993,"account":{"name":"Example","currency":null}}`
		second = `{"id":"i","kind":"instruments","revision":2,"instrument":{"identifiers":["first","second"],"ticker":null}}`
		full   = `{"mode":"full","baseline_id":"b","version":"v","resources":[` + first + "," + second + ` ]}`
		empty  = `{"mode":"full","baseline_id":"b","version":"v","resources":[]}`
	)

	for _, test := range []struct {
		name string
		got  string
		want string
		same bool
	}{
		{"same export", full, full, true},
		{"reorder objects and resources", `{"version":"v","baseline_id":"b","mode":"full","resources":[` + second + "," + first + `]}`, full, true},
		{"empty array", empty, empty, true},
		{"null resources", strings.Replace(empty, "[]", "null", 1), empty, false},
		{"absent resources", strings.Replace(empty, `,"resources":[]`, "", 1), empty, false},
		{"duplicate resources", strings.Replace(full, second, first, 1), full, false},
		{"nested array order", strings.Replace(full, `"first","second"`, `"second","first"`, 1), full, false},
		{"missing null field", strings.Replace(full, `,"currency":null`, "", 1), full, false},
		{"unexpected null field", strings.Replace(full, `"revision":2`, `"revision":2,"listing":null`, 1), full, false},
		{"unexpected envelope field", strings.Replace(full, `"mode":"full"`, `"mode":"full","from":null`, 1), full, false},
		{"inexact revision", strings.Replace(full, "9007199254740993", "9007199254740992", 1), full, false},
		{"duplicate object field", strings.Replace(full, `"revision":2`, `"revision":1,"revision":2`, 1), full, false},
		{"trailing JSON", full + " {}", full, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := historyExportsEqual(json.RawMessage(test.got), json.RawMessage(test.want)); got != test.same {
				t.Fatalf("historyExportsEqual = %v, want %v", got, test.same)
			}
		})
	}
}

func TestJSONExportsEqual(t *testing.T) {
	t.Parallel()

	want := json.RawMessage(`{"positions":[],"revision":9007199254740993}`)
	if !jsonExportsEqual(json.RawMessage(`{ "revision": 9007199254740993, "positions": [] }`), want) {
		t.Fatal("equivalent positions exports differ")
	}

	for _, got := range []json.RawMessage{
		json.RawMessage(`{"positions":[],"revision":9007199254740992}`),
		json.RawMessage(`{"positions":null,"revision":9007199254740993}`),
		json.RawMessage(`{"revision":9007199254740993}`),
	} {
		if jsonExportsEqual(got, want) {
			t.Fatalf("different positions exports compared equal: %s and %s", got, want)
		}
	}
}
