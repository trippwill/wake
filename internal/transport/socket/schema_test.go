// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package socket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"go.trippwill.dev/wake/internal/application"
)

func TestPublishedSchemaAcceptsUnknownObjectsWithoutWeakeningKnownObjects(t *testing.T) {
	loader := openapi3.NewLoader()

	document, err := loader.LoadFromFile("../../../api/openapi/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if err = document.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}

	count := 0
	for name, definition := range document.Components.Schemas {
		for field, property := range definition.Value.Properties {
			schema := property.Value
			if schema.Nullable && (schema.Type == nil || schema.Type.Is("object")) {
				count++
				// OpenAPI 3.0 nullable widens a locally declared type, not a nonnullable allOf member.
				if schema.Type == nil || !schema.Type.Is("object") || len(schema.AllOf) != 0 {
					t.Errorf("%s.%s does not declare a genuinely nullable object", name, field)
				}

				if err = schema.VisitJSON(nil); err != nil {
					t.Errorf("%s.%s rejects unknown: %v", name, field, err)
				}
			}
		}
	}

	if count < 10 {
		t.Fatal("nullable object coverage unexpectedly absent", count)
	}

	resources := []application.CanonicalResource{
		{ID: "account", Kind: "accounts", Revision: 1, Account: &application.PublishedAccount{Name: "Synthetic"}},
		{
			ID:       "instrument",
			Kind:     "instruments",
			Revision: 1,
			Instrument: &application.PublishedInstrument{
				Family:          "equity",
				ReferenceStatus: "provisional",
				Identifiers:     []application.PublishedIdentifier{},
			},
		},
		{
			ID:       "conversion",
			Kind:     "activity",
			Revision: 1,
			Activity: &application.PublishedActivity{
				Family:        "conversion",
				AccountID:     "account",
				State:         "accepted",
				Date:          "2026-10-20",
				Sent:          &application.PublishedAmount{Currency: "EUR"},
				Received:      &application.PublishedAmount{Currency: "USD", Amount: new("0.0000")},
				Charges:       []application.PublishedCharge{},
				Relationships: []application.PublishedRelationship{},
			},
		},
	}
	for _, resource := range resources {
		response := httptest.NewRecorder()
		NewHandler(
			reads{item: resource},
			nil,
		).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/"+resource.Kind+"/"+resource.ID, nil))

		var value any
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &value) != nil {
			t.Fatal(response.Code, response.Body.String())
		}

		if err = document.Components.Schemas["Resource"].Value.VisitJSON(value); err != nil {
			t.Errorf("actual %s response fails contract: %v", resource.Kind, err)
		}
	}

	money := document.Components.Schemas["Money"].Value
	if money.VisitJSON(nil) == nil || money.VisitJSON(map[string]any{"amount": "1.2300"}) == nil {
		t.Error("known money constraints weakened")
	}
}
