// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"strconv"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

type positionGridMeta struct {
	Account     string `json:"account"`
	AsOf        string `json:"as_of"`
	State       string `json:"state"`
	Message     string `json:"message"`
	SourceHref  string `json:"source_href"`
	SourceLabel string `json:"source_label"`
	Version     string `json:"version"`
}

type positionGridRow struct {
	ID       string   `json:"id"`
	Name     gridCell `json:"name"`
	Side     gridCell `json:"side"`
	Quantity gridCell `json:"quantity"`
	UnitCost gridCell `json:"cost"`
	Terms    gridCell `json:"terms"`
}

type gridFact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func positionsGrid(data application.PositionsList) gridPage[positionGridRow] {
	meta := &positionGridMeta{
		Account:     data.Account.Details().Name(),
		State:       "Unavailable",
		AsOf:        "Not provided",
		Version:     data.Version,
		SourceHref:  "/connections",
		SourceLabel: "Inspect source reports",
	}
	if data.Account.ID() == "" {
		meta.Account = "No account confirmed"
		meta.Message = "Confirm an account before inspecting reported positions."
		meta.SourceLabel = "Set up an account"
	} else {
		meta.SourceHref = "/connections/" + data.Account.Connection() + "?view=reports"
		switch text(data.Reason) {
		case "not_reported":
			meta.Message = "No usable position set has been reported. Check the source setup and retained reports."
		case "incomplete":
			meta.Message = "The latest applicable evidence does not establish a complete position set."
		case "conflict":
			meta.Message = "Conflicting reports prevent an available position set."
		case "identity_unresolved":
			meta.Message = "Instrument identities need review before positions can be shown. Inspect Instruments and Needs review."
		}
	}

	if data.AsOf != nil {
		meta.AsOf = data.AsOf.Date
		if data.AsOf.At != nil {
			meta.AsOf += " · " + *data.AsOf.At
		}

		if data.AsOf.Basis == source.PositionSourceFetched {
			meta.AsOf += " · SnapTrade fetch time"
		}
	}

	if data.Status == "available" {
		meta.State = "Available"
		if data.Total == 0 {
			meta.Message = "No open positions were reported."
		} else if data.Matched == 0 {
			meta.Message = "No positions match these filters. Reset the filters to inspect the reported set."
		}
	}

	rows := make([]positionGridRow, 0, len(data.Items))
	for _, item := range data.Items {
		p := item.Position
		unitCost := gridCell{Text: "Not provided"}
		if p.UnitCostBasis != nil {
			unitCost.Text = publishedMoney(&p.UnitCostBasis.PublishedMoney)
			unitCost.Note = quotationLabel(text(p.UnitCostBasis.QuotationBasis))
		}

		note := describe(instrumentDescription(item.Ticker, item.Name), activityKindLabel(item.Kind))

		rows = append(
			rows,
			positionGridRow{
				ID: item.Key,
				Name: gridCell{
					Text: instrumentTitle(item.Ticker, item.Name),
					Href: "/instruments/" + p.InstrumentID,
					Note: note,
				},
				Side:     gridCell{Text: stringTitle(p.Side)},
				Quantity: gridCell{Text: p.Quantity.Value + " " + p.Quantity.Unit},
				UnitCost: unitCost,
				Terms:    positionTerms(p),
			},
		)
	}

	out := gridResult(rows, data.PageInfo)
	out.Positions = meta

	return out
}

func positionTerms(p application.PublishedPosition) gridCell {
	kind := p.InstrumentTerms.Family
	out := gridCell{Text: notApplicableValue}
	switch kind {
	case "option", "debt":
		out.Text = "Not provided"
	case "unclassified":
		out.Text = "Unclassified instrument"
	}

	add := func(label, value string) {
		out.Details = append(out.Details, gridFact{Label: label, Value: provided(value)})
	}
	if o := p.InstrumentTerms.Option; o != nil {
		right := "Option"
		if o.Right != nil {
			right = stringTitle(*o.Right)
		}

		out.Text = right + " · " + provided(text(o.Expiry))
		add("Put/call", text(o.Right))
		add("Expiry", text(o.Expiry))

		strike := ""
		if o.Strike != nil {
			strike = publishedMoney(o.Strike)
		}

		add("Strike", strike)
		add("Premium multiplier", text(o.Multiplier))
		add("Effective from", text(o.EffectiveFrom))

		if o.Deliverables == nil {
			add("Deliverables", "")
		} else if len(o.Deliverables) == 0 {
			add("Deliverables", "None reported in the complete set")
		}

		for _, d := range o.Deliverables {
			if d.Security != nil {
				add(
					"Deliverable",
					provided(
						text(d.Quantity),
					)+" units of "+d.Security.Authority+" / "+d.Security.Kind+" / "+provided(
						d.Security.Scope,
					)+" / "+d.Security.Value,
				)
			} else if d.Cash != nil {
				add("Cash deliverable", publishedMoney(d.Cash))
			}
		}
	}

	if d := p.InstrumentTerms.Debt; d != nil {
		out.Text = strings.ReplaceAll(provided(text(d.Type)), "-", " ") + " · " + provided(text(d.Maturity))
		add("Issue type", strings.ReplaceAll(text(d.Type), "-", " "))
		add("Issue currency", text(d.Currency))
		add("Maturity", text(d.Maturity))
		add("Original issue date", text(d.IssueDate))

		if text(d.Type) == "treasury-bill" {
			add("Coupon payments", "None (Treasury bill)")
		}

		if text(d.Type) != "treasury-bill" || d.CouponRate != nil {
			add("Coupon rate", text(d.CouponRate))
		}

		if text(d.Type) != "treasury-bill" || d.DatedDate != nil {
			add("Dated date", text(d.DatedDate))
		}

		if text(d.Type) != "treasury-bill" || d.PaymentsPerYear != nil {
			payments := ""
			if d.PaymentsPerYear != nil {
				payments = strconv.Itoa(*d.PaymentsPerYear)
			}

			add("Payments per year", payments)
		}
	}

	return out
}
