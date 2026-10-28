// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
)

type (
	correctionChoice  struct{ Value, Label string }
	correctionControl struct {
		Kind, Label, Hint, Unit, Pattern string
		Required, Monetary               bool
		Limit                            int
		Choices                          []correctionChoice
	}
)

const decimalPattern = `[+\-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+\-]?[0-9]+)?`

func selectedCorrectionField(ctx context.Context, f application.CorrectionForm) string {
	field := page(ctx).Query.Get("field")
	if f.Draft.Scope != "" {
		field = f.Draft.Scope
	}

	if !slices.Contains(f.Fields, field) {
		return ""
	}

	return field
}

func correctionEditValue(f application.CorrectionForm, field string) application.CorrectionValue {
	v := f.Values[field]
	if f.Draft.Scope == field {
		v.Value, v.Currency = f.Draft.Value, f.Draft.Currency
	}

	return v
}

func correctionSpec(f application.CorrectionForm, field string) correctionControl {
	c := correctionControl{
		Kind:     "text",
		Pattern:  ".*",
		Label:    fieldLabel(field),
		Required: true,
		Limit:    2048,
		Hint:     "Enter the corrected value.",
	}
	c.Unit = f.Values[field].Unit
	switch field {
	case "instrument", "listing", "underlier":
		c.Kind, c.Hint = "link", "Search by name, symbol or reference. Choose a retained record from the list."
		c.Required = field == "instrument"
		for _, link := range f.Links {
			if link.Scope == field {
				c.Choices = append(c.Choices, correctionChoice{link.Value, link.Label})
			}
		}
	case "side":
		c.Kind = "choice"
		c.Choices = []correctionChoice{{"buy", "Buy"}, {"sell", "Sell"}}
	case "kind":
		c.Kind = "choice"
		c.Choices = []correctionChoice{
			{"exercise", "Exercise"},
			{"assignment", "Assignment"},
			{"expiration", "Expiration"},
		}
	case "option.right":
		c.Kind = "choice"
		c.Required = false
		c.Choices = []correctionChoice{{"put", "Put"}, {"call", "Call"}}
	case "position_effect":
		c.Kind = "choice"
		c.Required = false
		c.Choices = []correctionChoice{{"opening", "Opening"}, {"closing", "Closing"}, {"mixed", "Mixed"}}
	case "quotation_basis":
		c.Kind = "choice"
		c.Required = false
		c.Choices = []correctionChoice{
			{"per 100 nominal", "Per 100 nominal"},
			{"per nominal unit", "Per nominal unit"},
		}
	case "trade_date", "settlement_date", "option.expiry", "debt.maturity":
		c.Kind, c.Hint = "date", "Choose a valid calendar date."
		c.Required = field == "trade_date"
		if field == "debt.maturity" {
			c.Hint = "Choose a maturity date consistent with the issue and dated dates."
		}
	case "executed_at":
		c.Kind = "timestamp"
		c.Required = false
		c.Hint = "Keep the reported time, fractional seconds and UTC offset. Leave all three parts blank for Unknown."
	case "name":
		c.Limit = 1024
		c.Hint = "Enter a readable instrument name."
	case "ticker":
		c.Limit = 256
		c.Required = false
		c.Hint = "Enter the reported symbol, or leave blank for Unknown."
	default:
		c.Kind, c.Pattern, c.Limit = "decimal", decimalPattern, 256

		c.Hint = "Enter an exact decimal value."
		c.Monetary = slices.Contains(
			[]string{"price", "gross", "proceeds", "net", "net_billable", "accrued_interest", "option.strike"},
			field,
		)
		c.Required = !slices.Contains(
			[]string{
				"gross",
				"proceeds",
				"net",
				"net_billable",
				"accrued_interest",
				"option.multiplier",
				"debt.coupon",
				"received_amount",
				"sent_amount",
			},
			field,
		)
		if field == "quantity" || field == "option.multiplier" || field == "rate" {
			c.Hint = "Enter a decimal greater than zero."
		}

		if field == "option.strike" || field == "debt.coupon" || field == "sent_amount" ||
			field == "received_amount" {
			c.Hint = "Enter a decimal of zero or more."
		}

		if !c.Monetary && f.Values[field].Currency != "" {
			c.Unit = strings.TrimSpace(f.Values[field].Currency + " " + c.Unit)
		}
	}

	if c.Kind == "choice" {
		c.Hint = "Choose the intended financial meaning."
	}

	if !c.Required && c.Kind != "link" && c.Kind != "timestamp" && c.Kind != "choice" {
		c.Hint += " Leave blank for Unknown."
	}

	return c
}

func correctionInputType(c correctionControl, value string) string {
	if c.Kind == "date" {
		if value != "" {
			if _, err := financial.ParseDate(value); err != nil {
				return "text"
			}
		}

		return "date"
	}

	return "text"
}

func correctionHasChoice(c correctionControl, value string) bool {
	return slices.ContainsFunc(c.Choices, func(choice correctionChoice) bool { return choice.Value == value })
}

func postedCorrection(r *http.Request, kind string) (application.Decision, error) {
	revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
	if err != nil {
		return application.Decision{}, application.ErrInvalid
	}

	return application.NewDecision(application.DecisionInput{
		TargetKind:       kind,
		TargetID:         r.PathValue("id"),
		Scope:            r.PostForm.Get("scope"),
		Text:             r.PostForm.Get("value"),
		Currency:         r.PostForm.Get("currency"),
		Action:           r.PostForm.Get("action"),
		Submission:       r.PostForm.Get("submission"),
		Rationale:        r.PostForm.Get("rationale"),
		ExpectedRevision: revision,
	})
}

const (
	correctionTimePattern = `[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?`
	correctionZonePattern = `Z|[+\-][0-9]{2}:[0-9]{2}`
)

func boolText(v bool) string { return strconv.FormatBool(v) }
func choiceSize(kind string) string {
	if kind == "link" {
		return "6"
	}

	return "1"
}
func limitText(limit int) string { return strconv.Itoa(limit) }
func correctionInputMode(kind string) string {
	if kind == "decimal" {
		return "decimal"
	}

	return "text"
}
