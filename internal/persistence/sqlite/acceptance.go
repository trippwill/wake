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

	state, err := activityState(r)
	if err != nil {
		return application.Execution{}, err
	}

	return application.NewExecution(
		r.ID,
		r.AccountID,
		r.InstrumentID.String,
		r.ListingID.String,
		r.Rule,
		state,
		t, r.Revision,
	)
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
		r.ReferenceFigi,
		r.EvidenceID,
		i, r.Rule, r.Revision, application.SelectionAuthority(r.SelectionAuthority),
	)
}

// Process accepts only the designated account/provider/stream. Accepted terms and
// every matching or dissenting observation commit with the artifact completion.
func (s *Store) Process(ctx context.Context, id, version string) error {
	complete, err := s.queries.ProcessingComplete(
		ctx,
		sqlc.ProcessingCompleteParams{ArtifactID: id, Version: version},
	)
	if err != nil {
		return err
	}

	if complete {
		return s.queries.ArtifactProcessed(ctx, id)
	}

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

	changed := false
	for _, record := range records {
		previous, knownErr := q.ClaimProcessing(
			ctx,
			sqlc.ClaimProcessingParams{ClaimID: record.ClaimID, Ordinal: record.Ordinal},
		)
		if knownErr == nil {
			if err = q.CopyClaimObservation(
				ctx,
				sqlc.CopyClaimObservationParams{
					ArtifactID:       id,
					Version:          version,
					Location:         record.Location,
					PreviousArtifact: previous.ArtifactID,
					PreviousVersion:  previous.Version,
					PreviousLocation: previous.Location,
				},
			); err != nil {
				return err
			}

			if err = q.CopyClaimCorrection(
				ctx,
				sqlc.CopyClaimCorrectionParams{
					ArtifactID:       id,
					Version:          version,
					Location:         record.Location,
					PreviousArtifact: previous.ArtifactID,
					PreviousVersion:  previous.Version,
					PreviousLocation: previous.Location,
				},
			); err != nil {
				return err
			}

			continue
		}

		if !errors.Is(knownErr, sql.ErrNoRows) {
			return knownErr
		}

		if err = q.PutClaimProcessing(
			ctx,
			sqlc.PutClaimProcessingParams{
				ClaimID:    record.ClaimID,
				Ordinal:    record.Ordinal,
				ArtifactID: id,
				Version:    version,
				Location:   record.Location,
			},
		); err != nil {
			return err
		}

		changed = changed || record.Kind != "rejected" && record.Kind != "instrument"

		if record.Kind == "correction" {
			f := record.Correction.Fields()
			if account.Connection() == retained.Connection && account.Provider() == retained.Provider &&
				account.Stream() == retained.Stream &&
				account.Reference() == f.Account {
				if err = q.PutSourceCorrection(
					ctx,
					sqlc.PutSourceCorrectionParams{
						ArtifactID:  id,
						Version:     version,
						Location:    f.Location,
						AccountID:   account.ID(),
						Kind:        string(f.Kind),
						Target:      referenceKey(f.Target),
						Replacement: referenceKey(f.Replacement),
					},
				); err != nil {
					return err
				}
			}

			continue
		}

		if record.Kind == "conversion" {
			if err = processConversion(
				ctx,
				q,
				account,
				retained.Connection,
				retained.Provider,
				retained.Stream,
				id,
				version,
				record.Conversion,
			); err != nil {
				return err
			}

			continue
		}

		if record.Kind == "lifecycle" {
			if err = processLifecycle(
				ctx,
				q,
				account,
				retained.Connection,
				retained.Provider,
				retained.Stream,
				id,
				version,
				record.Lifecycle,
			); err != nil {
				return err
			}

			continue
		}

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
			Outcome:    "unselected",
		}
		t := record.Trade
		f := t.Fields()
		if account.Connection() == retained.Connection && account.Provider() == retained.Provider &&
			account.Stream() == retained.Stream &&
			account.Reference() == f.Account {
			observation.Rule = application.RepeatRule
			if f.Event.IsZero() {
				observation.Outcome = "identity-review"
			} else {
				execution, ambiguous, lookupErr := matchExecution(ctx, q, account.ID(), t)
				if lookupErr != nil {
					return lookupErr
				}

				if ambiguous {
					observation.Outcome = "native-review"
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

					if old.Kind != "trade" {
						observation.Outcome = "kind-conflict"
						observation.ExecutionID = sql.NullString{String: execution, Valid: true}
						if e = q.PutObservation(ctx, observation); e != nil {
							return e
						}

						continue
					}

					accepted, e := decodeTrade(old.Accepted)
					if e != nil {
						return e
					}

					if accepted.Fields().Instrument.Reference() == f.Instrument.Reference() {
						if e = checkInstrumentRoot(ctx, q, old, f.Instrument); e != nil {
							return e
						}
					}

					observation.Outcome = "corroborated"
					if !application.Compatible(accepted, t) {
						observation.Outcome = "terms-conflict"
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
							InstrumentID: sql.NullString{String: instrument, Valid: true},
							ListingID:    sql.NullString{String: listing, Valid: listing != ""},
							Accepted:     data,
							Rule:         application.RepeatRule,
						},
					); e != nil {
						return e
					}

					if e = q.PutEventLink(
						ctx,
						sqlc.PutEventLinkParams{
							AccountID:     account.ID(),
							Reference:     referenceKey(f.Event),
							ExecutionID:   execution,
							DistinctEvent: boolInt(f.EventDistinct),
						},
					); e != nil {
						return e
					}

					observation.Outcome = "accepted"
				}

				for _, ref := range append([]source.Reference{f.Event}, f.EventAliases...) {
					if e = q.PutEventLink(
						ctx,
						sqlc.PutEventLinkParams{
							AccountID:     account.ID(),
							Reference:     referenceKey(ref),
							ExecutionID:   execution,
							DistinctEvent: boolInt(f.EventDistinct && ref == f.Event),
						},
					); e != nil {
						return e
					}
				}

				if observation.Outcome != "terms-conflict" {
					if e = putRelationships(ctx, q, execution, f.Relationships); e != nil {
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

	if changed {
		if err = projectArtifact(ctx, q, id); err != nil {
			return err
		}
	}

	if err = q.SaveProcessingReceipt(
		ctx,
		sqlc.SaveProcessingReceiptParams{ArtifactID: id, Version: version},
	); err != nil {
		return err
	}

	if err = q.ArtifactProcessed(ctx, id); err != nil {
		return err
	}

	return s.commit(ctx, tx)
}

func checkInstrumentRoot(
	ctx context.Context,
	q *sqlc.Queries,
	execution sqlc.Execution,
	i source.Instrument,
) error {
	id := execution.InstrumentID.String

	base, err := q.ActivityBase(ctx, execution.ID)
	if err == nil {
		// Source claims belong to the original root, even after an operator relink.
		id = base.InstrumentID.String
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A repeated row in the initial artifact precedes creation of its base.
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

	return resolveAutomaticInstrument(ctx, q,
		sqlc.ResolveInstrumentParams{
			ID:              root.ID,
			Name:            root.Name,
			Ticker:          root.Ticker,
			ReferenceFigi:   root.ReferenceFigi,
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
			if e = resolveAutomaticInstrument(ctx, q,
				sqlc.ResolveInstrumentParams{
					ID:              root.ID,
					Name:            root.Name,
					Ticker:          root.Ticker,
					ReferenceFigi:   root.ReferenceFigi,
					EvidenceID:      root.EvidenceID,
					ReferenceStatus: "conflict",
					ReferenceNext:   root.ReferenceNext,
				},
			); e != nil {
				return "", "", e
			}
		} else {
			f := previous.Reported().Fields()
			changed := false
			if f.Kind == source.UnknownInstrument && i.Kind() != source.UnknownInstrument {
				f.Kind = i.Kind()
				changed = true
			}

			if len(f.Identifiers) == 0 && len(i.Identifiers()) > 0 {
				f.Identifiers = i.Identifiers()
				changed = true
			}

			if f.Name == "" && i.Name() != "" {
				f.Name = i.Name()
				changed = true
			}

			if f.Ticker == "" && i.Ticker() != "" {
				f.Ticker = i.Ticker()
				changed = true
			}

			if changed {
				enriched, e := source.NewInstrument(f)
				if e != nil {
					return "", "", e
				}

				data, e := json.Marshal(instrumentWire(enriched))
				if e != nil {
					return "", "", e
				}

				if e = enrichAutomaticInstrument(
					ctx,
					q,
					sqlc.EnrichInstrumentParams{
						ID:       root.ID,
						Reported: data,
						Name:     i.Name(),
						Ticker:   i.Ticker(),
						Kind:     string(f.Kind),
					},
				); e != nil {
					return "", "", e
				}
			}
		}
	}

	if u := i.Option().Fields().Underlier; u.Kind() != "" && !root.UnderlierID.Valid {
		underlier, _, e := acceptInstrument(ctx, q, artifact, location+"/underlier", u, "")
		if e != nil {
			return "", "", e
		}

		if e = setAutomaticUnderlier(ctx, q,
			sqlc.SetUnderlierParams{ID: root.ID, UnderlierID: sql.NullString{String: underlier, Valid: true}},
		); e != nil {
			return "", "", e
		}
	}

	listing, err := acceptListing(ctx, q, root.ID, i, currency)

	return root.ID, listing, err
}

func acceptListing(
	ctx context.Context,
	q *sqlc.Queries,
	instrument string,
	i source.Instrument,
	currency string,
) (string, error) {
	if i.Venue() == "" || currency == "" {
		return "", nil
	}

	if err := q.PutListing(ctx, sqlc.PutListingParams{
		ID: rand.Text(), InstrumentID: instrument, Venue: i.Venue(), Ticker: i.Ticker(), Currency: currency,
	}); err != nil {
		return "", err
	}

	listing, err := q.ListingByTerms(ctx, sqlc.ListingByTermsParams{
		InstrumentID: instrument, Venue: i.Venue(), Ticker: i.Ticker(), Currency: currency,
	})
	if err != nil {
		return "", err
	}

	return listing.ID, nil
}

func instrumentContradiction(a, b source.Instrument) bool {
	if !application.CompatibleInstrumentKind(a.Kind(), b.Kind()) {
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

	out.Corrections, err = s.sourceInstructions(ctx, id)
	if err != nil {
		return out, err
	}

	out.Context, err = s.activityContext(ctx, r)
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
				Outcome:  application.ObservationOutcome(o.Outcome),
				Rule:     o.Rule,
			},
		)
	}

	out.Relationships, err = relationships(ctx, s.queries, id)

	return out, err
}

func (s *Store) InstrumentDetail(ctx context.Context, id string) (application.InstrumentDetail, error) {
	out := application.InstrumentDetail{}

	r, err := s.queries.Instrument(ctx, id)
	if err != nil {
		return out, err
	}

	out.Underlier = r.UnderlierID.String

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

	observations, err := s.queries.InstrumentObservations(ctx, sql.NullString{String: id, Valid: true})
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
				Outcome:  application.ObservationOutcome(o.Outcome),
				Rule:     o.Rule,
			},
		)
	}

	return out, nil
}

// A distinct event reference distinguishes fills even when an alias is shared.
// Aliases can corroborate an earlier observation with only a trade ID. Several
// possible executions stay ambiguous instead of selecting an arbitrary fill.
func matchExecution(
	ctx context.Context,
	q *sqlc.Queries,
	account string,
	t source.Trade,
) (string, bool, error) {
	f := t.Fields()
	return matchReferences(ctx, q, account, f.Event, f.EventAliases, f.EventDistinct)
}

func matchReferences(
	ctx context.Context,
	q *sqlc.Queries,
	account string,
	event source.Reference,
	aliases []source.Reference,
	distinct bool,
) (string, bool, error) {
	matches := map[string]bool{}
	for index, ref := range append([]source.Reference{event}, aliases...) {
		ids, err := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: account, Reference: referenceKey(ref)})
		if err != nil {
			return "", false, err
		}

		for _, id := range ids {
			if index > 0 && distinct {
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

					if r.Authority() == event.Authority() && r.Kind() == event.Kind() && r.Scope() == event.Scope() &&
						r != event {
						different = true
					}
				}

				if different {
					continue
				}
			}

			matches[id] = true
		}

		if index == 0 && len(matches) == 1 && distinct {
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

func putRelationships(ctx context.Context, q *sqlc.Queries, id string, links []source.Relationship) error {
	for _, r := range links {
		if err := q.PutRelationship(
			ctx,
			sqlc.PutRelationshipParams{ActivityID: id, Kind: string(r.Kind()), Target: referenceKey(r.Target())},
		); err != nil {
			return err
		}
	}

	return nil
}

func relationships(ctx context.Context, q *sqlc.Queries, id string) ([]application.Relationship, error) {
	rows, err := q.Relationships(ctx, id)
	if err != nil {
		return nil, err
	}

	out := []application.Relationship{}
	for _, r := range rows {
		out = append(out, application.Relationship{Kind: r.Kind, Target: r.Target, TargetID: r.TargetID})
	}

	return out, nil
}

func lifecycleValue(r sqlc.Execution) (application.Lifecycle, error) {
	l, err := decodeLifecycle(r.Accepted)
	if err != nil {
		return application.Lifecycle{}, ErrCorrupt
	}

	state, err := activityState(r)
	if err != nil {
		return application.Lifecycle{}, err
	}

	return application.NewLifecycle(r.ID, r.AccountID, r.InstrumentID.String, r.Rule, state, l, r.Revision)
}

func (s *Store) LifecycleDetail(ctx context.Context, id string) (application.LifecycleDetail, error) {
	out := application.LifecycleDetail{}

	r, err := s.queries.Execution(ctx, id)
	if err != nil {
		return out, err
	}

	if r.Kind != "lifecycle" {
		return out, application.ErrInvalid
	}

	out.Lifecycle, err = lifecycleValue(r)
	if err != nil {
		return out, err
	}

	out.Corrections, err = s.sourceInstructions(ctx, id)
	if err != nil {
		return out, err
	}

	out.Context, err = s.activityContext(ctx, r)
	if err != nil {
		return out, err
	}

	rows, err := s.queries.ExecutionObservations(ctx, sql.NullString{String: id, Valid: true})
	if err != nil {
		return out, err
	}

	for _, r := range rows {
		out.Observations = append(
			out.Observations,
			application.Observation{
				Artifact: r.ArtifactID,
				Version:  r.Version,
				Location: r.Location,
				Outcome:  application.ObservationOutcome(r.Outcome),
				Rule:     r.Rule,
			},
		)
	}

	out.Relationships, err = relationships(ctx, s.queries, id)

	return out, err
}

func processLifecycle(
	ctx context.Context,
	q *sqlc.Queries,
	account application.Account,
	connection, provider, stream, artifact, version string,
	l source.Lifecycle,
) error {
	f := l.Fields()

	n, err := q.ObservationExists(
		ctx,
		sqlc.ObservationExistsParams{ArtifactID: artifact, Version: version, Location: f.Location},
	)
	if err != nil || n > 0 {
		return err
	}

	o := sqlc.PutObservationParams{
		ArtifactID: artifact,
		Version:    version,
		Location:   f.Location,
		Rule:       application.AuthorityRule,
		Outcome:    "unselected",
	}
	if account.Connection() != connection || account.Provider() != provider || account.Stream() != stream ||
		account.Reference() != f.Account {
		return q.PutObservation(ctx, o)
	}

	o.Rule = application.RepeatRule
	if f.Event.IsZero() {
		o.Outcome = "identity-review"
		return q.PutObservation(ctx, o)
	}

	id, ambiguous, err := matchReferences(ctx, q, account.ID(), f.Event, f.EventAliases, f.EventDistinct)
	if err != nil {
		return err
	}

	if ambiguous {
		o.Outcome = "native-review"
		return q.PutObservation(ctx, o)
	}

	if id == "" {
		instrument, _, e := acceptInstrument(ctx, q, artifact, f.Location, f.Instrument, "")
		if e != nil {
			return e
		}

		data, e := encodeLifecycle(l)
		if e != nil {
			return e
		}

		id = rand.Text()
		if e = q.PutLifecycleActivity(
			ctx,
			sqlc.PutLifecycleActivityParams{
				ID:           id,
				AccountID:    account.ID(),
				InstrumentID: sql.NullString{String: instrument, Valid: true},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		); e != nil {
			return e
		}

		o.Outcome = "accepted"
	} else {
		row, e := q.Execution(ctx, id)
		if e != nil {
			return e
		}

		o.Outcome = "corroborated"
		old, e := decodeLifecycle(row.Accepted)
		if row.Kind != "lifecycle" || e != nil || !application.CompatibleLifecycle(old, l) {
			o.Outcome = "terms-conflict"
		}
	}

	for _, r := range append([]source.Reference{f.Event}, f.EventAliases...) {
		if err = q.PutEventLink(
			ctx,
			sqlc.PutEventLinkParams{
				AccountID:     account.ID(),
				Reference:     referenceKey(r),
				ExecutionID:   id,
				DistinctEvent: boolInt(f.EventDistinct && r == f.Event),
			},
		); err != nil {
			return err
		}
	}

	if o.Outcome != "terms-conflict" {
		if err = putRelationships(ctx, q, id, f.Relationships); err != nil {
			return err
		}
	}

	o.ExecutionID = sql.NullString{String: id, Valid: true}

	return q.PutObservation(ctx, o)
}

func conversionValue(r sqlc.Execution) (application.Conversion, error) {
	v, err := decodeConversion(r.Accepted)
	if err != nil {
		return application.Conversion{}, ErrCorrupt
	}

	state, err := activityState(r)
	if err != nil {
		return application.Conversion{}, err
	}

	return application.NewConversion(r.ID, r.AccountID, r.Rule, state, v, r.Revision)
}

func (s *Store) ConversionDetail(ctx context.Context, id string) (application.ConversionDetail, error) {
	out := application.ConversionDetail{}

	r, err := s.queries.Execution(ctx, id)
	if err != nil {
		return out, err
	}

	if r.Kind != "conversion" {
		return out, application.ErrInvalid
	}

	out.Conversion, err = conversionValue(r)
	if err != nil {
		return out, err
	}

	out.Corrections, err = s.sourceInstructions(ctx, id)
	if err != nil {
		return out, err
	}

	out.Context, err = s.activityContext(ctx, r)
	if err != nil {
		return out, err
	}

	rows, err := s.queries.ExecutionObservations(ctx, sql.NullString{String: id, Valid: true})
	if err != nil {
		return out, err
	}

	for _, r := range rows {
		out.Observations = append(
			out.Observations,
			application.Observation{
				Artifact: r.ArtifactID,
				Version:  r.Version,
				Location: r.Location,
				Outcome:  application.ObservationOutcome(r.Outcome),
				Rule:     r.Rule,
			},
		)
	}

	return out, nil
}

func processConversion(
	ctx context.Context,
	q *sqlc.Queries,
	account application.Account,
	connection, provider, stream, artifact, version string,
	c source.Conversion,
) error {
	f := c.Fields()

	n, err := q.ObservationExists(
		ctx,
		sqlc.ObservationExistsParams{ArtifactID: artifact, Version: version, Location: f.Location},
	)
	if err != nil || n > 0 {
		return err
	}

	o := sqlc.PutObservationParams{
		ArtifactID: artifact,
		Version:    version,
		Location:   f.Location,
		Rule:       application.AuthorityRule,
		Outcome:    "unselected",
	}
	if account.Connection() != connection || account.Provider() != provider || account.Stream() != stream ||
		account.Reference() != f.Account {
		return q.PutObservation(ctx, o)
	}

	o.Rule = application.RepeatRule
	if f.Event.IsZero() {
		o.Outcome = "identity-review"
		return q.PutObservation(ctx, o)
	}

	id, ambiguous, err := matchReferences(ctx, q, account.ID(), f.Event, f.EventAliases, f.EventDistinct)
	if err != nil {
		return err
	}

	if ambiguous {
		o.Outcome = "native-review"
		return q.PutObservation(ctx, o)
	}

	if id == "" {
		data, e := encodeConversion(c)
		if e != nil {
			return e
		}

		id = rand.Text()
		if e = q.PutConversionActivity(
			ctx,
			sqlc.PutConversionActivityParams{
				ID:        id,
				AccountID: account.ID(),
				Accepted:  data,
				Rule:      application.RepeatRule,
			},
		); e != nil {
			return e
		}

		o.Outcome = "accepted"
	} else {
		row, e := q.Execution(ctx, id)
		if e != nil {
			return e
		}

		o.Outcome = "corroborated"
		old, e := decodeConversion(row.Accepted)
		if row.Kind != "conversion" || e != nil || !application.CompatibleConversion(old, c) {
			o.Outcome = "terms-conflict"
		}
	}

	for _, r := range append([]source.Reference{f.Event}, f.EventAliases...) {
		if err = q.PutEventLink(
			ctx,
			sqlc.PutEventLinkParams{
				AccountID:     account.ID(),
				Reference:     referenceKey(r),
				ExecutionID:   id,
				DistinctEvent: boolInt(f.EventDistinct && r == f.Event),
			},
		); err != nil {
			return err
		}
	}

	o.ExecutionID = sql.NullString{String: id, Valid: true}

	return q.PutObservation(ctx, o)
}

func (s *Store) activityContext(
	ctx context.Context,
	row sqlc.Execution,
) (application.ActivityContext, error) {
	out := application.ActivityContext{}

	account, err := s.queries.Account(ctx)
	if err != nil {
		return out, err
	}

	out.AccountName = account.Name
	if row.InstrumentID.Valid {
		instrument, err := s.queries.Instrument(ctx, row.InstrumentID.String)
		if err != nil {
			return out, err
		}

		out.InstrumentName = instrument.Name
		if out.InstrumentName == "" {
			out.InstrumentName = instrument.Ticker
		}
	}

	return out, nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}

	return 0
}
