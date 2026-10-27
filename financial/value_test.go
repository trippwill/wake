// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financial_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/num"
	"go.trippwill.dev/wake/financial"
)

func TestCurrencyRoundTrip(t *testing.T) {
	for _, text := range []string{"GBP", "USD", "EUR", "JPY", "XAU"} {
		t.Run(text, func(t *testing.T) {
			value, err := financial.ParseCurrency(text)
			if err != nil {
				t.Fatalf("ParseCurrency(%q) error = %v", text, err)
			}

			if value.String() != text {
				t.Errorf("String() = %q, want %q", value, text)
			}

			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			if string(encoded) != `"`+text+`"` {
				t.Errorf("json.Marshal() = %s, want %q", encoded, text)
			}

			var decoded financial.Currency
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", encoded, err)
			}

			if decoded != value {
				t.Errorf("JSON round trip = %q, want %q", decoded, value)
			}
		})
	}
}

func TestCurrencyRejectsInvalidCodes(t *testing.T) {
	for _, text := range []string{"", "US", "USDD", "usd", "Usd", "US1", " US", "US ", "£££"} {
		t.Run(text, func(t *testing.T) {
			_, err := financial.ParseCurrency(text)
			if !errors.Is(err, financial.ErrInvalidCurrency) {
				t.Fatalf(
					"ParseCurrency(%q) error = %v, want ErrInvalidCurrency",
					text,
					err,
				)
			}
		})
	}
}

func TestCurrencyUnmarshalFailurePreservesReceiver(t *testing.T) {
	value := parseCurrency(t, "USD")
	original := value
	if err := value.UnmarshalText(
		[]byte("usd"),
	); !errors.Is(
		err,
		financial.ErrInvalidCurrency,
	) {
		t.Fatalf("UnmarshalText() error = %v, want ErrInvalidCurrency", err)
	}

	if value != original {
		t.Errorf("UnmarshalText() changed receiver to %q, want USD", value)
	}
}

func TestZeroCurrencyCannotBeMarshaled(t *testing.T) {
	var value financial.Currency
	if _, err := value.MarshalText(); !errors.Is(err, financial.ErrInvalidCurrency) {
		t.Fatalf("MarshalText() error = %v, want ErrInvalidCurrency", err)
	}
}

func TestFinancialValuesPreserveExactMeaning(t *testing.T) {
	usd, err := financial.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ input, exact string }{
		{"+001.23456789012345678900", "1.234567890123456789"},
		{"-0.0000000000001", "-0.0000000000001"},
		{"1e-10", "0.0000000001"},
		{"1.2e+3", "1200"},
		{"-0.000", "0"},
		{".5", "0.5"},
		{"12.", "12"},
		{"12345678901234.123456", "12345678901234.123456"},
	} {
		money, e := financial.ParseMoney(test.input, usd)
		if e != nil || money.Amount() != test.exact {
			t.Fatalf("money %q: %q, %v", test.input, money.Amount(), e)
		}

		quantity, e := financial.ParseQuantity(test.input)
		if e != nil || quantity.Value() != test.exact {
			t.Fatalf("quantity %q: %q, %v", test.input, quantity.Value(), e)
		}

		price, e := financial.ParsePrice(test.input, usd)
		if e != nil || price.Amount() != test.exact {
			t.Fatalf("price %q: %q, %v", test.input, price.Amount(), e)
		}
	}
}

func TestDerivedNumConversion(t *testing.T) {
	usd := parseCurrency(t, "USD")
	for _, exact := range []string{
		"0", "1.2345678905", "-0.1234567896", "0.0000000001",
		"9999999999.999999999", "10000000000", "-10000000000",
		strings.Repeat("9", 256), "0." + strings.Repeat("0", 253) + "1",
	} {
		t.Run(exact, func(t *testing.T) {
			money, err := financial.NewMoney(exact, usd)
			if err != nil {
				t.Fatal(err)
			}

			quantity, err := financial.NewQuantity(exact)
			if err != nil {
				t.Fatal(err)
			}

			price, err := financial.NewPrice(exact, usd)
			if err != nil {
				t.Fatal(err)
			}

			for _, convert := range []func() (num.Num, error){money.Num, quantity.Num, price.Num} {
				value, e := convert()
				if e != nil || !value.Ok() || value.String() != exact {
					t.Fatalf("conversion %s: %s, %v", exact, value, e)
				}
			}

			if money.Amount() != exact || quantity.Value() != exact || price.Amount() != exact {
				t.Fatal("conversion changed source")
			}
		})
	}
}

func TestDerivedNumCalculationsPreservePrecision(t *testing.T) {
	small, err := financial.ParseQuantity("0.0000000001")
	if err != nil {
		t.Fatal(err)
	}

	large, err := financial.ParsePrice("10000000000", parseCurrency(t, "USD"))
	if err != nil {
		t.Fatal(err)
	}

	q, err := small.Num()
	if err != nil {
		t.Fatal(err)
	}

	p, err := large.Num()
	if err != nil {
		t.Fatal(err)
	}

	product := q.Mul(p)
	if !product.Ok() || !product.Equal(num.FromInt64(1)) {
		t.Fatalf("product = %s", product)
	}

	for _, test := range []struct{ exact, rounded string }{
		{"1.00000000050000000001", "1.000000001"},
		{"-1.00000000050000000001", "-1.000000001"},
		{"1.2345678905", "1.234567890"},
		{"1.2345678915", "1.234567892"},
	} {
		source, e := financial.ParseQuantity(test.exact)
		if e != nil {
			t.Fatal(e)
		}

		value, e := source.Num()
		if e != nil {
			t.Fatal(e)
		}

		rounded := value.Quantize(9)
		if !rounded.Ok() || rounded.String() != test.rounded || value.String() != test.exact ||
			source.Value() != test.exact {
			t.Fatalf("quantization of %s: %s", test.exact, rounded)
		}
	}

	quotient := num.FromInt64(1).Div(num.FromInt64(3))
	if !quotient.Ok() || quotient.String() != "0."+strings.Repeat("3", 34) {
		t.Fatalf("division = %s", quotient)
	}
}

func TestFinancialConstructorsRejectInvalidInputs(t *testing.T) {
	usd := parseCurrency(t, "USD")
	for _, text := range []string{"", " ", "NaN", "Inf", "1/2", "0x10", "1.2.3", "1e", "1e129", "1e-129", "--1", strings.Repeat("1", 257)} {
		_, m := financial.NewMoney(text, usd)
		_, q := financial.NewQuantity(text)
		_, p := financial.NewPrice(text, usd)
		for _, err := range []error{m, q, p} {
			if !errors.Is(err, financial.ErrInvalidNumber) {
				t.Fatalf("%q: %v", text, err)
			}
		}
	}

	if _, err := financial.NewMoney("0", financial.Currency{}); !errors.Is(err, financial.ErrInvalidCurrency) {
		t.Fatal(err)
	}

	if _, err := financial.NewPrice("0", financial.Currency{}); !errors.Is(err, financial.ErrInvalidCurrency) {
		t.Fatal(err)
	}

	var quantity financial.Quantity
	if _, err := quantity.Num(); !errors.Is(err, financial.ErrInvalidNumber) {
		t.Fatal(err)
	}

	for _, text := range []string{"1e-128", "1e128"} {
		q, err := financial.ParseQuantity(text)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = financial.ParseQuantity(q.Value()); err != nil {
			t.Fatal("canonical form cannot round trip", err)
		}
	}
}

func parseCurrency(t *testing.T, text string) financial.Currency {
	t.Helper()

	value, err := financial.ParseCurrency(text)
	if err != nil {
		t.Fatal(err)
	}

	return value
}

func TestDateConstructionAndRoundTrip(t *testing.T) {
	value, err := financial.NewDate(2024, time.February, 29)
	if err != nil {
		t.Fatalf("NewDate() error = %v", err)
	}

	if value.Year() != 2024 || value.Month() != time.February ||
		value.Day() != 29 {
		t.Errorf(
			"date components = %d-%d-%d",
			value.Year(),
			value.Month(),
			value.Day(),
		)
	}

	if value.IsZero() {
		t.Error("valid Date reports zero")
	}

	if value.String() != "2024-02-29" {
		t.Errorf("String() = %q, want 2024-02-29", value)
	}

	parsed, err := financial.ParseDate(value.String())
	if err != nil {
		t.Fatalf("ParseDate() error = %v", err)
	}

	if parsed != value {
		t.Errorf("ParseDate() = %v, want %v", parsed, value)
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	if string(encoded) != `"2024-02-29"` {
		t.Errorf("json.Marshal() = %s, want date string", encoded)
	}

	var decoded financial.Date
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", encoded, err)
	}

	if decoded != value {
		t.Errorf("JSON round trip = %v, want %v", decoded, value)
	}
}

func TestDateRejectsInvalidValues(t *testing.T) {
	tests := []string{
		"", "0000-01-01", "10000-01-01", "2023-02-29", "1900-02-29",
		"2024-00-01", "2024-13-01", "2024-01-00", "2024-04-31",
		"2024-2-29", "2024-02-9", "2024/02/29", "2024-02-29 ",
		" 2024-02-29", "202A-02-29",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			_, err := financial.ParseDate(text)
			if !errors.Is(err, financial.ErrInvalidDate) {
				t.Fatalf("ParseDate(%q) error = %v, want ErrInvalidDate", text, err)
			}
		})
	}

	for _, test := range []struct {
		year  int
		month time.Month
		day   int
	}{
		{year: 0, month: time.January, day: 1},
		{year: 2024, month: 0, day: 1},
		{year: 2024, month: time.February, day: 30},
		{year: 10000, month: time.January, day: 1},
	} {
		if _, err := financial.NewDate(
			test.year,
			test.month,
			test.day,
		); !errors.Is(
			err,
			financial.ErrInvalidDate,
		) {
			t.Errorf(
				"NewDate(%d, %d, %d) error = %v, want ErrInvalidDate",
				test.year,
				test.month,
				test.day,
				err,
			)
		}
	}
}

func TestDateUnmarshalFailurePreservesReceiver(t *testing.T) {
	value, err := financial.ParseDate("2024-02-29")
	if err != nil {
		t.Fatal(err)
	}

	original := value

	if err := value.UnmarshalText(
		[]byte("2023-02-29"),
	); !errors.Is(
		err,
		financial.ErrInvalidDate,
	) {
		t.Fatalf("UnmarshalText() error = %v, want ErrInvalidDate", err)
	}

	if value != original {
		t.Errorf("UnmarshalText() changed receiver to %v, want %v", value, original)
	}
}

func TestZeroDateCannotBeMarshaled(t *testing.T) {
	var value financial.Date
	if !value.IsZero() {
		t.Error("zero Date does not report zero")
	}

	if _, err := value.MarshalText(); !errors.Is(err, financial.ErrInvalidDate) {
		t.Fatalf("MarshalText() error = %v, want ErrInvalidDate", err)
	}
}
