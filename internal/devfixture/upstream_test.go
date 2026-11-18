// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devfixture

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamMappingRespondsToEachJob(t *testing.T) {
	t.Parallel()

	handler := Upstream(func() []byte { return nil }, nil)
	for _, test := range []struct {
		name, path, request, want string
		status                    int
	}{
		{
			"two jobs", "/mapping", `[{"idValue":"one"},{"idValue":"two"}]`,
			`[{"warning":"No identifier found."},{"warning":"No identifier found."}]` + "\n",
			http.StatusOK,
		},
		{"empty jobs", "/mapping", "[]", "[]\n", http.StatusOK},
		{"malformed jobs", "/mapping", "{", "invalid\n", http.StatusBadRequest},
		{"unknown path", "/other", "[]", "404 page not found\n", http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.request))
			resp := httptest.NewRecorder()

			handler.ServeHTTP(resp, req)

			if resp.Code != test.status || resp.Body.String() != test.want {
				t.Fatalf("response = %d %q, want %d %q", resp.Code, resp.Body.String(), test.status, test.want)
			}
		})
	}
}
