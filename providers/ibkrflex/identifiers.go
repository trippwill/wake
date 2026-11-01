// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

// Confirmation reports supply an untyped underlyingSecurityID. Only a complete
// ISIN (including its check digit) establishes the scheme we emit.
func underlierISIN(value string) bool {
	if len(value) != 12 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' ||
		value[11] < '0' ||
		value[11] > '9' {
		return false
	}

	digits := []int{}
	for _, c := range value {
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, int(c-'0'))
		case c >= 'A' && c <= 'Z':
			n := int(c-'A') + 10
			digits = append(digits, n/10, n%10)
		default:
			return false
		}
	}

	total := 0
	for i, d := range digits {
		if (len(digits)-i)%2 == 0 {
			d *= 2
		}

		total += d/10 + d%10
	}

	return total%10 == 0
}

// The symbol is evidence, not a contract synthesized from potentially rounded terms.
func occIdentifier(symbol string, terms source.OptionTerms) (source.Identifier, bool) {
	if len(symbol) != 21 || strings.TrimSpace(symbol[:6]) == "" {
		return source.Identifier{}, false
	}

	// OCC roots are left aligned and space padded; embedded/leading spaces
	// cannot establish a reported root. This interpreter supports 2000-2099 dates.
	if strings.Contains(strings.TrimRight(symbol[:6], " "), " ") {
		return source.Identifier{}, false
	}

	for _, c := range symbol[:6] {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != ' ' && c != '.' {
			return source.Identifier{}, false
		}
	}

	for _, part := range []string{symbol[6:12], symbol[13:]} {
		if !digits(part) {
			return source.Identifier{}, false
		}
	}

	f := terms.Fields()
	if f.Expiry.IsZero() || !strings.HasPrefix(f.Expiry.String(), "20") ||
		strings.ReplaceAll(f.Expiry.String()[2:], "-", "") != symbol[6:12] {
		return source.Identifier{}, false
	}

	if (f.Right != source.Call || symbol[12] != 'C') && (f.Right != source.Put || symbol[12] != 'P') {
		return source.Identifier{}, false
	}

	strike, err := financial.NewQuantity(symbol[13:18] + "." + symbol[18:])
	if err != nil {
		return source.Identifier{}, false
	}

	reported, err := financial.NewQuantity(f.Strike.Amount())
	if err != nil || !strike.Equal(reported) {
		return source.Identifier{}, false
	}

	id, err := source.NewIdentifier(source.OCC, symbol, "symbol")

	return id, err == nil
}
