// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"testing"

	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestProcessingRetryPreservesSourceCorrections(t *testing.T) {
	ctx := t.Context()
	s := coverageStore(t)
	coverageReport(t, s, "original", equityRow("U1", "original", "100"))
	coverageReport(t, s, "replacement", correctionRow("replacement", "Co", "Toriginal"))

	var err error
	if err = s.RetryProcessing(ctx, "original"); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(ctx, "original", ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 2 || view.Executions[1].Status() != "superseded" {
		t.Fatal("retry duplicated or erased correction", view, err)
	}

	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM execution_observations WHERE artifact_id='original'`).
		Scan(&count); err != nil ||
		count != 2 {
		t.Fatal("retry duplicated provenance", count, err)
	}

	if err = s.RetryProcessing(ctx, "discovery"); err == nil {
		t.Fatal("discovery processing retry accepted")
	}
}
