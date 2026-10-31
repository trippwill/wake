// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

type (
	snapshotItem  struct{ Resource application.CanonicalResource }
	snapshotDelta struct {
		Items    []snapshotItem
		Snapshot application.FinancialSnapshot
	}
)

func snapshotState(ctx context.Context, s *Store) (application.FinancialSnapshot, error) {
	read, err := s.Snapshot(ctx, nil)
	return read.Snapshot, err
}

// snapshotChanges compares complete current states; intermediate revisions are not a delivery history.
func snapshotChanges(t *testing.T, s *Store, before application.FinancialSnapshot) snapshotDelta {
	t.Helper()

	after, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	prior := map[string]int64{}
	for _, r := range before.Resources {
		prior[r.Kind+"/"+r.ID] = r.Revision
	}

	out := snapshotDelta{Snapshot: after}
	for _, r := range after.Resources {
		if prior[r.Kind+"/"+r.ID] != r.Revision {
			out.Items = append(out.Items, snapshotItem{r})
		}
	}

	if (len(out.Items) == 0) != (before.Version == after.Version) {
		t.Fatal("publication version differs from accepted revisions")
	}

	return out
}

func snapshotResources(ctx context.Context, s *Store, kind string) (struct {
	Items []application.CanonicalResource
}, error,
) {
	var out struct {
		Items []application.CanonicalResource
	}

	snapshot, err := snapshotState(ctx, s)
	if err != nil {
		return out, err
	}

	for _, r := range snapshot.Resources {
		if r.Kind == kind {
			out.Items = append(out.Items, r)
		}
	}

	return out, nil
}

func explainCurrent(ctx context.Context, s *Store, kind, id string) (application.Explanation, error) {
	r, err := s.Resource(ctx, kind, id)
	if err != nil {
		return application.Explanation{}, err
	}

	return s.Explanation(ctx, kind, id, r.Revision)
}
