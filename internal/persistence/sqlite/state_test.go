// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestSchedulingUsesFailureCodeInsteadOfWording(t *testing.T) {
	s := coverageStore(t)
	ctx := context.Background()
	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}
	if err := s.Schedule(ctx, key, true); err != nil {
		t.Fatal(err)
	}

	if err := s.Fail(ctx, key, host.FailureTemporary, provider.ErrUnauthorized.Error()); err != nil {
		t.Fatal(err)
	}

	work, err := s.PendingWork(ctx, time.Now())
	if err != nil || len(work) != 1 {
		t.Fatal("display wording stopped a retryable failure", work, err)
	}

	if err = s.Fail(ctx, key, host.FailureUnauthorized, "wording that contains no classification"); err != nil {
		t.Fatal(err)
	}

	work, err = s.PendingWork(ctx, time.Now())
	if err != nil || len(work) != 0 {
		t.Fatal("unauthorized work was retried", work, err)
	}
}

func TestProjectionKeepsLifecycleReviewAndDecisionIndependent(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "original", equityRow("U1", "original", "10"))

	v, err := s.Dashboard(context.Background())
	if err != nil || len(v.Executions) != 1 {
		t.Fatal(v, err)
	}

	id := v.Executions[0].ID()
	retainDecision(t, s, "activity", id, "quantity", "12", "", "set")
	coverageReport(t, s, "conflict", equityRow("U1", "original", "11"))
	coverageReport(
		t,
		s,
		"cancel",
		`<TradeConfirm accountId="U1" assetCategory="STK" transactionType="ExchTrade" levelOfDetail="EXECUTION" origTradeID="Toriginal" code="Ca"/>`,
	)

	d, err := s.ExecutionDetail(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	state := d.Execution.State()
	if state.Lifecycle() != application.Cancelled || !state.NeedsReview() ||
		state.Authority() != application.OperatorSelection ||
		d.Execution.Terms().Fields().Quantity.Value() != "12" {
		t.Fatal("independent facts lost", state, d.Execution.Terms())
	}
}
