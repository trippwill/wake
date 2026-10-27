// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func (s *Store) ReferenceKey(ctx context.Context) ([]byte, string, error) {
	r, err := s.queries.ReferenceSetting(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}

	return r.Encrypted, r.Failure, err
}

func (s *Store) SetReferenceKey(ctx context.Context, payload []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)
	if err = q.SetReferenceKey(ctx, nonNil(payload)); err != nil {
		return err
	}

	if err = q.WakeReference(ctx); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ReferenceFailure(ctx context.Context, failure string) error {
	return s.queries.ReferenceFailure(ctx, failure)
}

func (s *Store) ReferenceEvidence(ctx context.Context, id string) (application.ReferenceEvidence, error) {
	r, err := s.queries.ReferenceEvidence(ctx, id)
	if err != nil {
		return application.ReferenceEvidence{}, err
	}

	return application.ReferenceEvidence{
		Retrieved: r.Retrieved,
		Request:   string(r.Request),
		Response:  string(r.Response),
	}, nil
}

func (s *Store) ReserveReference(ctx context.Context, now time.Time, limit int) (bool, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	r, err := q.ReferenceBudget(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, err
	}

	start, err := parseOptionalTime(r.WindowStart)
	if err != nil {
		return false, time.Time{}, err
	}

	cooldown, err := parseOptionalTime(r.Cooldown)
	if err != nil {
		return false, time.Time{}, err
	}

	if cooldown.After(now) {
		return false, cooldown, nil
	}

	if start.IsZero() || !now.Before(start.Add(time.Minute)) {
		start = now
		r.Used = 0
	}

	if r.Used >= int64(limit) {
		return false, start.Add(time.Minute), nil
	}

	if err = q.SetReferenceBudget(
		ctx,
		sqlc.SetReferenceBudgetParams{
			WindowStart: start.UTC().Format(time.RFC3339Nano),
			Used:        r.Used + 1,
			Cooldown:    r.Cooldown,
		},
	); err != nil {
		return false, time.Time{}, err
	}

	if err = tx.Commit(); err != nil {
		return false, time.Time{}, err
	}

	return true, start.Add(time.Minute), nil
}

func (s *Store) ReferenceCooldown(ctx context.Context, until time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	r, err := q.ReferenceBudget(ctx)
	if err != nil {
		return err
	}

	old, err := parseOptionalTime(r.Cooldown)
	if err != nil {
		return err
	}

	if until.Before(old) {
		return nil
	}

	if err = q.SetReferenceBudget(
		ctx,
		sqlc.SetReferenceBudgetParams{
			WindowStart: r.WindowStart,
			Used:        r.Used,
			Cooldown:    until.UTC().Format(time.RFC3339Nano),
		},
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ReferenceAllowance(ctx context.Context, used int, reset time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	r, err := q.ReferenceBudget(ctx)
	if err != nil {
		return err
	}

	start, err := parseOptionalTime(r.WindowStart)
	if err != nil {
		return err
	}

	reported := reset.Add(-time.Minute)
	if reported.After(start) {
		start = reported
	}

	if err = q.SetReferenceBudget(
		ctx,
		sqlc.SetReferenceBudgetParams{
			WindowStart: start.UTC().Format(time.RFC3339Nano),
			Used:        max(r.Used, int64(used)),
			Cooldown:    r.Cooldown,
		},
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ReferenceCached(
	ctx context.Context,
	key string,
	now time.Time,
) (reference.Result, bool, error) {
	r, err := s.queries.CachedReference(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return reference.Result{}, false, nil
	}

	if err != nil {
		return reference.Result{}, false, err
	}

	expires, err := parseOptionalTime(r.Expires)
	if err != nil {
		return reference.Result{}, false, err
	}

	if !expires.After(now) {
		return reference.Result{}, false, nil
	}

	result := reference.Result{Status: r.Status, Evidence: r.EvidenceID, Next: expires}
	if json.Unmarshal(r.Records, &result.Records) != nil {
		return reference.Result{}, false, ErrCorrupt
	}

	return result, true, nil
}

func (s *Store) SaveReference(
	ctx context.Context,
	evidence reference.Evidence,
	jobs []reference.Job,
	results []reference.Result,
) ([]reference.Result, error) {
	if len(jobs) != len(results) {
		return nil, ErrCorrupt
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)
	id := rand.Text()

	headers, err := json.Marshal(evidence.Headers)
	if err != nil {
		return nil, err
	}

	if err = q.PutReferenceEvidence(
		ctx,
		sqlc.PutReferenceEvidenceParams{
			ID:         id,
			Retrieved:  evidence.At.UTC().Format(time.RFC3339Nano),
			Request:    evidence.Request,
			Response:   nonNil(evidence.Response),
			HttpStatus: int64(evidence.Status),
			Headers:    headers,
		},
	); err != nil {
		return nil, err
	}

	for i := range results {
		results[i].Evidence = id
		switch results[i].Status {
		case "resolved", "ambiguous", "no-match":
			records, e := json.Marshal(results[i].Records)
			if e != nil {
				return nil, e
			}

			if e = q.CacheReference(
				ctx,
				sqlc.CacheReferenceParams{
					Key:        jobs[i].Key(),
					Status:     results[i].Status,
					Records:    records,
					Expires:    results[i].Next.UTC().Format(time.RFC3339Nano),
					EvidenceID: id,
				},
			); e != nil {
				return nil, e
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return results, nil
}

func (s *Store) PendingReference(ctx context.Context, now time.Time) ([]reference.Pending, error) {
	rows, err := s.queries.PendingReference(ctx, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}

	out := []reference.Pending{}
	for _, r := range rows {
		i, e := instrumentValue(r)
		if e != nil {
			return nil, e
		}

		out = append(out, reference.Pending{ID: r.ID, Instrument: i.Reported()})
	}

	return out, nil
}

func (s *Store) ApplyReference(ctx context.Context, id string, result reference.Result) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	status := result.Status
	name, figi, evidence := root.Name, root.ShareClassFigi, root.EvidenceID
	ticker := root.Ticker
	if root.ReferenceStatus == "conflict" {
		status = "conflict"
	}

	if result.Evidence != "" {
		if err = q.PutInstrumentReferenceAttempt(
			ctx,
			sqlc.PutInstrumentReferenceAttemptParams{
				InstrumentID: id,
				EvidenceID:   result.Evidence,
				Status:       status,
				Rule:         reference.Rule,
			},
		); err != nil {
			return err
		}
	}

	if status == "resolved" {
		if len(result.Records) != 1 || result.Records[0].ShareClass == "" {
			return ErrCorrupt
		}

		record := result.Records[0]
		if figi != "" && figi != record.ShareClass {
			status = "conflict"
		} else {
			other, e := q.InstrumentByFIGI(ctx, record.ShareClass)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}

			if e == nil && other.ID != id {
				status = "conflict"

				if e = q.ResolveInstrument(
					ctx,
					sqlc.ResolveInstrumentParams{
						ID:              other.ID,
						Name:            other.Name,
						Ticker:          other.Ticker,
						ShareClassFigi:  other.ShareClassFigi,
						EvidenceID:      other.EvidenceID,
						ReferenceStatus: "conflict",
						ReferenceNext:   other.ReferenceNext,
					},
				); e != nil {
					return e
				}
			} else {
				name, figi, evidence = record.Name, record.ShareClass, result.Evidence
				if record.Ticker != "" {
					ticker = record.Ticker
				}
			}
		}
	}

	if err = q.ResolveInstrument(
		ctx,
		sqlc.ResolveInstrumentParams{
			ID:              id,
			Name:            name,
			Ticker:          ticker,
			ShareClassFigi:  figi,
			EvidenceID:      evidence,
			ReferenceStatus: status,
			ReferenceNext:   result.Next.UTC().Format(time.RFC3339Nano),
		},
	); err != nil {
		return err
	}

	if status == "resolved" {
		listings, e := q.Listings(ctx, id)
		if e != nil {
			return e
		}
		// A root lookup never chooses a listing from several candidates.
		var exactFIGI bool

		value, e := instrumentValue(root)
		if e != nil {
			return e
		}

		for _, id := range value.Reported().Identifiers() {
			if (id.Kind() == source.VenueFIGI || id.Kind() == source.UnclassifiedFIGI) &&
				id.Value() == result.Records[0].FIGI {
				exactFIGI = true
			}
		}

		if len(listings) == 1 && len(result.Records) == 1 && exactFIGI {
			if e = q.SetListingFIGI(
				ctx,
				sqlc.SetListingFIGIParams{ID: listings[0].ID, Figi: result.Records[0].FIGI},
			); e != nil {
				return e
			}
		}
	}

	return tx.Commit()
}
