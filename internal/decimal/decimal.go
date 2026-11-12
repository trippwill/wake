// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package decimal

import (
	"cmp"
	"strconv"
	"strings"
)

const maxBytes = 256

type decimalError string

func (e decimalError) Error() string { return string(e) }

const errInvalid decimalError = "invalid decimal text"

// Normalize expands finite base-ten notation while retaining fractional scale
// without rounding. Input and expanded output
// are bounded to 256 bytes and exponents to +/-128 to prevent amplification.
func Normalize(text string) (string, error) {
	if text == "" || len(text) > maxBytes {
		return "", errInvalid
	}

	negative := text[0] == '-'
	if text[0] == '-' || text[0] == '+' {
		text = text[1:]
	}

	exponent := 0
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		var err error

		exponent, err = strconv.Atoi(text[i+1:])
		if err != nil || exponent < -128 || exponent > 128 {
			return "", errInvalid
		}

		text = text[:i]
	}

	point := strings.IndexByte(text, '.')
	if point < 0 {
		point = len(text)
	}

	digits := strings.Replace(text, ".", "", 1)
	if digits == "" {
		return "", errInvalid
	}

	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", errInvalid
		}
	}

	point += exponent
	if point < 0 {
		digits = strings.Repeat("0", -point) + digits
		point = 0
	}

	if point > len(digits) {
		digits += strings.Repeat("0", point-len(digits))
	}

	integer := strings.TrimLeft(digits[:point], "0")
	fraction := digits[point:]

	if integer == "" {
		integer = "0"
	}

	result := integer
	if fraction != "" {
		result += "." + fraction
	}

	if negative && trimScale(result) != "0" {
		result = "-" + result
	}

	if len(result) > maxBytes {
		return "", errInvalid
	}

	return result, nil
}

// Equal compares normalized text by numeric value. Empty text represents an unset value.
func Equal(left, right string) bool {
	return left == right || trimScale(left) == trimScale(right)
}

// Positive reports whether normalized text represents a positive value.
func Positive(text string) bool {
	return text != "" && !Negative(text) && trimScale(text) != "0"
}

// Negative reports whether normalized text represents a negative value.
func Negative(text string) bool { return strings.HasPrefix(text, "-") }

// Abs returns the magnitude of normalized text, preserving scale.
func Abs(text string) string { return strings.TrimPrefix(text, "-") }

func trimScale(text string) string {
	if strings.Contains(text, ".") {
		return strings.TrimSuffix(strings.TrimRight(text, "0"), ".")
	}

	return text
}

// Compare orders exact decimals and reports each operand's validity.
// The ordering result is meaningful when both operands are valid.
func Compare(left, right string) (order int, leftOK, rightOK bool) {
	a, l, leftOK := orderParts(left)
	b, r, rightOK := orderParts(right)
	if !leftOK || !rightOK {
		return 0, leftOK, rightOK
	}
	return compareMagnitudes(a, l, b, r), true, true
}

func orderParts(text string) (string, decimalOrder, bool) {
	magnitude, order, ok := decimalMagnitude(text)
	if ok {
		return magnitude, order, true
	}
	normalized, err := Normalize(text)
	if err != nil {
		return "", decimalOrder{}, false
	}
	return decimalMagnitude(normalized)
}

type decimalOrder struct {
	integerDigits int
	negative      bool
}

// decimalMagnitude recognizes the plain form emitted by the financial parser.
// Other forms are handled by Normalize.
func decimalMagnitude(text string) (string, decimalOrder, bool) {
	if len(text) == 0 || len(text) > maxBytes {
		return "", decimalOrder{}, false
	}

	magnitude := strings.TrimPrefix(text, "-")

	negative := len(magnitude) != len(text)
	point := len(magnitude)
	nonzero := false
	for i := range len(magnitude) {
		digit := magnitude[i]
		if digit == '.' && point == len(magnitude) && i > 0 && i+1 < len(magnitude) {
			point = i

			continue
		}

		if digit < '0' || digit > '9' {
			return "", decimalOrder{}, false
		}

		nonzero = nonzero || digit != '0'
	}

	if magnitude == "" || (point > 1 && magnitude[0] == '0') || (negative && !nonzero) {
		return "", decimalOrder{}, false
	}

	if point < len(magnitude) {
		magnitude = trimScale(magnitude)
	}

	return magnitude, decimalOrder{integerDigits: point, negative: negative}, true
}

func compareMagnitudes(left string, l decimalOrder, right string, r decimalOrder) int {
	if l.negative != r.negative {
		if l.negative {
			return -1
		}

		return 1
	}

	order := cmp.Compare(l.integerDigits, r.integerDigits)
	if order == 0 {
		order = strings.Compare(left, right)
	}

	if l.negative {
		return -order
	}

	return order
}
