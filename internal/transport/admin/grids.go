// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"go.trippwill.dev/wake/internal/application"
)

type gridCell struct {
	Text    string     `json:"text"`
	Href    string     `json:"href,omitempty"`
	Note    string     `json:"note,omitempty"`
	Tone    string     `json:"tone,omitempty"`
	Details []gridFact `json:"details,omitempty"`
}
type activityGridRow struct {
	ID        string   `json:"id"`
	Label     gridCell `json:"label"`
	Date      gridCell `json:"date"`
	Amount    gridCell `json:"amount"`
	Quantity  gridCell `json:"quantity"`
	Price     gridCell `json:"price"`
	Realized  gridCell `json:"realized"`
	State     gridCell `json:"state"`
	Reference gridCell `json:"reference"`
}
type instrumentGridRow struct {
	ID     string   `json:"id"`
	Name   gridCell `json:"name"`
	Kind   gridCell `json:"kind"`
	Ticker gridCell `json:"ticker"`
	State  gridCell `json:"state"`
}
type observationGridRow struct {
	ID       string   `json:"id"`
	Report   gridCell `json:"report"`
	Outcome  gridCell `json:"outcome"`
	Location gridCell `json:"location"`
	Rule     gridCell `json:"rule"`
	Activity gridCell `json:"activity"`
}
type gridPage[T any] struct {
	Data      []T               `json:"data"`
	LastPage  int64             `json:"last_page"`
	LastRow   int64             `json:"last_row"`
	Total     int64             `json:"total"`
	Positions *positionGridMeta `json:"positions,omitempty"`
}

func gridResult[T any](rows []T, info application.PageInfo) gridPage[T] {
	pages := (info.Matched + int64(max(1, info.PageSize)) - 1) / int64(max(1, info.PageSize))
	if pages < 1 {
		pages = 1
	}

	return gridPage[T]{Data: rows, LastPage: pages, LastRow: info.Matched, Total: info.Total}
}

func gridJSON(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		status := http.StatusServiceUnavailable

		message := "Data is temporarily unavailable. Retry this table."
		if errors.Is(err, application.ErrHistoryUpdating) {
			message = "Wake is updating retained financial history. Retry when the update shown on Overview is complete."
		}

		if errors.Is(err, application.ErrInvalid) {
			status = http.StatusBadRequest
			message = "Invalid table query."
		}

		if errors.Is(err, application.ErrNotFound) {
			status = http.StatusNotFound
			message = "This record is unavailable."
		}

		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Message string `json:"message"`
		}{message})

		return
	}

	_ = json.NewEncoder(w).Encode(data)
}

func activityGrid(data application.ActivityList) gridPage[activityGridRow] {
	view := activityList(data)
	rows := make([]activityGridRow, 0, len(view.Items))
	for _, item := range view.Items {
		reference := gridCell{Text: item.Reference}
		if item.InstrumentID != "" {
			reference.Href = "/instruments/" + item.InstrumentID
		} else {
			reference.Text = notApplicableValue
		}

		realized := gridCell{Text: item.Realized}
		if item.RealizedConflict {
			realized.Note = "Reported result needs review"
			realized.Tone = "review"
		}

		note := describe(item.Description, activityKindLabel(item.Kind))
		if item.AccountName != "" {
			note = describe(note, item.AccountName)
		}

		rows = append(
			rows,
			activityGridRow{
				ID: item.ID,
				Label: gridCell{
					Text: item.Label,
					Href: item.Href,
					Note: note,
				},
				Date:      gridCell{Text: item.Date},
				Quantity:  gridCell{Text: item.Quantity},
				Amount:    gridCell{Text: item.Amount},
				Price:     gridCell{Text: item.Price, Note: item.Basis},
				Realized:  realized,
				State:     gridCell{Text: item.State, Tone: activityBadgeKind(item.FinancialState)},
				Reference: reference,
			},
		)
	}

	return gridResult(rows, data.PageInfo)
}

func instrumentGrid(data application.InstrumentList) gridPage[instrumentGridRow] {
	view := instrumentList(data)
	rows := make([]instrumentGridRow, 0, len(view.Instruments))
	for _, item := range view.Instruments {
		rows = append(
			rows,
			instrumentGridRow{
				ID:     item.ID,
				Name:   gridCell{Text: known(item.Name)},
				Kind:   gridCell{Text: item.Kind},
				Ticker: gridCell{Text: instrumentTitle(item.Ticker, item.Name), Href: "/instruments/" + item.ID},
				State: gridCell{
					Text: referenceLabel(item.Status, item.Authority),
					Tone: referenceTone(item.Status),
				},
			},
		)
	}

	return gridResult(rows, data.PageInfo)
}

func observationGrid(data application.ObservationList) gridPage[observationGridRow] {
	rows := make([]observationGridRow, 0, len(data.Items))
	for _, row := range data.Items {
		activity := gridCell{Text: "Not linked"}
		if row.ActivityID != "" {
			activity = gridCell{Text: "Inspect related activity", Href: "/activity/" + row.ActivityID}
		}

		outcome := gridCell{Text: observationStatus(row.Outcome), Tone: observationBadgeKind(row.Outcome)}

		rows = append(
			rows,
			observationGridRow{
				ID: observationID(row),
				Report: gridCell{
					Text: row.Retrieved,
					Note: row.Artifact,
					Href: "/evidence/" + row.Artifact,
				},
				Outcome:  outcome,
				Location: gridCell{Text: row.Location},
				Rule:     gridCell{Text: row.Rule + " · " + row.Version},
				Activity: activity,
			},
		)
	}

	return gridResult(rows, data.PageInfo)
}

func registerGridRoutes(
	gate func(string, func(http.ResponseWriter, *http.Request, string)),
	workflow *application.Workflow,
) {
	for _, kind := range []string{"activity", "instruments", "observations", "positions"} {
		gate("GET /grids/"+kind, func(w http.ResponseWriter, r *http.Request, _ string) {
			r = pageRequest(r)

			scope := application.ActivityScope
			if kind == "instruments" {
				scope = application.InstrumentScope
			}

			if kind == "observations" {
				scope = application.ObservationScope
			}

			if kind == "positions" {
				scope = application.PositionsScope
			}

			query, err := listQuery(r.Context(), scope)
			if err != nil {
				gridJSON(w, nil, err)
				return
			}

			switch kind {
			case "positions":
				data, err := workflow.PositionsList(r.Context(), query)
				gridJSON(w, positionsGrid(data), err)
			case "activity":
				data, err := workflow.ActivityList(r.Context(), query)
				gridJSON(w, activityGrid(data), err)
			case "instruments":
				data, err := workflow.InstrumentList(r.Context(), query)
				gridJSON(w, instrumentGrid(data), err)
			case "observations":
				data, err := workflow.ObservationList(
					r.Context(),
					r.URL.Query().Get("subject"),
					r.URL.Query().Get("id"),
					query,
				)
				gridJSON(w, observationGrid(data), err)
			}
		})
	}
}
