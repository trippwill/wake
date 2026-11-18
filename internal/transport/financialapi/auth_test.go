// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financialapi

import (
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
	}
}
