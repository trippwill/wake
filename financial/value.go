// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financial

import (
	"fmt"
	"strconv"
	"time"

	"go.trippwill.dev/wake/internal/decimal"
)

const (
	// ErrInvalidCurrency is returned when a currency is not a three-letter
	// uppercase code.
	ErrInvalidCurrency Error = "invalid currency"
	// ErrInvalidNumber is returned when a financial value contains an invalid
	// or error-state number.
	ErrInvalidNumber Error = "invalid number"
	// ErrInvalidDate is returned when a civil date is malformed or does not
	// exist in the Gregorian calendar.
	ErrInvalidDate Error = "invalid date"
)

// Currency is the three-letter uppercase code of a source-stated currency.
// Its private representation prevents construction without validation, apart
// from Go's invalid zero value.
type Currency struct {
	code [3]byte
}

// ParseCurrency parses a three-letter uppercase currency code.
func ParseCurrency(text string) (Currency, error) {
	if len(text) != 3 {
		return Currency{}, fmt.Errorf(
			"%w: expected three uppercase ASCII letters",
			ErrInvalidCurrency,
		)
	}

	for _, character := range text {
		if character < 'A' || character > 'Z' {
			return Currency{}, fmt.Errorf(
				"%w: expected three uppercase ASCII letters",
				ErrInvalidCurrency,
			)
		}
	}

	var code [3]byte
	copy(code[:], text)

	return Currency{code: code}, nil
}

func validateCurrency(value Currency) error {
	for _, character := range value.code {
		if character < 'A' || character > 'Z' {
			return fmt.Errorf(
				"%w: expected three uppercase ASCII letters",
				ErrInvalidCurrency,
			)
		}
	}

	return nil
}

// String returns the currency code.
func (value Currency) String() string { return string(value.code[:]) }

// MarshalText implements encoding.TextMarshaler.
func (value Currency) MarshalText() ([]byte, error) {
	if err := validateCurrency(value); err != nil {
		return nil, err
	}

	return []byte(value.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. The receiver is left
// unchanged when parsing fails.
func (value *Currency) UnmarshalText(text []byte) error {
	parsed, err := ParseCurrency(string(text))
	if err != nil {
		return err
	}

	*value = parsed

	return nil
}

// Money is an exact decimal amount paired with its source-stated currency.
type Money struct {
	amount   string
	currency Currency
}

func ParseMoney(amount string, currency Currency) (Money, error) {
	text, err := decimal.Normalize(amount)
	if err != nil {
		return Money{}, ErrInvalidNumber
	}

	if err = validateCurrency(currency); err != nil {
		return Money{}, err
	}

	return Money{amount: text, currency: currency}, nil
}

func (value Money) Amount() string     { return value.amount }
func (value Money) Currency() Currency { return value.currency }

// Quantity is an exact decimal count or measure. Record constructors enforce sign constraints.
type Quantity struct{ value string }

func ParseQuantity(value string) (Quantity, error) {
	text, err := decimal.Normalize(value)
	if err != nil {
		return Quantity{}, ErrInvalidNumber
	}

	return Quantity{value: text}, nil
}

// Negative reports whether the quantity is below zero.
func (value Quantity) Negative() bool { return decimal.Negative(value.value) }

func (value Quantity) Value() string { return value.value }

// Positive checks the exact value without a rounded conversion.
func (value Quantity) Positive() bool {
	return decimal.Positive(value.value)
}

// Abs preserves exact magnitude without arithmetic conversion.
func (value Quantity) Abs() Quantity { return Quantity{value: decimal.Abs(value.value)} }

// Price is an exact decimal amount in a stated currency. Its quotation basis
// belongs to the reported context and may be unavailable.
type Price struct {
	amount   string
	currency Currency
}

func ParsePrice(amount string, currency Currency) (Price, error) {
	text, err := decimal.Normalize(amount)
	if err != nil {
		return Price{}, ErrInvalidNumber
	}

	if err = validateCurrency(currency); err != nil {
		return Price{}, err
	}

	return Price{amount: text, currency: currency}, nil
}

// Negative reports whether the price is below zero.
func (value Price) Negative() bool { return decimal.Negative(value.amount) }

func (value Price) Amount() string     { return value.amount }
func (value Price) Currency() Currency { return value.currency }

// Equal compares numeric meaning and currency without discarding retained precision.
func (value Money) Equal(other Money) bool {
	return value.currency == other.currency && decimal.Equal(value.amount, other.amount)
}

func (value Price) Equal(other Price) bool {
	return value.currency == other.currency && decimal.Equal(value.amount, other.amount)
}

func (value Quantity) Equal(other Quantity) bool {
	return decimal.Equal(value.value, other.value)
}

// Date is a validated Gregorian civil date without a time or timezone.
type Date struct {
	year  uint16
	month uint8
	day   uint8
}

// NewDate validates and returns a civil date.
func NewDate(year int, month time.Month, day int) (Date, error) {
	if year < 1 || year > 9999 || month < time.January || month > time.December || day < 1 || day > 31 {
		return Date{}, fmt.Errorf(
			"%w: %04d-%02d-%02d",
			ErrInvalidDate,
			year,
			month,
			day,
		)
	}

	if !validDate(year, month, day) {
		return Date{}, fmt.Errorf(
			"%w: %04d-%02d-%02d",
			ErrInvalidDate,
			year,
			month,
			day,
		)
	}

	return Date{year: uint16(year), month: uint8(month), day: uint8(day)}, nil
}

// ParseDate parses a civil date in strict YYYY-MM-DD form.
func ParseDate(text string) (Date, error) {
	if len(text) != len("YYYY-MM-DD") || text[4] != '-' || text[7] != '-' {
		return Date{}, fmt.Errorf("%w: expected YYYY-MM-DD", ErrInvalidDate)
	}

	for index, character := range text {
		if index == 4 || index == 7 {
			continue
		}

		if character < '0' || character > '9' {
			return Date{}, fmt.Errorf("%w: expected YYYY-MM-DD", ErrInvalidDate)
		}
	}

	year, _ := strconv.Atoi(text[0:4])
	month, _ := strconv.Atoi(text[5:7])
	day, _ := strconv.Atoi(text[8:10])

	return NewDate(year, time.Month(month), day)
}

func validateDate(value Date) error {
	if !validDate(value.Year(), value.Month(), value.Day()) {
		return ErrInvalidDate
	}

	return nil
}

// Year returns the civil year.
func (value Date) Year() int { return int(value.year) }

// Month returns the civil month.
func (value Date) Month() time.Month { return time.Month(value.month) }

// Day returns the day of the month.
func (value Date) Day() int { return int(value.day) }

// IsZero reports whether the date is its invalid zero value.
func (value Date) IsZero() bool { return value == Date{} }

// String returns the date in YYYY-MM-DD form.
func (value Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", value.Year(), value.Month(), value.Day())
}

// MarshalText implements encoding.TextMarshaler.
func (value Date) MarshalText() ([]byte, error) {
	if err := validateDate(value); err != nil {
		return nil, err
	}

	return []byte(value.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. The receiver is left
// unchanged when parsing fails.
func (value *Date) UnmarshalText(text []byte) error {
	parsed, err := ParseDate(string(text))
	if err != nil {
		return err
	}

	*value = parsed

	return nil
}

func validDate(year int, month time.Month, day int) bool {
	if year < 1 || year > 9999 || month < time.January || month > time.December ||
		day < 1 {
		return false
	}

	days := [...]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

	maximum := days[month]
	if month == time.February && (year%400 == 0 || year%4 == 0 && year%100 != 0) {
		maximum++
	}

	return day <= maximum
}
