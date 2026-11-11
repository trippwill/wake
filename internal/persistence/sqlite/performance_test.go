// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/transport/socket"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

// TestPerformanceWarehouse is opt-in because it creates a sizeable disposable warehouse.
func TestPerformanceWarehouse(t *testing.T) {
	size := os.Getenv("WAKE_PERFORMANCE_SIZE")
	if size == "" {
		t.Skip("set WAKE_PERFORMANCE_SIZE to quick, representative or large")
	}

	var count, repeats, positions int
	switch size {
	case "quick":
		count, repeats, positions = 1000, 5, 100
	case "representative":
		count, repeats, positions = 20000, 5, 1000
	case "large":
		count, repeats, positions = 100000, 10, 5000
	default:
		t.Fatal("unknown performance size", size)
	}

	ctx := t.Context()
	s := coverageStore(t)

	var rows strings.Builder
	for i := range count {
		rows.WriteString(equityRow("U1", strconv.Itoa(i), fmt.Sprintf("9007199254740993.000000000000%d", i%2+1)))
	}

	body := workflowReport(rows.String())

	reg, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	measure := func(name string, f func()) {
		t.Helper()

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		start := time.Now()

		f()

		elapsed := time.Since(start)

		runtime.ReadMemStats(&after)
		t.Logf(
			"performance size=%s phase=%s elapsed=%s allocated_bytes=%d allocations=%d",
			size,
			name,
			elapsed,
			after.TotalAlloc-before.TotalAlloc,
			after.Mallocs-before.Mallocs,
		)
	}
	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}

	var first string
	for i := range repeats {
		// Distinct evidence bodies with the same financial claims exercise retained appearances.
		artifact, e := provider.NewArtifact(
			ibkrflex.ConfirmationFormat,
			"application/xml",
			time.Date(2026, 10, 20, 0, 0, i, 0, time.UTC),
			[]byte(body+strings.Repeat(" ", i)),
		)
		if e != nil {
			t.Fatal(e)
		}

		var id string
		measure(fmt.Sprintf("acquire-%d", i), func() {
			cp, e := s.Checkpoint(ctx, key)
			if e != nil {
				t.Fatal(e)
			}

			step, e := provider.NewStep(provider.State{}, artifact, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
			if e != nil {
				t.Fatal(e)
			}

			id, e = s.Accept(ctx, key, cp.Revision, step, nil, provider.Progress{}, host.PollCycle{})
			if e != nil {
				t.Fatal(e)
			}
		})

		if i == 0 {
			first = id
		}

		measure(fmt.Sprintf("normalize-%d", i), func() {
			if _, e := s.Normalize(ctx, id, ibkrflex.Version, reg.Normalizer()); e != nil {
				t.Fatal(e)
			}
		})
		measure(fmt.Sprintf("process-%d", i), func() {
			if e := s.Process(ctx, id, ibkrflex.Version); e != nil {
				t.Fatal(e)
			}
		})
	}

	measure("repeat-acquisition", func() {
		artifact, e := provider.NewArtifact(
			ibkrflex.ConfirmationFormat,
			"application/xml",
			time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			[]byte(body),
		)
		if e != nil {
			t.Fatal(e)
		}

		cp, e := s.Checkpoint(ctx, key)
		if e != nil {
			t.Fatal(e)
		}

		step, e := provider.NewStep(provider.State{}, artifact, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
		if e != nil {
			t.Fatal(e)
		}

		id, e := s.Accept(ctx, key, cp.Revision, step, nil, provider.Progress{}, host.PollCycle{})
		if e != nil || id != first {
			t.Fatal("repeat acquisition did not reuse evidence", id, e)
		}
	})
	measure("idempotent-replay", func() {
		if _, e := s.Normalize(ctx, first, ibkrflex.Version, reg.Normalizer()); e != nil {
			t.Fatal(e)
		}

		if e := s.Process(ctx, first, ibkrflex.Version); e != nil {
			t.Fatal(e)
		}
	})
	measure("enrichment-and-conflict", func() {
		acquireRealized(t, s, realizedReport("0", "10.0000000000001", "-90.0000"))
		acquireRealized(t, s, realizedReport("0", "11.0000000000001", "-89.0000"))
	})

	var snap application.SnapshotRead
	measure("snapshot", func() {
		var e error

		snap, e = s.Snapshot(ctx, application.SnapshotCondition{})
		if e != nil {
			t.Fatal(e)
		}
	})

	var activity string

	conflict := false
	for _, r := range snap.Snapshot.Resources {
		if r.Activity != nil && r.Activity.ReportedRealizedPnL != nil && r.Activity.ReportedRealizedPnL.Conflict {
			conflict = true
			break
		}
	}

	if !conflict {
		t.Fatal("missing retained enrichment conflict")
	}

	for _, r := range snap.Snapshot.Resources {
		if r.Kind == "activity" {
			if r.Activity == nil || r.Activity.Price == nil ||
				!strings.HasPrefix(r.Activity.Price.Amount, "9007199254740993.000000000000") {
				t.Fatal("snapshot lost exact price", r)
			}

			activity = r.ID

			break
		}
	}

	if activity == "" {
		t.Fatal("missing activity")
	}

	measure("unchanged-snapshot", func() {
		r, e := s.Snapshot(ctx, application.MatchingSnapshotVersions(snap.Snapshot.Version))
		if e != nil || !r.Unchanged {
			t.Fatal(r, e)
		}
	})

	consumer := os.Getenv("WAKE_PERFORMANCE_CONSUMER")
	if consumer == "" {
		t.Fatal("WAKE_PERFORMANCE_CONSUMER must name the independent consumer binary")
	}

	dir := t.TempDir()
	socketPath, mirrorPath := filepath.Join(dir, "wake.sock"), filepath.Join(dir, "mirror.db")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{Handler: socket.NewHandler(s, nil, nil), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()

	t.Cleanup(func() { _ = server.Close() })

	syncConsumer := func(wantVersion string) {
		t.Helper()
		// The task builds this disposable test consumer before invoking the fixture.
		//nolint:gosec // The executable is an explicit local test input.
		output, e := exec.CommandContext(ctx, consumer, "-socket", socketPath, "-state", mirrorPath).Output()
		if e != nil {
			t.Fatal("consumer failed", e)
		}

		var result struct {
			Status      string `json:"status"`
			Resources   int    `json:"resources"`
			NewBaseline bool   `json:"new_baseline"`
			Version     string `json:"version"`
		}
		if e = json.Unmarshal(
			output,
			&result,
		); e != nil || result.Status != "synchronized" || result.Resources != len(snap.Snapshot.Resources) || result.NewBaseline ||
			result.Version != wantVersion {
			t.Fatal("consumer result", string(output), e)
		}
	}
	measure("consumer-baseline", func() { syncConsumer(snap.Snapshot.Version) })
	measure("consumer-unchanged", func() { syncConsumer(snap.Snapshot.Version) })
	retainDecision(t, s, "activity", activity, "price", "12.000000000000001", "USD", "set")

	changedVersion := ""
	measure("changed-snapshot", func() {
		r, e := s.Snapshot(ctx, application.MatchingSnapshotVersions(snap.Snapshot.Version))
		if e != nil || r.Unchanged {
			t.Fatal(r, e)
		}

		changedVersion = r.Snapshot.Version
		found := false
		for _, item := range r.Snapshot.Resources {
			if item.ID == activity && item.Activity != nil && item.Activity.Price != nil &&
				item.Activity.Price.Amount == "12.000000000000001" {
				found = true
			}
		}

		if !found {
			t.Fatal("decision missing from changed snapshot")
		}
	})
	measure("consumer-changed", func() { syncConsumer(changedVersion) })
	measure("retained-reinterpretation", func() {
		if _, e := s.Normalize(ctx, first, "performance-reinterpretation/1", reg.Normalizer()); e != nil {
			t.Fatal(e)
		}

		if e := s.Process(ctx, first, "performance-reinterpretation/1"); e != nil {
			t.Fatal(e)
		}
	})

	retained, err := s.Resource(ctx, "activity", activity)
	if err != nil || retained.Activity == nil || retained.Activity.Price == nil ||
		retained.Activity.Price.Amount != "12.000000000000001" {
		t.Fatal("reinterpretation lost active decision", err)
	}

	q, err := application.NewListQuery(
		application.ListOptions{Search: "9007199254740993.000000000000", Sort: "price", Direction: "asc"},
		application.ActivityScope,
	)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		measure(fmt.Sprintf("activity-filter-sort-%d", i), func() {
			r, e := s.ActivityList(ctx, q)
			if e != nil || r.Matched != int64(count-1) {
				t.Fatal(r.Matched, e)
			}

			for _, item := range r.Items {
				if item.Resource.Activity == nil || item.Resource.Activity.Price == nil ||
					item.Resource.Activity.Price.Amount != "9007199254740993.0000000000001" {
					t.Fatal("exact price order changed", item)
				}
			}
		})
	}

	var holdings strings.Builder
	for i := range positions {
		holdings.WriteString(
			strings.ReplaceAll(
				holdingXML("2026-11-01", "9007199254740993.0000000000001", "12.0000000000001"),
				`conid="100"`,
				fmt.Sprintf(`conid="%d"`, i+1000),
			),
		)
	}

	positionBody := reportedPositionsXML("2026-11-01", "<OpenPositions>"+holdings.String()+"</OpenPositions>")
	measure("positions-ingest", func() { acquireRealized(t, s, positionBody) })

	pq, err := application.NewListQuery(
		application.ListOptions{Sort: "quantity", Direction: "asc"},
		application.PositionsScope,
	)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		measure(fmt.Sprintf("positions-sort-%d", i), func() {
			r, e := s.PositionsList(ctx, pq)
			if e != nil || r.Total != int64(positions) {
				t.Fatal(r.Total, e)
			}
		})
	}

	measure("positions-read", func() {
		r, e := s.Positions(ctx, "account", application.SnapshotCondition{})
		if e != nil || len(r.View.Positions) != positions {
			t.Fatal(e)
		}
	})

	var (
		dbPath   string
		sequence int
		dbName   string
	)
	if err := s.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &dbName, &dbPath); err != nil {
		t.Fatal(err)
	}

	var storage int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, e := os.Stat(dbPath + suffix); e == nil {
			storage += info.Size()
		} else if !os.IsNotExist(e) {
			t.Fatal(e)
		}
	}

	t.Logf("performance size=%s warehouse_storage_bytes=%d", size, storage)
}

// TestPerformanceRead uses synthetic copies of real projected resources to isolate
// read scaling; it is not evidence about ingestion or history reconciliation.
func TestPerformanceRead(t *testing.T) {
	size := os.Getenv("WAKE_PERFORMANCE_READ")
	if size == "" {
		t.Skip("set WAKE_PERFORMANCE_READ")
	}

	count := 0
	switch size {
	case "representative":
		count = 20000
	case "large":
		count = 100000
	default:
		t.Fatal("unknown size", size)
	}

	t.Logf(
		"read-fixture specification_sha256=%x count=%d",
		sha256.Sum256(
			[]byte(
				fmt.Sprintf(
					"wake-read-v1:%d:alternating-9007199254740993.0000000000001:9007199254740993.0000000000002",
					count,
				),
			),
		),
		count,
	)

	samples := 5
	if size == "large" {
		samples = 1
	}

	ctx := t.Context()
	s := seedPerformanceRead(t, count)

	measure := func(name string, f func()) {
		t.Helper()

		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)

		start := time.Now()

		f()

		elapsed := time.Since(start)

		runtime.ReadMemStats(&b)
		t.Logf(
			"performance read=%s count=%d phase=%s elapsed=%s allocated_bytes=%d allocations=%d",
			size,
			count,
			name,
			elapsed,
			b.TotalAlloc-a.TotalAlloc,
			b.Mallocs-a.Mallocs,
		)
	}

	q, err := application.NewListQuery(
		application.ListOptions{Search: "9007199254740993.000000000000", Sort: "price", Direction: "asc"},
		application.ActivityScope,
	)
	if err != nil {
		t.Fatal(err)
	}

	for i := range samples {
		measure(fmt.Sprintf("activity-filter-sort-%d", i), func() {
			result, e := s.ActivityList(ctx, q)
			if e != nil || result.Matched != int64(count) {
				t.Fatal(result.Matched, e)
			}

			for _, item := range result.Items {
				if item.Resource.Activity.Price.Amount != "9007199254740993.0000000000001" {
					t.Fatal("incorrect order")
				}
			}
		})
	}

	var snap application.SnapshotRead
	measure("snapshot", func() {
		var e error

		snap, e = s.Snapshot(ctx, application.SnapshotCondition{})
		if e != nil || len(snap.Snapshot.Resources) != count+3 {
			t.Fatal(len(snap.Snapshot.Resources), e)
		}
	})

	consumer := os.Getenv("WAKE_PERFORMANCE_CONSUMER")
	if consumer == "" {
		t.Fatal("missing consumer")
	}

	dir := t.TempDir()
	socketPath, mirrorPath := filepath.Join(dir, "wake.sock"), filepath.Join(dir, "mirror.db")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{Handler: socket.NewHandler(s, nil, nil), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()

	t.Cleanup(func() { _ = server.Close() })

	syncConsumer := func(version string) {
		t.Helper()
		// The task builds this disposable test consumer before invoking the fixture.
		//nolint:gosec // The executable is an explicit local test input.
		output, e := exec.CommandContext(ctx, consumer, "-socket", socketPath, "-state", mirrorPath).Output()
		if e != nil {
			t.Fatal(e)
		}

		var result struct {
			Status    string `json:"status"`
			Resources int    `json:"resources"`
			Version   string `json:"version"`
		}
		if e = json.Unmarshal(
			output,
			&result,
		); e != nil || result.Status != "synchronized" || result.Resources != count+3 ||
			result.Version != version {
			t.Fatal(string(output), e)
		}
	}
	measure("consumer-baseline", func() { syncConsumer(snap.Snapshot.Version) })

	for i := range samples {
		// Publish one changed resource while all other resource bytes remain identical.
		if _, err = s.db.ExecContext(
			ctx,
			`UPDATE published_resources SET revision=revision+1,snapshot=CAST(json_set(CAST(snapshot AS TEXT),'$.revision',revision+1,'$.activity.price.amount',?) AS BLOB) WHERE kind='activity' AND id='performance-00000000'; UPDATE publishing SET version=version+1`,
			fmt.Sprintf("12.00000000000000%d", i),
		); err != nil {
			t.Fatal(err)
		}

		current, e := s.Snapshot(ctx, application.SnapshotCondition{})
		if e != nil {
			t.Fatal(e)
		}

		measure(fmt.Sprintf("consumer-changed-%d", i), func() { syncConsumer(current.Snapshot.Version) })
	}

	var (
		dbPath, dbName string
		sequence       int
	)
	if err = s.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &dbName, &dbPath); err != nil {
		t.Fatal(err)
	}

	for _, file := range []string{dbPath, mirrorPath} {
		var storage int64
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if info, e := os.Stat(file + suffix); e == nil {
				storage += info.Size()
			} else if !os.IsNotExist(e) {
				t.Fatal(e)
			}
		}

		t.Logf("performance read=%s file=%s storage_bytes=%d", size, filepath.Base(file), storage)
	}
}

func seedPerformanceRead(t *testing.T, count int) *Store {
	t.Helper()

	ctx := t.Context()
	s := coverageStore(t)
	coverageReport(
		t,
		s,
		"seed",
		equityRow(
			"U1",
			"even",
			"9007199254740993.0000000000001",
		)+equityRow(
			"U1",
			"odd",
			"9007199254740993.0000000000002",
		),
	)
	// The two templates share identity metadata but retain exact, distinct prices.
	// Direct SQL seeds the materialized read model only, outside all timings.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	const copies = `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i+1<?), templates AS (SELECT id, row_number() OVER (ORDER BY id)-1 AS slot FROM executions)
 INSERT INTO executions(revision,kind,id,account_id,instrument_id,listing_id,accepted,status,review_reasons,selection_authority,rule)
 SELECT e.revision,e.kind,printf('performance-%08d',n.i),e.account_id,e.instrument_id,e.listing_id,e.accepted,e.status,e.review_reasons,e.selection_authority,e.rule FROM n JOIN templates t ON t.slot=n.i%2 JOIN executions e ON e.id=t.id`
	if _, err = tx.ExecContext(ctx, copies, count-2); err != nil {
		t.Fatal(err)
	}

	const resources = `WITH templates AS (SELECT p.*,row_number() OVER (ORDER BY p.id)-1 AS slot FROM published_resources p WHERE kind='activity'), copies AS (SELECT id,CAST(substr(id,13) AS INTEGER) AS i FROM executions WHERE id LIKE 'performance-%')
 INSERT INTO published_resources(kind,id,revision,snapshot,explanation)
 SELECT 'activity',copies.id,t.revision,CAST(json_set(CAST(t.snapshot AS TEXT),'$.id',copies.id) AS BLOB),t.explanation FROM copies JOIN templates t ON t.slot=copies.i%2`
	if _, err = tx.ExecContext(ctx, resources); err != nil {
		t.Fatal(err)
	}

	if _, err = tx.ExecContext(
		ctx,
		`DELETE FROM publishing_dirty; UPDATE publishing SET version=version+1`,
	); err != nil {
		t.Fatal(err)
	}

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	return s
}
