// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

type positionsReadStore struct {
	workspaceStore
	read func(application.ListQuery) (application.PositionsList, error)
}

func (s positionsReadStore) PositionsList(
	_ context.Context,
	q application.ListQuery,
) (application.PositionsList, error) {
	return s.read(q)
}

func positionsFixture(t *testing.T) application.PositionsList {
	t.Helper()

	ref, err := source.NewReference("synthetic", "account", "", "account")
	if err != nil {
		t.Fatal(err)
	}

	details, err := application.NewAccountDetails("Synthetic account", "USD")
	if err != nil {
		t.Fatal(err)
	}

	account, err := application.NewAccount("account", "source", "synthetic", ref, details, "fixture")
	if err != nil {
		t.Fatal(err)
	}

	return application.PositionsList{
		Account:  account,
		Version:  "positions-1",
		Status:   "available",
		AsOf:     &application.PositionTime{Date: "2026-11-04"},
		PageInfo: application.PageInfo{Total: 2, Matched: 2, Page: 1, PageSize: 50},
		Items: []application.PositionListItem{
			{
				Key:    "0",
				Name:   "Synthetic equity",
				Ticker: "SYN",
				Kind:   "equity",
				Position: application.PublishedPosition{
					InstrumentID: "equity",
					Side:         "long",
					Quantity: application.PublishedQuantity{
						Value: "9007199254740993.0000000000001",
						Unit:  "shares",
					},
					CostBasis: &application.PublishedMoney{Amount: "0.0000", Currency: "USD"},
				},
			},
			{
				Key:    "1",
				Name:   "Synthetic option",
				Ticker: "OPT",
				Kind:   "option",
				Position: application.PublishedPosition{
					InstrumentID: "option",
					Side:         "short",
					Quantity:     application.PublishedQuantity{Value: "-2.000000000001", Unit: "contracts"},
					Option: &application.PublishedOption{
						Right:         new("call"),
						Expiry:        new("2027-01-15"),
						Strike:        &application.PublishedMoney{Amount: "123.45000", Currency: "USD"},
						Multiplier:    new("100.0000"),
						EffectiveFrom: new("2026-11-01"),
					},
				},
			},
		},
	}
}

func TestPositionsGridFinancialMeaningAndNavigation(t *testing.T) {
	data := positionsFixture(t)
	got := positionsGrid(data)
	if got.Positions.AsOf != "2026-11-04" || got.Positions.State != "Available" ||
		got.Data[0].Quantity.Text != "9007199254740993.0000000000001 shares" ||
		got.Data[0].Cost.Text != "0.0000 USD" ||
		got.Data[1].Cost.Text != "Not provided" ||
		got.Data[1].Side.Text != "Short" {
		t.Fatal(got)
	}

	if got.Data[0].Name.Href != "/instruments/equity" ||
		got.Positions.SourceHref != "/connections/source?view=reports" {
		t.Fatal(got)
	}

	b, _ := json.Marshal(got.Data[1].Terms)
	for _, value := range []string{"123.45000 USD", "100.0000", "2026-11-01", "Put/call", "Not provided"} {
		if !strings.Contains(string(b), value) {
			t.Fatal(string(b), value)
		}
	}

	data.AsOf.At = new("2026-11-04T10:12:13.123400+01:00")
	if got := positionsGrid(data); got.Positions.AsOf != "2026-11-04 · 2026-11-04T10:12:13.123400+01:00" {
		t.Fatal(got.Positions)
	}

	debt := positionTerms(
		application.PublishedPosition{
			Debt: &application.PublishedDebt{
				Type:            new("treasury-bill"),
				Currency:        new("USD"),
				Maturity:        new("2027-01-01"),
				Coupon:          new("0.0000"),
				PaymentsPerYear: new(0),
			},
		},
		"debt",
	)

	b, _ = json.Marshal(debt)
	for _, value := range []string{"None (Treasury bill)", "0.0000", "Payments per year", "Not provided"} {
		if !strings.Contains(string(b), value) {
			t.Fatal(string(b), value)
		}
	}
}

func TestPositionsGridAvailability(t *testing.T) {
	data := positionsFixture(t)
	for _, reason := range []string{"not_reported", "incomplete", "conflict", "identity_unresolved"} {
		data.Status = "unavailable"
		data.Reason = new(reason)
		data.Items = nil
		data.Total = 0
		data.Matched = 0

		got := positionsGrid(data)
		if got.Positions.State != "Unavailable" || got.Positions.Message == "" || len(got.Data) != 0 ||
			got.Positions.SourceHref == "" {
			t.Fatal(got)
		}
	}

	data = positionsFixture(t)

	data.Items = nil
	data.Total = 0
	data.Matched = 0
	if got := positionsGrid(data); got.Positions.Message != "No open positions were reported." {
		t.Fatal(got)
	}

	data.Total = 2
	if got := positionsGrid(data); !strings.Contains(got.Positions.Message, "match these filters") {
		t.Fatal(got)
	}

	if got := positionsGrid(
		application.PositionsList{},
	); got.Positions.SourceLabel != "Set up an account" ||
		got.Positions.Account != "No account confirmed" {
		t.Fatal(got)
	}
}

func TestPositionsGridDistinguishesMissingAndInapplicableContractTerms(t *testing.T) {
	for _, kind := range []string{"equity", "fund", "option", "debt"} {
		data := positionsFixture(t)
		data.Items[1].Kind = kind
		data.Items[1].Position.Option = nil
		data.Items[1].Position.Debt = nil

		want := notApplicableValue
		if kind == "option" || kind == "debt" {
			want = "Not provided"
		}

		if got := positionsGrid(data).Data[1].Terms; got.Text != want || len(got.Details) != 0 {
			t.Fatal("missing applicable terms were labeled inapplicable", kind, got)
		}
	}
}

func TestPositionsAuthenticatedPageGridAndFailures(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-positions-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	session := httptest.NewRecorder()
	auth.issue(session)

	data := positionsFixture(t)
	for _, tc := range []struct {
		route         string
		authenticated bool
		failure       error
		status        int
	}{
		{"/positions", true, nil, 200},
		{"/grids/positions?q=SYN&state=short&kind=option", true, nil, 200},
		{"/positions", false, nil, 401},
		{"/grids/positions", false, nil, 401},
		{"/positions?sort=unsupported", true, nil, 400},
		{"/grids/positions?state=accepted", true, nil, 400},

		{"/grids/positions", true, errors.New("private diagnostic"), 503},
	} {
		t.Run(tc.route+http.StatusText(tc.status), func(t *testing.T) {
			store := positionsReadStore{read: func(q application.ListQuery) (application.PositionsList, error) {
				if q.Scope() != application.PositionsScope {
					t.Fatal(q.Scope())
				}

				return data, tc.failure
			}}

			handler, e := NewHandler(auth, Dependencies{Workflow: application.New(store, nil, nil, nil, nil)})
			if e != nil {
				t.Fatal(e)
			}

			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+tc.route, nil)
			req.Header.Set("HX-Request", "true")

			if tc.authenticated {
				req.AddCookie(session.Result().Cookies()[0])
			}

			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != tc.status || strings.Contains(res.Body.String(), "private diagnostic") {
				t.Fatal(res.Code, res.Body.String())
			}

			if tc.status == 200 && tc.route == "/positions" {
				for _, value := range []string{"data-grid=\"positions\"", "Reported as of", "/positions", "Search instruments"} {
					if !strings.Contains(res.Body.String(), value) {
						t.Fatal(value, res.Body.String())
					}
				}

				if strings.Contains(res.Body.String(), "hx-get=\"/positions") {
					t.Fatal("grid page would be replaced by htmx")
				}
			}
		})
	}
}
