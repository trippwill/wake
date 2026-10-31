// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"database/sql"
	"database/sql/driver"
	"math/big"
	"strings"
	"sync"

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

	a, _ := new(big.Rat).SetString(l.Value())
	b, _ := new(big.Rat).SetString(r.Value())

	return a.Cmp(b)
}
