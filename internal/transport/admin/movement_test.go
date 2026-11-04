// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

type movementReadStore struct {
	workspaceStore
	resources map[string]application.CanonicalResource
	form      application.CorrectionForm
}

func (s movementReadStore) Resource(
	_ context.Context,
	kind, id string,
) (application.CanonicalResource, error) {
	r, ok := s.resources[kind+"/"+id]
	if !ok {
		return application.CanonicalResource{}, application.ErrNotFound
	}

	return r, nil
}

func (s movementReadStore) ActivityList(
	context.Context,
	application.ListQuery,
) (application.ActivityList, error) {
	return application.ActivityList{Items: []application.ActivityListItem{
		{Resource: s.resources["activity/cash"]},
		{Resource: s.resources["activity/transfer"], InstrumentName: "Synthetic security"},
	}}, nil
}

func (s movementReadStore) CorrectionForm(
	context.Context,
	string,
	string,
) (application.CorrectionForm, error) {
	return s.form, nil
}

func TestMovementHTTPDetailsGridAndCorrectionChoices(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-movement-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	session := httptest.NewRecorder()
	auth.issue(session)

	instrument, ticker, description := "security", "SYN", "Synthetic <refund>"
	resources := map[string]application.CanonicalResource{
		"activity/cash": {ID: "cash", Activity: &application.PublishedActivity{
			Family: "cash",
			Date:   "2026-10-20",
			State:  "accepted",
			Cash: &application.PublishedCash{
				Category:    "fee",
				Description: &description,
				Amount:      application.PublishedMoney{Amount: "0.0000000000001", Currency: "USD"},
			},
		}},
		"activity/transfer": {ID: "transfer", Activity: &application.PublishedActivity{
			Family:       "security-transfer",
			Date:         "2026-10-21",
			State:        "accepted",
			Conflict:     true,
			InstrumentID: &instrument,
			Quantity:     &application.PublishedQuantity{Value: "12.00000000001", Unit: "shares"},
			Transfer: &application.PublishedTransfer{
				Direction: "out",
				Price:     &application.PublishedMoney{Amount: "123.4500", Currency: "USD"},
			},
			Relationships: []application.PublishedRelationship{{Kind: "transfer"}},
		}},
		"instruments/security": {ID: "security", Instrument: &application.PublishedInstrument{Ticker: &ticker}},
	}

	store := movementReadStore{resources: resources}
	request := func(store movementReadStore, route string) *httptest.ResponseRecorder {
		t.Helper()

		h, e := NewHandler(auth, Dependencies{Workflow: application.New(store, nil, nil, nil, nil)})
		if e != nil {
			t.Fatal(e)
		}

		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+route, nil)
		r.AddCookie(session.Result().Cookies()[0])

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}

		return w
	}
	for _, tc := range []struct {
		route string
		want  []string
	}{
		{"/activity/cash", []string{"0.0000000000001 USD", "Synthetic &lt;refund&gt;", "Correct accepted data"}},
		{"/activity/transfer", []string{"Transfer out · SYN", "12.00000000001", "123.4500 USD", "Conflicting source claims", "Awaiting related evidence"}},
	} {
		body := request(store, tc.route).Body.String()
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", tc.route, want)
			}
		}

		if tc.route == "/activity/cash" && strings.Contains(body, "Inspect instrument") {
			t.Error("unlinked cash invented an instrument")
		}
	}

	name := "Synthetic security"
	resources["instruments/security"] = application.CanonicalResource{
		ID:         instrument,
		Instrument: &application.PublishedInstrument{Name: &name},
	}

	if body := request(
		store,
		"/activity/transfer",
	).Body.String(); !strings.Contains(
		body,
		"Transfer out · Synthetic security",
	) {
		t.Fatal("instrument name missing")
	}

	var grid gridPage[activityGridRow]
	if e := json.Unmarshal(request(store, "/grids/activity").Body.Bytes(), &grid); e != nil {
		t.Fatal(e)
	}

	if len(grid.Data) != 2 || grid.Data[0].Amount.Text != "0.0000000000001 USD" ||
		grid.Data[1].Quantity.Text != "12.00000000001 shares" ||
		grid.Data[1].Price.Text != "123.4500 USD" {
		t.Fatal(grid)
	}

	for _, tc := range []struct {
		field   string
		choices []string
	}{
		{"cash_category", []string{"payment-in-lieu", "adjustment", "other"}},
		{"direction", []string{"in", "out"}},
	} {
		store.form = application.CorrectionForm{
			Kind:     "activity",
			ID:       "cash",
			Revision: 1,
			Fields:   []string{tc.field},
		}
		body := request(store, "/activity/cash/correct?field="+tc.field).Body.String()
		for _, choice := range tc.choices {
			if !strings.Contains(body, `value="`+choice+`"`) {
				t.Errorf("%s missing choice %s", tc.field, choice)
			}
		}
	}
}
