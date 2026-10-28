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

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func (s *Store) Normalize(
	ctx context.Context,
	id, version string,
	n provider.Normalizer,
) (provider.Result, error) {
	old, err := s.queries.Succeeded(ctx, sqlc.SucceededParams{ArtifactID: id, Version: version})
	if err == nil {
		return decodeResult(old)
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return provider.Result{}, err
	}

	retained, err := s.Retained(ctx, id)
	if err != nil {
		return provider.Result{}, err
	}

	attempt := rand.Text()
	if err = s.queries.StartAttempt(
		ctx,
		sqlc.StartAttemptParams{ID: attempt, ArtifactID: id, Version: version},
	); err != nil {
		return provider.Result{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.failedAttempt(ctx, attempt, "storage-failed")
		return provider.Result{}, host.ErrStorage
	}

	defer func() { _ = tx.Rollback() }()

	sink := &claims{q: s.queries.WithTx(tx), artifact: id, version: version}

	result, normalizeErr := n.Normalize(ctx, retained.Artifact, sink)
	if normalizeErr == nil {
		normalizeErr = ctx.Err()
	}

	reported := result.Counts()
	reported.Rows -= reported.Ignored
	reported.Ignored = 0
	if normalizeErr == nil && (reported != sink.counts || result.Rows() > 100000) {
		normalizeErr = provider.ErrInvalid
	}

	if normalizeErr != nil || sink.failed != nil {
		_ = tx.Rollback()

		outcome := "normalization-failed"
		failure := error(provider.ErrMalformed)

		if errors.Is(normalizeErr, provider.ErrUnavailable) {
			failure = provider.ErrUnavailable
		}

		switch {
		case errors.Is(sink.failed, host.ErrStorage):
			outcome = "storage-failed"
			failure = host.ErrStorage
		case errors.Is(sink.failed, provider.ErrLimit):
			failure = provider.ErrLimit
		case sink.failed != nil:
			failure = provider.ErrInvalid
		}

		if ctx.Err() != nil {
			outcome = "canceled"
			failure = ctx.Err()
		}

		s.failedAttempt(ctx, attempt, outcome)

		return provider.Result{}, failure
	}

	data, err := json.Marshal(result.Counts())
	if err != nil {
		return provider.Result{}, err
	}

	if err = sink.q.FinishAttempt(
		ctx,
		sqlc.FinishAttemptParams{Outcome: "succeeded", Counts: data, ID: attempt},
	); err != nil {
		_ = tx.Rollback()

		s.failedAttempt(ctx, attempt, "storage-failed")

		return provider.Result{}, host.ErrStorage
	}

	if err = sink.q.SaveNormalizationReceipt(
		ctx,
		sqlc.SaveNormalizationReceiptParams{ArtifactID: id, Version: version, Counts: data},
	); err != nil {
		_ = tx.Rollback()

		s.failedAttempt(ctx, attempt, "storage-failed")

		return provider.Result{}, host.ErrStorage
	}

	if err = s.commit(ctx, tx); err != nil {
		s.failedAttempt(ctx, attempt, "storage-failed")
		return provider.Result{}, host.ErrStorage
	}

	return result, nil
}

func (s *Store) failedAttempt(ctx context.Context, id, outcome string) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_ = s.queries.FinishAttempt(detached, sqlc.FinishAttemptParams{Outcome: outcome, Counts: []byte{}, ID: id})
}

func decodeResult(data []byte) (provider.Result, error) {
	var counts provider.Counts
	if err := json.Unmarshal(data, &counts); err != nil {
		return provider.Result{}, ErrCorrupt
	}

	result, err := provider.NewResult(counts)
	if err != nil {
		return provider.Result{}, ErrCorrupt
	}

	return result, nil
}

type Attempt struct{ ID, Version, Outcome string }

func (s *Store) Attempts(ctx context.Context, id string) ([]Attempt, error) {
	rows, err := s.queries.Attempts(ctx, id)
	if err != nil {
		return nil, err
	}

	out := make([]Attempt, 0, len(rows))
	for _, r := range rows {
		out = append(out, Attempt{r.ID, r.Version, r.Outcome})
	}

	return out, nil
}

type claims struct {
	q                 *sqlc.Queries
	artifact, version string
	counts            provider.Counts
	failed            error
}

func (c *claims) put(ctx context.Context, location, kind string, data []byte) error {
	if c.failed != nil {
		return c.failed
	}

	if c.counts.Rows >= 100000 {
		c.failed = provider.ErrLimit
		return c.failed
	}

	if err := c.q.PutRecord(
		ctx,
		sqlc.PutRecordParams{
			ArtifactID: c.artifact,
			Version:    c.version,
			Location:   location,
			Kind:       kind,
			Data:       data,
		},
	); err != nil {
		c.failed = host.ErrStorage
		return c.failed
	}

	c.counts.Rows++

	return nil
}

func (c *claims) PutTrade(ctx context.Context, t source.Trade) error {
	if _, err := source.NewTrade(t.Fields()); err != nil {
		c.failed = provider.ErrInvalid
		return c.failed
	}

	data, err := encodeTrade(t)
	if err != nil {
		c.failed = err
		return err
	}

	if err = c.put(ctx, t.Fields().Location, "trade", data); err != nil {
		return err
	}

	c.counts.Trades++

	return nil
}

func (c *claims) PutInstrument(ctx context.Context, o source.InstrumentObservation) error {
	if o.Location() == "" {
		c.failed = provider.ErrInvalid
		return c.failed
	}

	wire := instrumentWire(o.Instrument())
	if !o.AsOf().IsZero() {
		wire.AsOf = o.AsOf().String()
	}

	data, err := json.Marshal(wire)
	if err != nil {
		c.failed = err
		return err
	}

	if err = c.put(ctx, o.Location(), "instrument", data); err != nil {
		return err
	}

	c.counts.Instruments++

	return nil
}

func (c *claims) Reject(ctx context.Context, location, reason string) error {
	if _, err := provider.NewProgress(reason, 0, 0); err != nil || location == "" || len(location) > 1024 {
		c.failed = provider.ErrInvalid
		return c.failed
	}

	data, err := json.Marshal(reason)
	if err != nil {
		c.failed = err
		return err
	}

	if err = c.put(ctx, location, "rejected", data); err != nil {
		return err
	}

	c.counts.Rejected++

	return nil
}

// Record retains one interpretation at an artifact/location/version. It is not a
// financial account, accepted instrument or accepted trade.
type Record struct {
	Location, Kind, Reason string
	Trade                  source.Trade
	Lifecycle              source.Lifecycle
	Conversion             source.Conversion
	Correction             source.Correction
	Instrument             source.InstrumentObservation
}

func (s *Store) Records(ctx context.Context, id, version string) ([]Record, error) {
	rows, err := s.queries.Records(ctx, sqlc.RecordsParams{ArtifactID: id, Version: version})
	if err != nil {
		return nil, err
	}

	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		record := Record{Location: row.Location, Kind: row.Kind}
		switch row.Kind {
		case "trade":
			record.Trade, err = decodeTrade(row.Data)
			if err == nil && record.Trade.Fields().Location != row.Location {
				err = ErrCorrupt
			}
		case "correction":
			record.Correction, err = decodeCorrection(row.Data)
			if err == nil && record.Correction.Fields().Location != row.Location {
				err = ErrCorrupt
			}
		case "conversion":
			record.Conversion, err = decodeConversion(row.Data)
			if err == nil && record.Conversion.Fields().Location != row.Location {
				err = ErrCorrupt
			}
		case "lifecycle":
			record.Lifecycle, err = decodeLifecycle(row.Data)
			if err == nil && record.Lifecycle.Fields().Location != row.Location {
				err = ErrCorrupt
			}
		case "instrument":
			var data instrumentData

			err = json.Unmarshal(row.Data, &data)
			if err == nil {
				var i source.Instrument

				i, err = data.instrument()
				if err == nil {
					date := sourceDate(data.AsOf)
					if data.AsOf != "" && date.IsZero() {
						err = ErrCorrupt
					} else {
						record.Instrument, err = source.NewInstrumentObservation(row.Location, i, date)
					}
				}
			}
		case "rejected":
			err = json.Unmarshal(row.Data, &record.Reason)
		default:
			err = ErrCorrupt
		}

		if err != nil {
			return nil, ErrCorrupt
		}

		out = append(out, record)
	}

	return out, nil
}

func (c *claims) PutLifecycle(ctx context.Context, l source.Lifecycle) error {
	if _, err := source.NewLifecycle(l.Fields()); err != nil {
		c.failed = provider.ErrInvalid
		return c.failed
	}

	data, err := encodeLifecycle(l)
	if err != nil {
		c.failed = err
		return err
	}

	if err = c.put(ctx, l.Fields().Location, "lifecycle", data); err != nil {
		return err
	}

	c.counts.Lifecycles++

	return nil
}

func (c *claims) PutConversion(ctx context.Context, v source.Conversion) error {
	if _, err := source.NewConversion(v.Fields()); err != nil {
		c.failed = provider.ErrInvalid
		return c.failed
	}

	data, err := encodeConversion(v)
	if err != nil {
		c.failed = err
		return err
	}

	if err = c.put(ctx, v.Fields().Location, "conversion", data); err != nil {
		return err
	}

	c.counts.Conversions++

	return nil
}

func (c *claims) PutCorrection(ctx context.Context, v source.Correction) error {
	if _, err := source.NewCorrection(v.Fields()); err != nil {
		c.failed = provider.ErrInvalid
		return c.failed
	}

	data, err := encodeCorrection(v)
	if err != nil {
		c.failed = err
		return err
	}

	if err = c.put(ctx, v.Fields().Location, "correction", data); err != nil {
		return err
	}

	c.counts.Corrections++

	return nil
}
