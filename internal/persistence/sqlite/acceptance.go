// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func referenceKey(ref source.Reference) string { b, _ := json.Marshal(refWire(ref)); return string(b) }

func executionValue(r sqlc.Execution) (application.Execution, error) {
	t, err := decodeTrade(r.Accepted)
	if err != nil {
		return application.Execution{}, ErrCorrupt
	}

	return application.NewExecution(r.ID, r.AccountID, r.InstrumentID, r.ListingID, r.Status, r.Rule, t)
}

func instrumentValue(r sqlc.Instrument) (application.Instrument, error) {
	var wire instrumentData
	if json.Unmarshal(r.Reported, &wire) != nil {
		return application.Instrument{}, ErrCorrupt
	}

	i, err := wire.instrument()
	if err != nil {
		return application.Instrument{}, ErrCorrupt
	}

	return application.NewInstrument(
		r.ID,
		r.Name,
		r.Ticker,
		r.Kind,
		r.ReferenceStatus,
		r.ShareClassFigi,
		r.EvidenceID,
		i, r.Rule,
	)
}

// Process accepts only the designated account/provider/stream. Accepted terms and
// every matching or dissenting observation commit with the artifact completion.
func (s *Store) Process(ctx context.Context, id, version string) error {
	records, err := s.Records(ctx, id, version)
	if err != nil {
		return err
	}

	retained, err := s.Retained(ctx, id)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	row, err := q.Account(ctx)
	if err != nil {
		return err
	}

	account, err := accountValue(row)
	if err != nil {
		return err
	}

	for _, record := range records {
		if record.Kind != "trade" {
			continue
		}

		count, e := q.ObservationExists(
			ctx,
			sqlc.ObservationExistsParams{ArtifactID: id, Version: version, Location: record.Location},
		)
		if e != nil {
			return e
		}

		if count != 0 {
			continue
		}

		observation := sqlc.PutObservationParams{
			ArtifactID: id,
			Version:    version,
			Location:   record.Location,
			Rule:       application.AuthorityRule,
			Outcome:    "unselected account or source",
		}
		t := record.Trade
		f := t.Fields()
		if account.Connection() == retained.Connection && account.Provider() == retained.Provider &&
			account.Stream() == retained.Stream &&
			account.Reference() == f.Account {
			observation.Rule = application.RepeatRule
			if f.Event.IsZero() {
				observation.Outcome = "identity needs review"
			} else {
				execution, ambiguous, lookupErr := matchExecution(ctx, q, account.ID(), t)
				if lookupErr != nil {
					return lookupErr
				}

				if ambiguous {
					observation.Outcome = "native identity needs review"
					if e = q.PutObservation(ctx, observation); e != nil {
						return e
					}

					continue
				}

				if execution != "" {
					old, e := q.Execution(ctx, execution)
					if e != nil {
						return e
					}

					accepted, e := decodeTrade(old.Accepted)
					if e != nil {
						return e
					}

					if accepted.Fields().Instrument.Reference() == f.Instrument.Reference() {
						if e = checkInstrumentRoot(ctx, q, old.InstrumentID, f.Instrument); e != nil {
							return e
						}
					}

					known, e := q.ExecutionKnownTrades(ctx, sql.NullString{String: execution, Valid: true})
					if e != nil {
						return e
					}

					compatible := application.Compatible(accepted, t)
					for _, data := range known {
						prior, decodeErr := decodeTrade(data)
						if decodeErr != nil {
							return decodeErr
						}

						compatible = application.Compatible(prior, t) && compatible
					}

					observation.Outcome = "corroborating observation"
					if !compatible {
						observation.Outcome = "conflicting terms; needs review"

						if e = q.ConflictExecution(ctx, execution); e != nil {
							return e
						}
					}
				} else {
					instrument, listing, e := acceptInstrument(
						ctx,
						q,
						id,
						record.Location,
						f.Instrument,
						f.Price.Currency().String(),
					)
					if e != nil {
						return e
					}

					data, e := encodeTrade(t)
					if e != nil {
						return e
					}

					execution = rand.Text()
					if e = q.PutExecution(
						ctx,
						sqlc.PutExecutionParams{
							ID:           execution,
							AccountID:    account.ID(),
							InstrumentID: instrument,
							ListingID:    listing,
							Accepted:     data,
							Rule:         application.RepeatRule,
						},
					); e != nil {
						return e
					}

					if e = q.PutEventLink(
						ctx,
						sqlc.PutEventLinkParams{
							AccountID:   account.ID(),
							Reference:   referenceKey(f.Event),
							ExecutionID: execution,
						},
					); e != nil {
						return e
					}

					observation.Outcome = "accepted execution"
				}

				for _, ref := range append([]source.Reference{f.Event}, f.EventAliases...) {
					if e = q.PutEventLink(
						ctx,
						sqlc.PutEventLinkParams{
							AccountID:   account.ID(),
							Reference:   referenceKey(ref),
							ExecutionID: execution,
						},
					); e != nil {
						return e
					}
				}

				observation.ExecutionID = sql.NullString{String: execution, Valid: true}
			}
		}

		if e = q.PutObservation(ctx, observation); e != nil {
			return e
		}
	}

	if err = q.ArtifactProcessed(ctx, id); err != nil {
		return err
	}

	return tx.Commit()
}

func checkInstrumentRoot(ctx context.Context, q *sqlc.Queries, id string, i source.Instrument) error {
	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	previous, err := instrumentValue(root)
	if err != nil {
		return err
	}

	if !instrumentContradiction(previous.Reported(), i) {
		return nil
	}

	return q.ResolveInstrument(
		ctx,
		sqlc.ResolveInstrumentParams{
			ID:              root.ID,
			Name:            root.Name,
			Ticker:          root.Ticker,
			ShareClassFigi:  root.ShareClassFigi,
			EvidenceID:      root.EvidenceID,
			ReferenceStatus: "conflict",
			ReferenceNext:   root.ReferenceNext,
		},
	)
}

func acceptInstrument(
	ctx context.Context,
	q *sqlc.Queries,
	artifact, location string,
	i source.Instrument,
	currency string,
) (string, string, error) {
	key := referenceKey(i.Reference())
	if i.Reference().IsZero() {
		b, _ := json.Marshal([]string{artifact, location})
		key = string(b)
	}

	root, err := q.InstrumentBySource(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		reported, e := json.Marshal(instrumentWire(i))
		if e != nil {
			return "", "", e
		}

		root = sqlc.Instrument{ID: rand.Text()}
		err = q.PutInstrumentRoot(
			ctx,
			sqlc.PutInstrumentRootParams{
				ID:        root.ID,
				SourceKey: key,
				Name:      i.Name(),
				Ticker:    i.Ticker(),
				Kind:      string(i.Kind()),
				Reported:  reported, Rule: application.InstrumentRule,
			},
		)
	}

	if err != nil {
		return "", "", err
	}

	if len(root.Reported) > 0 {
		previous, e := instrumentValue(root)
		if e != nil {
			return "", "", e
		}

		if instrumentContradiction(previous.Reported(), i) {
			if e = q.ResolveInstrument(
				ctx,
				sqlc.ResolveInstrumentParams{
					ID:              root.ID,
					Name:            root.Name,
					Ticker:          root.Ticker,
					ShareClassFigi:  root.ShareClassFigi,
					EvidenceID:      root.EvidenceID,
					ReferenceStatus: "conflict",
					ReferenceNext:   root.ReferenceNext,
				},
			); e != nil {
				return "", "", e
			}
		}
	}

	if err = q.PutListing(
		ctx,
		sqlc.PutListingParams{
			ID:           rand.Text(),
			InstrumentID: root.ID,
			Venue:        i.Venue(),
			Ticker:       i.Ticker(),
			Currency:     currency,
		},
	); err != nil {
		return "", "", err
	}

	listing, err := q.ListingByTerms(
		ctx,
		sqlc.ListingByTermsParams{
			InstrumentID: root.ID,
			Venue:        i.Venue(),
			Ticker:       i.Ticker(),
			Currency:     currency,
		},
	)
	if err != nil {
		return "", "", err
	}

	return root.ID, listing.ID, nil
}

func instrumentContradiction(a, b source.Instrument) bool {
	if a.Kind() != b.Kind() {
		return true
	}

	kinds := map[source.IdentifierKind]map[string]bool{}
	for _, id := range a.Identifiers() {
		if kinds[id.Kind()] == nil {
			kinds[id.Kind()] = map[string]bool{}
		}

		kinds[id.Kind()][id.Value()] = true
	}

	for kind, values := range kinds {
		reported := false
		match := false
		for _, id := range b.Identifiers() {
			if id.Kind() == kind {
				reported = true

				if values[id.Value()] {
					match = true
				}
			}
		}

		if reported && !match {
			return true
		}
	}

	return false
}

func (s *Store) ExecutionDetail(ctx context.Context, id string) (application.ExecutionDetail, error) {
	out := application.ExecutionDetail{}

	r, err := s.queries.Execution(ctx, id)
	if err != nil {
		return out, err
	}

	out.Execution, err = executionValue(r)
	if err != nil {
		return out, err
	}

	observations, err := s.queries.ExecutionObservations(ctx, sql.NullString{String: id, Valid: true})
	if err != nil {
		return out, err
	}

	for _, o := range observations {
		out.Observations = append(
			out.Observations,
			application.Observation{
				Artifact: o.ArtifactID,
				Version:  o.Version,
				Location: o.Location,
				Outcome:  o.Outcome,
				Rule:     o.Rule,
			},
		)
	}

	return out, nil
}

func (s *Store) InstrumentDetail(ctx context.Context, id string) (application.InstrumentDetail, error) {
	out := application.InstrumentDetail{}

	r, err := s.queries.Instrument(ctx, id)
	if err != nil {
		return out, err
	}

	out.Instrument, err = instrumentValue(r)
	if err != nil {
		return out, err
	}

	rows, err := s.queries.Listings(ctx, id)
	if err != nil {
		return out, err
	}

	for _, r := range rows {
		l, e := application.NewListing(r.ID, r.InstrumentID, r.Venue, r.Ticker, r.Currency, r.Figi)
		if e != nil {
			return out, e
		}

		out.Listings = append(out.Listings, l)
	}

	attempts, err := s.queries.InstrumentReferenceAttempts(ctx, id)
	if err != nil {
		return out, err
	}

	for _, a := range attempts {
		out.References = append(
			out.References,
			application.ReferenceAttempt{
				Evidence:  a.EvidenceID,
				Status:    a.Status,
				Retrieved: a.Retrieved,
				Rule:      a.Rule,
			},
		)
	}

	observations, err := s.queries.InstrumentObservations(ctx, id)
	if err != nil {
		return out, err
	}

	for _, o := range observations {
		out.Observations = append(
			out.Observations,
			application.Observation{
				Artifact: o.ArtifactID,
				Version:  o.Version,
				Location: o.Location,
				Outcome:  o.Outcome,
				Rule:     o.Rule,
			},
		)
	}

	return out, nil
}

// An execution ID distinguishes fills even when another native reference is shared.
// Aliases can corroborate an earlier observation with only a trade ID. Several
// possible executions stay ambiguous instead of selecting an arbitrary fill.
func matchExecution(
	ctx context.Context,
	q *sqlc.Queries,
	account string,
	t source.Trade,
) (string, bool, error) {
	f := t.Fields()
	matches := map[string]bool{}
	for index, ref := range append([]source.Reference{f.Event}, f.EventAliases...) {
		ids, err := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: account, Reference: referenceKey(ref)})
		if err != nil {
			return "", false, err
		}

		for _, id := range ids {
			if index > 0 && f.Event.Kind() == "execution-id" {
				links, err := q.ExecutionLinks(ctx, id)
				if err != nil {
					return "", false, err
				}

				different := false
				for _, link := range links {
					var wire refData
					if json.Unmarshal([]byte(link), &wire) != nil {
						return "", false, ErrCorrupt
					}

					r, err := wire.reference()
					if err != nil {
						return "", false, err
					}

					if r.Kind() == "execution-id" && r != f.Event {
						different = true
					}
				}

				if different {
					continue
				}
			}

			matches[id] = true
		}

		if index == 0 && len(matches) == 1 && f.Event.Kind() == "execution-id" {
			for id := range matches {
				return id, false, nil
			}
		}
	}

	if len(matches) > 1 {
		return "", true, nil
	}

	for id := range matches {
		return id, false, nil
	}

	return "", false, nil
}
