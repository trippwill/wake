// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devfixture

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func Upstream(report func() []byte, realizedReport []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SendRequest":
			if r.URL.Query().Get("q") == "456" {
				_, _ = fmt.Fprint(
					w,
					`<FlexStatementResponse><Status>Success</Status><ReferenceCode>api-result-fixture</ReferenceCode></FlexStatementResponse>`,
				)

				return
			}

			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>api-fixture</ReferenceCode></FlexStatementResponse>`,
			)
		case "/GetStatement":
			if r.URL.Query().Get("q") == "api-result-fixture" {
				_, _ = w.Write(realizedReport)
				return
			}

			_, _ = w.Write(report())
		case "/mapping":
			var jobs []any
			if json.NewDecoder(r.Body).Decode(&jobs) != nil {
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}

			response := make([]map[string]string, len(jobs))
			for i := range response {
				response[i] = map[string]string{"warning": "No identifier found."}
			}

			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	})
}
