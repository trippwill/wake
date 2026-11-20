// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package reference

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/reference/figi"
)

type evidenceStore struct {
	Store
	evidence Evidence
}

func (s *evidenceStore) ReserveReference(context.Context, time.Time, int) (bool, time.Time, error) {
	return true, time.Time{}, nil
}

func (s *evidenceStore) SaveReference(
	_ context.Context,
	evidence Evidence,
	_ []figi.Job,
	results []Result,
) ([]Result, error) {
	s.evidence = evidence

	return results, nil
}

type responseTransport func(*http.Request) (*http.Response, error)

func (send responseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return send(request)
}

func TestFetchScreensCredentialBeforeSavingEvidence(t *testing.T) {
	const (
		key       = "synthetic-key"
		validBody = `[{"data":[{"name":"Synthetic instrument"}]}]`
	)

	for _, tc := range []struct {
		name, key, body string
		omitted         bool
		status          string
	}{
		{"literal string", key, `[{"data":[{"name":"synthetic-key"}]}]`, true, "temporary-failure"},
		{"decoded string in result", key, `[{"data":[{"name":"synthetic\u002dkey"}]}]`, true, "temporary-failure"},
		{"padded key", " " + key + "\t", `[{"data":[{"name":"synthetic\u002dkey"}]}]`, true, "temporary-failure"},
		{"decoded key in unknown field", key, `[{"extra":{"synthetic\u002dkey":1},"data":[{"name":"Synthetic instrument"}]}]`, true, "temporary-failure"},
		{"duplicate key", key, `[{"extra":"synthetic\u002dkey","extra":"ordinary"}]`, true, "temporary-failure"},
		{"keyed malformed response", key, "unstructured response", true, "temporary-failure"},
		{"anonymous malformed response", "", "unstructured response", false, "temporary-failure"},
		{"clean keyed result", key, validBody, false, "resolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &evidenceStore{}
			client := &http.Client{
				Transport: responseTransport(func(request *http.Request) (*http.Response, error) {
					if request.Header.Get("X-OPENFIGI-APIKEY") != strings.Trim(tc.key, " \t") {
						t.Fatal("unexpected outbound credential header")
					}

					header := http.Header{}
					header.Set("Ratelimit-Limit", "25")
					header.Set("Retry-After", "response metadata "+key)

					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     header,
						Body:       io.NopCloser(strings.NewReader(tc.body)),
					}, nil
				}),
			}

			service, err := New(store, nil, client, "https://example.invalid", nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			jobs := []figi.Job{{IDType: "ID_ISIN", IDValue: "synthetic-instrument"}}

			results, err := service.fetch(t.Context(), jobs, time.Now(), 25, []byte(tc.key))
			if err != nil || len(results) != 1 || results[0].Status != tc.status {
				t.Fatal("unexpected reference result", err)
			}

			if tc.omitted {
				if string(store.evidence.Response) != `{"error":"response omitted during credential screening"}` ||
					len(results[0].Records) != 0 {
					t.Fatal("credential screening admitted response material")
				}
			} else if string(store.evidence.Response) != tc.body {
				t.Fatal("clean evidence bytes changed")
			}

			if store.evidence.Headers.Get("Ratelimit-Limit") != "25" ||
				(tc.key != "") != (store.evidence.Headers.Get("Retry-After") == "") {
				t.Fatal("unexpected retained headers")
			}
		})
	}
}
