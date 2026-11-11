// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"cmp"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"time"

	"go.trippwill.dev/wake/financial"
	modernsqlite "modernc.org/sqlite"
)

var (
	collationOnce  sync.Once
	collationError error
	sqliteDriver   driver.Driver
)

func registerCollations() error {
	collationOnce.Do(func() {
		collationError = modernsqlite.RegisterCollationUtf8("wake_decimal", compareDecimalText)
		if collationError != nil {
			return
		}

		collationError = modernsqlite.RegisterCollationUtf8("wake_instant", compareInstantText)
		if collationError != nil {
			return
		}

		collationError = modernsqlite.RegisterDeterministicScalarFunction("wake_contains", 2,
			func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				haystack, ok := args[0].(string)
				if !ok {
					return int64(0), nil
				}

				needle, ok := args[1].(string)
				if !ok {
					return int64(0), nil
				}

				if strings.Contains(strings.ToLower(haystack), strings.ToLower(needle)) {
					return int64(1), nil
				}

				return int64(0), nil
			})
		if collationError != nil {
			return
		}

		collationError = modernsqlite.RegisterDeterministicScalarFunction("wake_fold", 1,
			func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				value, ok := args[0].(string)
				if !ok {
					return nil, nil
				}

				return strings.ToLower(value), nil
			})
		if collationError != nil {
			return
		}

		// Collations belong to the registered driver instance. A zero Driver has none.
		// sql.Open is lazy: obtaining its driver opens no connection or filesystem path.
		pool, err := sql.Open("sqlite", "")
		if err != nil {
			collationError = err
			return
		}

		sqliteDriver = pool.Driver()
		collationError = pool.Close()
	})

	return collationError
}

// Invalid values occupy a separate deterministic partition. Mixing lexical and
// numeric comparisons within one partition would violate collation transitivity.
func compareDecimalText(left, right string) int {
	if left == right {
		return 0
	}

	a, l, leftOK := decimalMagnitude(left)
	b, r, rightOK := decimalMagnitude(right)
	if !leftOK || !rightOK {
		return compareUnnormalizedDecimals(left, right)
	}

	return compareMagnitudes(a, l, b, r)
}

type decimalOrder struct {
	integerDigits int
	negative      bool
}

// decimalMagnitude recognizes the plain form emitted by the financial parser.
// Other forms are handled by the financial parser.
func decimalMagnitude(text string) (string, decimalOrder, bool) {
	if len(text) == 0 || len(text) > 256 {
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
		magnitude = strings.TrimSuffix(strings.TrimRight(magnitude, "0"), ".")
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

func compareUnnormalizedDecimals(left, right string) int {
	l, le := financial.NewQuantity(left)
	r, re := financial.NewQuantity(right)
	if le != nil && re != nil {
		return strings.Compare(left, right)
	}

	if le != nil {
		return 1
	}

	if re != nil {
		return -1
	}

	a, ao, _ := decimalMagnitude(l.Value())
	b, bo, _ := decimalMagnitude(r.Value())

	return compareMagnitudes(a, ao, b, bo)
}

// RFC3339 text can vary in fractional precision and offset. Compare instants
// without SQLite's floating-point timestamp conversion.
func compareInstantText(left, right string) int {
	l, le := time.Parse(time.RFC3339Nano, left)
	r, re := time.Parse(time.RFC3339Nano, right)
	if le != nil && re != nil {
		return strings.Compare(left, right)
	}

	if le != nil {
		return 1
	}

	if re != nil {
		return -1
	}

	return l.Compare(r)
}
