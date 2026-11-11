// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package persistence

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

const activityIngestionBatch = 200

func measureActivityIngestion(
	t *testing.T, s *Store,
	run func(...chromedp.Action), measure func(string, string, chromedp.Action) float64,
	report func(string, []float64), samples, original int,
	ingesting, overlapped *atomic.Bool,
) {
	t.Helper()

	if _, err := s.db.ExecContext(t.Context(), `
UPDATE published_resources SET snapshot=CAST(json_set(CAST(snapshot AS TEXT),'$.activity.date','2026-12-31') AS BLOB)
WHERE kind='activity' AND id LIKE 'kind-%' AND CAST(substr(id,-8) AS INTEGER)%100=0;
UPDATE published_resources SET snapshot=CAST(json_set(CAST(snapshot AS TEXT),'$.activity.price.amount',
 printf('%d.%s', 20+CAST(substr(id,-8) AS INTEGER)%140, '000000000000000') || substr(id,-8)) AS BLOB)
WHERE kind='activity' AND id LIKE 'kind-trade-%';
DELETE FROM publishing_dirty;`); err != nil {
		t.Fatal(err)
	}

	sort := os.Getenv("WAKE_ACTIVITY_PERFORMANCE_SELECTION")
	if sort == "" {
		sort = "date"
	}

	var completed atomic.Int64

	stop := make(chan struct{})
	done := make(chan error, 1)
	worker, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Minute)
	go func() { done <- ingestActivityReports(worker, s, stop, ingesting, &completed) }()

	defer func() {
		defer cancel()

		close(stop)

		if err := <-done; err != nil {
			t.Error(err)
		}

		if completed.Load() == 0 {
			t.Error("no ingestion batch committed")
		}

		t.Logf("activity-browser ingested %d additional trades", completed.Load()*activityIngestionBatch)
	}()

	for _, search := range []string{"2026-", "2026-12-31"} {
		const searchButton = `[data-list-filters] button[type="submit"]`

		run(chromedp.SetValue("#activity-search", search, chromedp.ByQuery))
		measure("click", searchButton, chromedp.Click(searchButton, chromedp.ByQuery))

		durations := make([][]float64, len(activityKindSizes)+1)
		overlaps := make([]int, len(durations))
		for trial := range samples + 1 {
			matchedAll := 0
			for i, family := range activityKindSizes {
				matched := family.size
				if search == "2026-12-31" {
					matched = 1 + (family.size-2)/100
				}

				matchedAll += matched

				elapsed, overlap := measureIngestingKind(
					t,
					run,
					measure,
					overlapped,
					family.kind,
					search,
					sort,
					matched,
					original,
				)
				if trial > 0 {
					durations[i] = append(durations[i], elapsed)
					if overlap {
						overlaps[i]++
					}
				}
			}

			elapsed, overlap := measureIngestingKind(
				t,
				run,
				measure,
				overlapped,
				"",
				search,
				sort,
				matchedAll,
				original,
			)
			if trial > 0 {
				durations[len(activityKindSizes)] = append(durations[len(activityKindSizes)], elapsed)
				if overlap {
					overlaps[len(activityKindSizes)]++
				}
			}
		}

		for i, times := range durations {
			name := "all"
			if i < len(activityKindSizes) {
				name = activityKindSizes[i].kind
			}

			path := fmt.Sprintf("ingest-%s-%s-%s", sort, search, name)
			t.Logf("activity-browser path=%s ingestion_overlap=%d/%d", path, overlaps[i], samples)

			if overlaps[i]*10 < samples*9 {
				t.Errorf("%s overlapped too few ingestion requests", path)
			}

			report(path, times)
		}
	}
}

func measureIngestingKind(
	t *testing.T, run func(...chromedp.Action), measure func(string, string, chromedp.Action) float64,
	overlapped *atomic.Bool, kind, search, sort string, matched, original int,
) (float64, bool) {
	t.Helper()

	overlapped.Store(false)

	elapsed := measure("change", "#activity-kind", selectActivityKind(kind))
	overlap := overlapped.Load()

	var rendered []string
	run(chromedp.Evaluate(`(() => {
 const query = new URL(location.href).searchParams;
 return [query.get('kind') || '', query.get('q'), query.get('sort'), query.get('dir'), query.get('page'),
  document.querySelector('[data-grid-status]').textContent];
})()`, &rendered))

	var shown, total int

	if len(rendered) != 6 || rendered[0] != kind || rendered[1] != search || rendered[2] != sort ||
		rendered[3] != "desc" || rendered[4] != "1" {
		t.Fatal("unexpected grid state", rendered)
	}

	if _, err := fmt.Sscanf(rendered[5], "%d of %d results", &shown, &total); err != nil ||
		shown != matched || total < original || (total-original)%activityIngestionBatch != 0 {
		t.Fatalf(
			"%s %s %s: grid state %v, expected %d matches and a complete report total",
			kind,
			search,
			sort,
			rendered,
			matched,
		)
	}

	return elapsed, overlap
}

func ingestActivityReports(
	ctx context.Context, s *Store, stop <-chan struct{}, ingesting *atomic.Bool, completed *atomic.Int64,
) error {
	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		return err
	}

	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}
	for batch := 0; ; batch++ {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var rows strings.Builder
		for row := range activityIngestionBatch {
			rows.WriteString(equityRow("U1", fmt.Sprintf("load-%d-%d", batch, row), "98.76543210987654321"))
		}

		// Use a different instrument and dates outside both submitted searches.
		body := strings.NewReplacer(
			"2026-",
			"2025-",
			`conid="100"`,
			`conid="991"`,
			`symbol="SYN"`,
			`symbol="LOAD"`,
			` isin="US0000000001"`,
			"",
			` orderID="O1"`,
			"",
		).Replace(workflowReport(rows.String()))
		now := time.Now()

		artifact, err := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", now, []byte(body))
		if err != nil {
			return err
		}

		step, err := provider.NewStep(provider.State{}, artifact, now.Add(time.Minute))
		if err != nil {
			return err
		}

		ingesting.Store(true)

		checkpoint, err := s.Checkpoint(ctx, key)
		if err != nil {
			return err
		}

		id, err := s.Accept(ctx, key, checkpoint.Revision, step, nil, provider.Progress{}, host.PollCycle{})
		if err != nil {
			return err
		}

		if _, err = s.Normalize(ctx, id, ibkrflex.Version, registration.Normalizer()); err == nil {
			err = s.Process(ctx, id, ibkrflex.Version)
		}

		ingesting.Store(false)

		if err != nil {
			return err
		}

		completed.Add(1)
	}
}
