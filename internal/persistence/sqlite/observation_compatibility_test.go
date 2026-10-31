// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

// Synthetic reports use a disposable account, never operator data or credentials.
func observationStore(t *testing.T) *Store {
	t.Helper()
	s, _ := testStore(t)

	_, err := s.db.Exec(`INSERT INTO connections(id,provider) VALUES('synthetic','ibkr-flex');
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('discovery','synthetic','confirmations','discover','ibkr-confirmation-v1','application/xml','2026-10-20T00:00:00Z','synthetic',X'00');
INSERT INTO accounts(id,singleton,name,currency,connection_id,provider,stream,authority,kind,scope,value,artifact_id) VALUES('account',1,'Synthetic account','','synthetic','ibkr-flex','confirmations','ibkr','account','','U1','discovery');`)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func observationReport(t *testing.T, s *Store, id, rows string) {
	t.Helper()

	ctx := context.Background()
	body := []byte(workflowReport(rows))
	artifact, _ := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", time.Now(), body)

	r, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = s.queries.PutArtifact(
		ctx,
		sqlc.PutArtifactParams{
			ID:           id,
			ConnectionID: "synthetic",
			Stream:       "confirmations",
			Operation:    "acquire",
			Format:       ibkrflex.ConfirmationFormat,
			MediaType:    "application/xml",
			Retrieved:    time.Now().UTC().Format(time.RFC3339Nano),
			Hash:         artifact.Hash(),
			Content:      body,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.Normalize(ctx, id, ibkrflex.Version, r.Normalizer()); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(ctx, id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}
}

func TestConflictsAcrossIncompleteObservations(t *testing.T) {
	base := equityRow("U1", "same", "100")
	absentNet := strings.Replace(base, ` netCash="-0.000000049324000000123"`, "", 1)
	for _, tc := range []struct {
		name string
		rows []string
	}{
		{"missing incoming native ID", []string{base, strings.Replace(strings.Replace(base, ` conid="100"`, "", 1), `US0000000001`, `US0000000002`, 1)}},
		{"missing baseline native ID", []string{strings.Replace(base, ` conid="100"`, "", 1), strings.Replace(base, `US0000000001`, `US0000000002`, 1)}},
		{"changed order", []string{base, strings.Replace(base, `orderID="O1"`, `orderID="O2"`, 1)}},
		{"late net cash", []string{absentNet, strings.Replace(absentNet, ` proceeds=`, ` netCash="-100" proceeds=`, 1), strings.Replace(absentNet, ` proceeds=`, ` netCash="-200" proceeds=`, 1)}},
		{"late component", []string{base, strings.Replace(base, ` commission=`, ` brokerExecutionCommission="-1" commission=`, 1), strings.Replace(base, ` commission=`, ` brokerExecutionCommission="-2" commission=`, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := observationStore(t)
			for i, row := range tc.rows {
				observationReport(t, s, fmt.Sprint("observation-", i), row)

				v, err := s.Dashboard(t.Context())
				if err != nil || len(v.Executions) != 1 {
					t.Fatal(v, err)
				}

				wantReview := i == len(tc.rows)-1
				if got := v.Executions[0].State().NeedsReview(); got != wantReview {
					t.Fatalf("observation %d: review=%v want %v", i, got, wantReview)
				}
			}
			// The original accepted values and every dissenting report remain retained.
			v, _ := s.Dashboard(t.Context())
			detail, err := s.ExecutionDetail(t.Context(), v.Executions[0].ID())
			if err != nil || len(detail.Observations) != len(tc.rows) {
				t.Fatal(detail, err)
			}
		})
	}
}
