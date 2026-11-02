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
	policy, ok := application.CorrectionFieldPolicy(field)
	c := correctionControl{
		Kind:     "text",
		Pattern:  ".*",
		Label:    fieldLabel(field),
		Required: true,
		Limit:    2048,
		Hint:     "Enter the corrected value.",
		Unit:     f.Values[field].Unit,
	}
	if !ok {
		return c
	}

	c.Required = !policy.Optional()
	c.Limit = policy.Limit()
	switch policy.ValueKind() {
	case application.LinkCorrection:
		c.Kind, c.Hint = "link", "Search by name, symbol or reference. Choose a retained record from the list."
		for _, link := range f.Links {
			if link.Scope == field {
				c.Choices = append(c.Choices, correctionChoice{link.Value, link.Label})
			}
		}
	case application.ChoiceCorrection:
		c.Kind, c.Hint = "choice", "Choose the intended financial meaning."
		switch field {
		case "side":
			c.Choices = []correctionChoice{{"buy", "Buy"}, {"sell", "Sell"}}
		case "kind":
			c.Choices = []correctionChoice{
				{"exercise", "Exercise"},
				{"assignment", "Assignment"},
				{"expiration", "Expiration"},
			}
		case "option.right":
			c.Choices = []correctionChoice{{"put", "Put"}, {"call", "Call"}}
		case "position_effect":
			c.Choices = []correctionChoice{{"opening", "Opening"}, {"closing", "Closing"}, {"mixed", "Mixed"}}
		}
	case application.DateCorrection:
		c.Kind, c.Hint = "date", "Choose a valid calendar date."
		if field == "debt.maturity" {
			c.Hint = "Choose a maturity date consistent with the issue and dated dates."
		}
	case application.TimestampCorrection:
		c.Kind, c.Hint = "timestamp", "Keep the reported time, fractional seconds and UTC offset. Leave all three parts blank for Unknown."
	case application.TextCorrection:
		if field == "name" {
			c.Hint = "Enter a readable instrument name."
		} else {
			c.Hint = "Enter the reported symbol, or leave blank for Unknown."
		}
	case application.DecimalCorrection, application.MoneyCorrection:
		c.Kind, c.Pattern = "decimal", decimalPattern
		c.Monetary = policy.ValueKind() == application.MoneyCorrection
		switch policy.DecimalBound() {
		case application.PositiveDecimal:
			c.Hint = "Enter a decimal greater than zero."
		case application.NonnegativeDecimal:
			c.Hint = "Enter a decimal of zero or more."
		default:
			c.Hint = "Enter an exact decimal value."
		}

		if !c.Monetary && f.Values[field].Currency != "" {
			c.Unit = strings.TrimSpace(f.Values[field].Currency + " " + c.Unit)
		}
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
