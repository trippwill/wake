// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"context"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func normalizePositions(
	ctx context.Context,
	body []byte,
	sink provider.Claims,
	counts *provider.Counts,
) error {
	type statement struct {
		input                     source.PositionSetInput
		sections, rows, summaries int
		invalid                   bool
		attemptDate               financial.Date
	}

	sets := []*statement{}

	var current *statement

	err := walk(ctx, body, func(path []node) error {
		n := path[len(path)-1]
		if len(path) == 3 && path[1].name == "FlexStatements" && n.name == "FlexStatement" {
			ref, e := source.NewReference("ibkr", "account", "", n.attributes["accountId"])
			if e != nil {
				return e
			}

			date, _ := parseDate(n.attributes["toDate"])
			current = &statement{
				input: source.PositionSetInput{
					Location: location(path) + "/OpenPositions",
					Account:  ref,
					Date:     date,
				},
			}
			sets = append(sets, current)
		}

		if current == nil || len(path) < 4 || path[1].name != "FlexStatements" ||
			path[2].name != "FlexStatement" {
			return nil
		}

		if len(path) == 4 && n.name == "OpenPositions" {
			current.sections++
			return nil
		}

		if path[3].name != "OpenPositions" {
			return nil
		}

		if len(path) != 5 || n.name != "OpenPosition" {
			current.invalid = true
			return nil
		}

		current.rows++

		if n.attributes["levelOfDetail"] == "LOT" {
			return nil
		}

		if n.attributes["levelOfDetail"] != "SUMMARY" {
			current.invalid = true
			return nil
		}

		current.summaries++

		date, e := parseDate(n.attributes["reportDate"])
		if e != nil {
			current.invalid = true
			return nil
		}

		if current.attemptDate.IsZero() || date.String() > current.attemptDate.String() {
			current.attemptDate = date
		}

		if current.summaries == 1 {
			current.input.Date = date
		} else if current.input.Date != date {
			current.invalid = true
		}

		p, e := reportedPosition(n.attributes, current.input.Account.Value())
		if e != nil {
			current.invalid = true
			return nil
		}

		current.input.Positions = append(current.input.Positions, p)
		if len(current.input.Positions) > 100000 {
			return provider.ErrLimit
		}

		return nil
	})
	if err != nil {
		return err
	}

	for _, set := range sets {
		f := set.input
		f.Complete = set.sections == 1 && !f.Date.IsZero() && !set.invalid && (set.rows == 0 || set.summaries > 0)
		if !f.Complete {
			if !set.attemptDate.IsZero() {
				f.Date = set.attemptDate
			}

			f.Positions = nil
			f.Reason = "incomplete"
			if set.sections == 0 && !set.invalid {
				f.Reason = "not_reported"
			}
		}

		o, e := source.NewPositionSetObservation(f)
		if e != nil {
			return e
		}

		if e = sink.PutPositionSet(ctx, o); e != nil {
			return e
		}

		counts.Rows += 1 + uint64(len(f.Positions))
		counts.PositionSets++
		counts.Instruments += uint64(len(f.Positions))
		if counts.Rows > 100000 {
			return provider.ErrLimit
		}
	}

	return nil
}

func reportedPosition(row map[string]string, account string) (source.Position, error) {
	if row["accountId"] != "" && row["accountId"] != account || row["model"] != "" {
		return source.Position{}, provider.ErrMalformed
	}

	switch row["assetCategory"] {
	case "STK", "OPT", "BOND", "BILL":
	default:
		return source.Position{}, provider.ErrUnavailable
	}

	i, err := rowInstrument(row)
	if err != nil {
		return source.Position{}, err
	}

	q, err := financial.NewQuantity(row["position"])
	if err != nil {
		return source.Position{}, err
	}

	switch row["side"] {
	case "Long":
		if !q.Positive() {
			return source.Position{}, provider.ErrMalformed
		}
	case "Short":
		if !strings.HasPrefix(q.Value(), "-") {
			return source.Position{}, provider.ErrMalformed
		}
	case "":
	default:
		return source.Position{}, provider.ErrMalformed
	}

	var price financial.Price
	if c, e := financial.ParseCurrency(row["currency"]); e == nil && row["costBasisPrice"] != "" {
		price, _ = financial.NewPrice(row["costBasisPrice"], c)
	}

	return source.NewPosition(i, q, price)
}
