// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financialapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFinancialHostCheckPrecedesBearerAdmission(t *testing.T) {
	for _, tc := range []struct {
		allowed []string
		host    string
		want    int
	}{
		{[]string{"[0:0:0:0:0:0:0:1]:080"}, "[0:0:0:0:0:0:0:1]:080", http.StatusUnauthorized},
		{nil, "other.example:18788", http.StatusUnauthorized},
		{[]string{"localhost:18788"}, "LOCALHOST:18788", http.StatusUnauthorized},
		{[]string{"localhost:18788"}, "localhost:8788", http.StatusBadRequest},
		{[]string{"localhost:18788"}, "other.example:18788", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		Authenticated(nil, nil, tc.allowed...).ServeHTTP(w, r)

		if w.Code != tc.want {
			t.Errorf("allowed %v host %q: got %d, want %d", tc.allowed, tc.host, w.Code, tc.want)
		}

		if tc.want == http.StatusBadRequest {
			var response Error
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != InvalidRequest ||
				w.Header().Get("Content-Type") != "application/json" {
				t.Errorf("unexpected error response: %s %s", w.Header(), w.Body)
			}
		}
	}
}
