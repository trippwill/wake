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

func anonymousInstrumentKey(artifact, location string) string {
	b, _ := json.Marshal([]string{artifact, location})
	return string(b)
}

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
		i,
		r.Rule,
		r.Revision,
		application.SelectionAuthority(r.SelectionAuthority),
		application.ReferenceMetadata{Allocation: r.ReferenceAllocation, Reason: r.ReferenceReason},
	)
}

// Process accepts only the confirmed connection/provider/source account.
// Useful source contributions commit atomically with artifact completion.
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

	var account application.Account

	row, err := q.AccountByConnection(ctx, retained.Connection)
	if err == nil {
		account, err = accountValue(row)
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	changed := false

	hasPositions := false
	positionParents := []string{}
	seenPositionParents := map[string]bool{}
	for _, record := range records {
		hasPositions = hasPositions || record.Kind == "positions" || record.Kind == "position-instrument"

		previous, knownErr := q.ClaimProcessing(
			ctx,
			sqlc.ClaimProcessingParams{ClaimID: record.ClaimID, Ordinal: record.Ordinal},
		)
		if knownErr == nil {
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

		if record.Kind == "positions" {
			f := record.PositionSet.Fields()
			if account.AuthorizesSource(retained.Connection, retained.Provider, f.Account) {
				date := ""
				if !f.Date.IsZero() {
					date = f.Date.String()
				}

				reference := ""
				if !f.Reference.IsZero() {
					reference = referenceKey(f.Reference)
				}

				supersedes := []string{}
				for _, r := range f.Supersedes {
					supersedes = append(supersedes, referenceKey(r))
				}

				revisionData, e := json.Marshal(supersedes)
				if e != nil {
					return e
				}

				if err = q.PutPositionSet(
					ctx,
					sqlc.PutPositionSetParams{
						ClaimID:         record.ClaimID,
						AccountID:       account.ID(),
						EffectiveDate:   date,
						SourceReference: reference,
						Supersedes:      string(revisionData),
					},
				); err != nil {
					return err
				}
			}

			continue
		}

		if record.Kind == "position-instrument" {
			f := record.PositionInstrument

			ref, e := f.Account.reference()
			if e != nil {
				return e
			}

			if account.AuthorizesSource(retained.Connection, retained.Provider, ref) {
				i, e := f.Instrument.instrument()
				if e != nil {
					return e
				}

				root, _, e := acceptInstrument(ctx, q, id, record.Location, i, "")
				if e != nil {
					return e
				}

				if e = q.PutPositionBinding(
					ctx,
					sqlc.PutPositionBindingParams{
						ClaimID:      record.ClaimID,
						Ordinal:      record.Ordinal,
						AccountID:    account.ID(),
						InstrumentID: root,
					},
				); e != nil {
					return e
				}

				if e = q.PutInstrumentClaim(
					ctx,
					sqlc.PutInstrumentClaimParams{
						InstrumentID: root,
						ClaimID:      record.ClaimID,
						Ordinal:      record.Ordinal,
						Role:         "subject",
					},
				); e != nil {
					return e
				}

				if !seenPositionParents[root] {
					positionParents = append(positionParents, root)
					seenPositionParents[root] = true
				}

				changed = true
			}

			continue
		}

		changed = changed || record.Kind != "rejected" && record.Kind != "instrument"
		if !record.Observation.IsZero() {
			if err = admitActivity(ctx, q, account, retained, version, record); err != nil {
				return err
			}

			continue
		}

		if record.Kind == "correction" {
			f := record.Correction.Fields()
			if account.AuthorizesSource(retained.Connection, retained.Provider, f.Account) {
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
	}

	var reinterpretingPositions bool
	if err = tx.QueryRowContext(ctx, positionsStatement("Reinterpreting"), version, id).
		Scan(&reinterpretingPositions); err != nil {
		return err
	}

	if err = q.SaveProcessingReceipt(
		ctx,
		sqlc.SaveProcessingReceiptParams{ArtifactID: id, Version: version},
	); err != nil {
		return err
	}

	if err = bindUnderlierInstrumentClaims(ctx, q, positionParents); err != nil {
		return err
	}

	if changed && account.ID() != "" {
		if err = projectArtifact(ctx, q, id, account.ID()); err != nil {
			return err
		}
	} else if reinterpretingPositions || hasPositions {
		if err = projectArtifactRoots(ctx, q, id); err != nil {
			return err
		}
	}

	if err = q.ArtifactProcessed(ctx, id); err != nil {
		return err
	}
	// Reinterpretation retires earlier meanings while retaining every source claim.
	if _, err = tx.ExecContext(ctx, positionsStatement("InterpretationChanged"), id); err != nil {
		return err
	}

	return s.commit(ctx, tx)
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
		key = anonymousInstrumentKey(artifact, location)
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
				CalculationRule: string(application.RetainedClaimsRule),
			},
		)
	}

	if err != nil {
		return "", "", err
	}

	// Root enrichment and underlier selection are calculated from the complete
	// retained claim set after admission, before any shared root is written.

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

	listings, err := q.ListingsByTerms(
		ctx,
		sqlc.ListingsByTermsParams{
			InstrumentID: instrument,
			Venue:        i.Venue(),
			Ticker:       i.Ticker(),
			Currency:     currency,
		},
	)
	if err != nil {
		return "", err
	}
	// A reported venue identifier can select an already verified representation.
	hasVenueFIGI := false
	for _, identifier := range i.Identifiers() {
		if identifier.Kind() != source.VenueFIGI && identifier.Kind() != source.UnclassifiedFIGI {
			continue
		}

		hasVenueFIGI = true

		for _, listing := range listings {
			if listing.Figi == identifier.Value() {
				return listing.ID, nil
			}
		}
	}

	if len(listings) == 1 {
		if hasVenueFIGI && listings[0].Figi != "" {
			return "", nil
		}

		return listings[0].ID, nil
	}

	if len(listings) > 1 {
		return "", nil
	} // labels alone cannot choose a venue identity

	id := rand.Text()
	if err = q.PutListing(
		ctx,
		sqlc.PutListingParams{
			ID:           id,
			InstrumentID: instrument,
			Venue:        i.Venue(),
			Ticker:       i.Ticker(),
			Currency:     currency,
		},
	); err != nil {
		return "", err
	}

	return id, nil
}

func (s *Store) ExecutionDetail(ctx context.Context, id string) (application.ExecutionDetail, error) {
	out := application.ExecutionDetail{}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	r, err := q.Execution(ctx, id)
	if err != nil {
		return out, err
	}

	out.Execution, err = executionValue(r)
	if err != nil {
		return out, err
	}

	out.Corrections, err = sourceInstructions(ctx, q, id)
	if err != nil {
		return out, err
	}

	out.Context, err = activityContext(ctx, tx, q, r)
	if err != nil {
		return out, err
	}

	out.Relationships, err = relationships(ctx, q, id)

	return out, err
}

func (s *Store) InstrumentDetail(ctx context.Context, id string) (application.InstrumentDetail, error) {
	out := application.InstrumentDetail{}

	target, err := canonicalID(ctx, s.queries, "instruments", id)
	if err != nil {
		return out, err
	}

	id = target

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

	return out, nil
}

// A distinct event reference distinguishes fills even when an alias is shared.
// Aliases can corroborate an earlier observation with only a trade ID. Several
// possible executions stay ambiguous instead of selecting an arbitrary fill.
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

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	r, err := q.Execution(ctx, id)
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

	out.Corrections, err = sourceInstructions(ctx, q, id)
	if err != nil {
		return out, err
	}

	out.Context, err = activityContext(ctx, tx, q, r)
	if err != nil {
		return out, err
	}

	out.Relationships, err = relationships(ctx, q, id)

	return out, err
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

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	r, err := q.Execution(ctx, id)
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

	out.Corrections, err = sourceInstructions(ctx, q, id)
	if err != nil {
		return out, err
	}

	out.Context, err = activityContext(ctx, tx, q, r)
	if err != nil {
		return out, err
	}

	return out, nil
}

func activityContext(
	ctx context.Context,
	tx *sql.Tx,
	q *sqlc.Queries,
	row sqlc.Execution,
) (application.ActivityContext, error) {
	out := application.ActivityContext{}

	account, err := q.AccountByID(ctx, row.AccountID)
	if err != nil {
		return out, err
	}

	out.AccountName = account.Name

	out.Realized, err = publishedRealized(ctx, q, row.ID)
	if err != nil {
		return out, err
	}

	if row.InstrumentID.Valid {
		instrument, err := q.Instrument(ctx, row.InstrumentID.String)
		if err != nil {
			return out, err
		}

		out.InstrumentName = instrument.Name
		out.InstrumentTicker = instrument.Ticker
	}

	// Internal revisions can advance without a publication. Read the explanation
	// from the same snapshot as the accepted terms.
	_, explanation, err := readPublished(ctx, tx, "activity", row.ID)
	if err != nil && !errors.Is(err, application.ErrNotFound) {
		return out, err
	}

	out.FieldDetails = explanation.Fields

	return out, nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}

	return 0
}

// Admission reads only prior retained source assertions. The incoming observation
// is inserted afterwards; operator decisions never enter this comparison.
