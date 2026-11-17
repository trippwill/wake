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
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func anonymousInstrumentKey(artifact, location string) string {
	b, _ := json.Marshal([]string{artifact, location})
	return string(b)
}

func executionValue(r sqlc.Execution) (application.Execution, error) {
	t, err := recordcodec.DecodeTrade(r.Accepted)
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
	i, err := recordcodec.DecodeInstrument(r.Reported)
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
		calculation.SelectionAuthority(r.SelectionAuthority),
		application.ReferenceMetadata{Allocation: r.ReferenceAllocation, Reason: r.ReferenceReason},
	)
}

func acceptInstrument(
	ctx context.Context,
	q *sqlc.Queries,
	artifact, location string,
	i source.Instrument,
	currency string,
) (string, string, error) {
	key := recordcodec.ReferenceKey(i.Reference())
	if i.Reference().IsZero() {
		key = anonymousInstrumentKey(artifact, location)
	}

	root, err := q.InstrumentBySource(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		reported, e := recordcodec.EncodeInstrument(i)
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
				Reported:  reported, Rule: calculation.InstrumentRule,
				CalculationRule: string(calculation.RetainedClaimsRule),
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

	plan := calculation.PlanListingAdmission(i.Identifiers(), listingIdentities(listings))
	if !plan.Create {
		return plan.ExistingID, nil
	}

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

func listingIdentities(rows []sqlc.Listing) []calculation.ListingIdentity {
	identities := make([]calculation.ListingIdentity, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, calculation.ListingIdentity{ID: row.ID, FIGI: row.Figi})
	}

	return identities
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

func putRelationships(ctx context.Context, q *sqlc.Queries, id string, links []source.Relationship) error {
	for _, r := range links {
		if err := q.PutRelationship(
			ctx,
			sqlc.PutRelationshipParams{
				ActivityID: id,
				Kind:       string(r.Kind()),
				Target:     recordcodec.ReferenceKey(r.Target()),
			},
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
	l, err := recordcodec.DecodeLifecycle(r.Accepted)
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
		return out, calculation.ErrInvalid
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
	v, err := recordcodec.DecodeConversion(r.Accepted)
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
		return out, calculation.ErrInvalid
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
