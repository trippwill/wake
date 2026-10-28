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
		{
			ID:       "listing",
			Kind:     "listings",
			Revision: 1,
			Listing:  &application.PublishedListing{InstrumentID: "instrument", Currency: "USD"},
		},
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
	bodies := map[string]any{}
	bodyNames := map[string]string{
		"accounts":    "account",
		"instruments": "instrument",
		"listings":    "listing",
		"activity":    "activity",
	}
	for _, resource := range resources {
		data, e := json.Marshal(resource)
		if e != nil {
			t.Fatal(e)
		}

		var fields map[string]any
		if e = json.Unmarshal(data, &fields); e != nil {
			t.Fatal(e)
		}

		name := bodyNames[resource.Kind]
		bodies[name] = fields[name]
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

		schema := document.Components.Schemas["Resource"].Value
		if err = schema.VisitJSON(value); err != nil {
			t.Errorf("actual %s response fails contract: %v", resource.Kind, err)
		}

		bodyName := map[string]string{"accounts": "account", "instruments": "instrument", "listings": "listing", "activity": "activity"}[resource.Kind]
		fields := value.(map[string]any)
		body := fields[bodyName]
		delete(fields, bodyName)

		if schema.VisitJSON(fields) == nil {
			t.Errorf("%s missing body accepted", resource.Kind)
		}

		fields[bodyName] = nil
		if schema.VisitJSON(fields) == nil {
			t.Errorf("%s null body accepted", resource.Kind)
		}

		fields[bodyName] = body
		for otherKind, otherBody := range map[string]string{"accounts": "account", "instruments": "instrument", "listings": "listing", "activity": "activity"} {
			if otherKind == resource.Kind {
				continue
			}

			fields["kind"] = otherKind
			if schema.VisitJSON(fields) == nil {
				t.Errorf("%s body accepted as %s", resource.Kind, otherKind)
			}

			fields["kind"] = resource.Kind
			fields[otherBody] = bodies[otherBody]
			if schema.VisitJSON(fields) == nil {
				t.Errorf("%s accepted extra %s body", resource.Kind, otherBody)
			}

			delete(fields, otherBody)
		}
	}

	money := document.Components.Schemas["Money"].Value
	if money.VisitJSON(nil) == nil || money.VisitJSON(map[string]any{"amount": "1.2300"}) == nil {
		t.Error("known money constraints weakened")
	}
}
