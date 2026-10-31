// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"go.trippwill.dev/wake/internal/application"
)

type gridCell struct {
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
	Note string `json:"note,omitempty"`
	Tone string `json:"tone,omitempty"`
}
type activityGridRow struct {
	ID        string   `json:"id"`
	Label     gridCell `json:"label"`
	Date      gridCell `json:"date"`
	Quantity  gridCell `json:"quantity"`
	Price     gridCell `json:"price"`
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
}
type gridPage[T any] struct {
	Data     []T   `json:"data"`
	LastPage int64 `json:"last_page"`
	LastRow  int64 `json:"last_row"`
	Total    int64 `json:"total"`
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

func activityGrid(ctx context.Context, data application.ActivityList) gridPage[activityGridRow] {
	view := activityList(data)
	rows := make([]activityGridRow, 0, len(view.Items))
	for _, item := range view.Items {
		reference := gridCell{Text: item.Reference}
		if item.InstrumentID != "" {
			reference.Href = navURL(ctx, "/instruments/"+item.InstrumentID)
		} else {
			reference.Text = notApplicableValue
		}

		rows = append(
			rows,
			activityGridRow{
				ID: item.ID,
				Label: gridCell{
					Text: item.Label,
					Href: navURL(ctx, item.Href),
					Note: activityKindLabel(item.Kind),
				},
				Date:      gridCell{Text: item.Date},
				Quantity:  gridCell{Text: item.Quantity},
				Price:     gridCell{Text: item.Price, Note: item.Basis},
				State:     gridCell{Text: item.State, Tone: activityBadgeKind(item.FinancialState)},
				Reference: reference,
			},
		)
	}

	return gridResult(rows, data.PageInfo)
}

func instrumentGrid(ctx context.Context, data application.InstrumentList) gridPage[instrumentGridRow] {
	view := instrumentList(data)
	rows := make([]instrumentGridRow, 0, len(view.Instruments))
	for _, item := range view.Instruments {
		rows = append(
			rows,
			instrumentGridRow{
				ID:     item.ID,
				Name:   gridCell{Text: item.Name, Href: navURL(ctx, "/instruments/"+item.ID)},
				Kind:   gridCell{Text: item.Kind},
				Ticker: gridCell{Text: item.Ticker},
				State: gridCell{
					Text: referenceLabel(item.Status, item.Authority),
					Tone: referenceTone(item.Status),
				},
			},
		)
	}

	return gridResult(rows, data.PageInfo)
}

func observationGrid(ctx context.Context, data application.ObservationList) gridPage[observationGridRow] {
	rows := make([]observationGridRow, 0, len(data.Items))
	for _, row := range data.Items {
		rows = append(
			rows,
			observationGridRow{
				ID: observationID(row),
				Report: gridCell{
					Text: row.Retrieved,
					Note: row.Artifact,
					Href: navURL(ctx, "/evidence/"+row.Artifact) + "&location=" + url.QueryEscape(row.Location),
				},
				Outcome:  gridCell{Text: observationStatus(row.Outcome), Tone: observationBadgeKind(row.Outcome)},
				Location: gridCell{Text: row.Location},
				Rule:     gridCell{Text: row.Rule + " · " + row.Version},
			},
		)
	}

	return gridResult(rows, data.PageInfo)
}

func registerGridRoutes(
	gate func(string, func(http.ResponseWriter, *http.Request, string)),
	workflow *application.Workflow,
) {
	for _, kind := range []string{"activity", "instruments", "observations"} {
		gate("GET /grids/"+kind, func(w http.ResponseWriter, r *http.Request, _ string) {
			var err error

			r, err = gridPageRequest(r, kind)
			if err != nil {
				gridJSON(w, nil, err)
				return
			}

			scope := application.ActivityScope
			if kind == "instruments" {
				scope = application.InstrumentScope
			}

			if kind == "observations" {
				scope = application.ObservationScope
			}

			query, err := listQuery(r.Context(), scope)
			if err != nil {
				gridJSON(w, nil, err)
				return
			}

			switch kind {
			case "activity":
				data, err := workflow.ActivityList(r.Context(), query)
				gridJSON(w, activityGrid(r.Context(), data), err)
			case "instruments":
				data, err := workflow.InstrumentList(r.Context(), query)
				gridJSON(w, instrumentGrid(r.Context(), data), err)
			case "observations":
				data, err := workflow.ObservationList(
					r.Context(),
					r.URL.Query().Get("subject"),
					r.URL.Query().Get("id"),
					query,
				)
				gridJSON(w, observationGrid(r.Context(), data), err)
			}
		})
	}
}

// Grid reads carry the owning page's navigation context, rather than treating an
// internal JSON URL as the page to return to after inspecting a linked record.
func gridPageRequest(r *http.Request, kind string) (*http.Request, error) {
	r = pageRequest(r)
	owner := r.URL.Query().Get("page_url")
	if owner == "" {
		path := "/activity"

		query := r.URL.Query()
		if kind == "instruments" {
			query.Set("view", "instruments")
		}

		if kind == "observations" {
			if query.Get("subject") == "activity" {
				path = "/activity/" + url.PathEscape(query.Get("id"))
			}

			if query.Get("subject") == "instrument" {
				path = "/instruments/" + url.PathEscape(query.Get("id"))
			}
		}

		owner = path + "?" + query.Encode()
	}

	owner = localDestination(owner)
	if owner == "" {
		return nil, application.ErrInvalid
	}

	state := page(r.Context())
	state.URL = owner

	return r.WithContext(context.WithValue(r.Context(), pageKey{}, state)), nil
}
