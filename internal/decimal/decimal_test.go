// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package decimal

import (
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"-0.00", "0.00"},
		{"+001.2300e1", "12.300"},
		{"1e128", "1" + strings.Repeat("0", 128)},
		{"1e-128", "0." + strings.Repeat("0", 127) + "1"},
		{strings.Repeat("9", 256), strings.Repeat("9", 256)},
	} {
		got, err := Normalize(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("Normalize(%q) = %q, %v, want %q", tc.input, got, err, tc.want)
		}
	}

	for _, input := range []string{
		"", " 1", "1/2", "1.2.3", "1e", "1e129", "1e-129",
		strings.Repeat("9", 257), strings.Repeat("9", 129) + "e128",
	} {
		if got, err := Normalize(input); !errors.Is(err, errInvalid) {
			t.Errorf("Normalize(%q) = %q, %v, want invalid decimal error", input, got, err)
		}
	}
}

func TestNormalizedOperations(t *testing.T) {
	for _, tc := range []struct {
		text, magnitude    string
		positive, negative bool
	}{
		{"", "", false, false},
		{"0.00", "0.00", false, false},
		{"1.200", "1.200", true, false},
		{"-1.200", "1.200", false, true},
	} {
		if Abs(tc.text) != tc.magnitude || Positive(tc.text) != tc.positive || Negative(tc.text) != tc.negative {
			t.Errorf("incorrect magnitude or sign for %q", tc.text)
		}
	}

	if Equal("", "0.00") || !Equal("", "") || !Equal("0", "0.00") || !Equal("-1.200", "-1.2") {
		t.Fatal("incorrect equality for unset or scale variants")
	}
}

func TestCompare(t *testing.T) {
	values := []string{
		"-1000", "-.0001e-9", "-0.0000", "0", "0.0000000000001",
		"+0002.00e0", "2", "10", "9007199254740993.0000000000001",
		"9007199254740993.0000000000002", strings.Repeat("9", 256),
	}
	for _, left := range values {
		for _, right := range values {
			a, aOK := new(big.Rat).SetString(left)
			b, bOK := new(big.Rat).SetString(right)
			got, leftOK, rightOK := Compare(left, right)
			if !aOK || !bOK || !leftOK || !rightOK || got != a.Cmp(b) {
				t.Errorf(
					"Compare(%q, %q) = %d, %t, %t disagrees with exact rational order",
					left,
					right,
					got,
					leftOK,
					rightOK,
				)
			}
		}
	}

	_, leftOK, rightOK := Compare("1/2", "1")
	if leftOK || !rightOK {
		t.Fatal("comparison lost operand validity")
	}
}
