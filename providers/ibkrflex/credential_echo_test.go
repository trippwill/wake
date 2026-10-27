// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.trippwill.dev/wake/provider"
)

func TestRejectDecodedCredentialEchoes(t *testing.T) {
	const secret = "synthetic-token"

	var encoded strings.Builder
	for _, r := range secret {
		fmt.Fprintf(&encoded, "&#%d;", r)
	}

	report := func(attribute string) string {
		return `<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement ` + attribute + `><TradeConfirms/></FlexStatement></FlexStatements></FlexQueryResponse>`
	}
	for _, tc := range []struct {
		name, operation, body string
		rejected              bool
	}{
		{"escaped alias", "GetStatement", report(`acctAlias="prefix` + encoded.String() + `suffix"`), true},
		{"escaped description", "GetStatement", report(`description="` + encoded.String() + `"`), true},
		{"raw report", "GetStatement", report(`acctAlias="` + secret + `"`), true},
		{"escaped reference", "SendRequest", `<FlexStatementResponse><Status>Success</Status><ReferenceCode>prefix` + encoded.String() + `suffix</ReferenceCode></FlexStatementResponse>`, true},
		{"unrelated escaped report", "GetStatement", report(`acctAlias="Synthe&#116;ic"`), false},
		{"unrelated escaped reference", "SendRequest", `<FlexStatementResponse><Status>Success</Status><ReferenceCode>ref&#45;1</ReferenceCode></FlexStatementResponse>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, tc.body) }),
			)
			defer server.Close()

			s := Source{http: server.Client(), base: server.URL, limit: 1 << 20}

			result, err := s.request(t.Context(), tc.operation, []byte(secret), "123", nil)
			if tc.rejected {
				if !errors.Is(err, provider.ErrMalformed) || len(result.body) != 0 || result.reference != "" {
					t.Fatal("credential-bearing response admitted", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if tc.operation == "GetStatement" && string(result.body) != tc.body {
				t.Fatal("evidence bytes rewritten")
			}

			if tc.operation == "SendRequest" && result.reference != "ref-1" {
				t.Fatal("valid reference not decoded")
			}
		})
	}
}
