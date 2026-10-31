// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func correctionStates(ctx context.Context, q *sqlc.Queries) (map[string]application.ActivityState, error) {
	return correctionStatesFor(ctx, q, nil)
}

func correctionStatesFor(
	ctx context.Context,
	q *sqlc.Queries,
	entities map[string]bool,
) (map[string]application.ActivityState, error) {
	rows, err := q.SourceCorrections(ctx)
	if err != nil {
		return nil, err
	}

	edges := make([]application.CorrectionEdge, len(rows))
	for i, r := range rows {
		targets, e := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Target})
		if e != nil {
			return nil, e
		}

		replacements, e := q.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Replacement},
		)
		if e != nil {
			return nil, e
		}

		edges[i] = application.CorrectionEdge{
			Kind:         source.CorrectionKind(r.Kind),
			Targets:      targets,
			Replacements: replacements,
		}
	}

	states, statuses := application.CorrectionStates(edges)
	for i, r := range rows {
		applies := entities == nil
		for _, id := range append(append([]string{}, edges[i].Targets...), edges[i].Replacements...) {
			applies = applies || entities[id]
		}

		if !applies || r.Status == string(statuses[i]) {
			continue
		}

		if err = q.CorrectionStatus(
			ctx,
			sqlc.CorrectionStatusParams{
				ArtifactID: r.ArtifactID,
				Version:    r.Version,
				Location:   r.Location,
				Status:     string(statuses[i]),
			},
		); err != nil {
			return nil, err
		}
	}

	return states, nil
}

func projectAll(ctx context.Context, q *sqlc.Queries) error {
	if err := q.SeedActivityBases(ctx); err != nil {
		return err
	}

	states, err := correctionStates(ctx, q)
	if err != nil {
		return err
	}

	rows, err := q.Executions(ctx)
	if err != nil {
		return err
	}

	for _, r := range rows {
		if err = projectActivity(ctx, q, r, states[r.ID]); err != nil {
			return err
		}
	}

	instruments, err := q.Instruments(ctx)
	if err != nil {
		return err
	}

	for _, i := range instruments {
		if err = projectInstrument(ctx, q, i.ID); err != nil {
			return err
		}
	}

	return nil
}

func projectActivity(
	ctx context.Context,
	q *sqlc.Queries,
	row sqlc.Execution,
	correctionState application.ActivityState,
) error {
	base, err := q.ActivityBase(ctx, row.ID)
	if err != nil {
		return err
	}

	inputs, err := q.ActivityInputs(ctx, sql.NullString{String: row.ID, Valid: true})
	if err != nil {
		return err
	}

	var (
		data        []byte
		instruments []source.Instrument
	)

	conflict := false
	switch row.Kind {
	case "trade":
		initial, e := decodeTrade(base.Data)
		if e != nil {
			return e
		}

		observations := []source.Trade{}
		for _, r := range inputs {
			if r.Kind != "trade" {
				conflict = true
				continue
			}

			v, e := decodeTrade(r.Data)
			if e != nil {
				return e
			}

			observations = append(observations, v)
			instruments = append(instruments, v.Fields().Instrument)
		}

		selected, disagreement, e := application.SelectTrades(initial, observations)
		if e != nil {
			return e
		}

		conflict = conflict || disagreement

		data, err = encodeTrade(selected)
		if err == nil {
			err = enrichRootFromActivity(ctx, q, base.InstrumentID, selected.Fields().Instrument, instruments)
		}

		if err == nil && base.InstrumentID.Valid && !base.ListingID.Valid {
			var listing string

			listing, err = acceptListing(
				ctx,
				q,
				base.InstrumentID.String,
				selected.Fields().Instrument,
				selected.Fields().Price.Currency().String(),
			)
			base.ListingID = sql.NullString{String: listing, Valid: listing != ""}
		}
	case "lifecycle":
		initial, e := decodeLifecycle(base.Data)
		if e != nil {
			return e
		}

		observations := []source.Lifecycle{}
		for _, r := range inputs {
			if r.Kind != "lifecycle" {
				conflict = true
				continue
			}

			v, e := decodeLifecycle(r.Data)
			if e != nil {
				return e
			}

			observations = append(observations, v)
			instruments = append(instruments, v.Fields().Instrument)
		}

		selected, disagreement, e := application.SelectLifecycles(initial, observations)
		if e != nil {
			return e
		}

		conflict = conflict || disagreement

		data, err = encodeLifecycle(selected)
		if err == nil {
			err = enrichRootFromActivity(ctx, q, base.InstrumentID, selected.Fields().Instrument, instruments)
		}
	case "conversion":
		initial, e := decodeConversion(base.Data)
		if e != nil {
			return e
		}

		observations := []source.Conversion{}
		for _, r := range inputs {
			if r.Kind != "conversion" {
				conflict = true
				continue
			}

			v, e := decodeConversion(r.Data)
			if e != nil {
				return e
			}

			observations = append(observations, v)
		}

		selected, disagreement, e := application.SelectConversions(initial, observations)
		if e != nil {
			return e
		}

		conflict = conflict || disagreement
		data, err = encodeConversion(selected)
	default:
		return ErrCorrupt
	}

	if err != nil {
		return err
	}

	lifecycle := application.Accepted

	review := correctionState.ReviewReasons()
	if conflict {
		review |= application.ConflictingClaims
	}

	if correctionState.Lifecycle() != "" {
		lifecycle = correctionState.Lifecycle()
	}

	data, instrument, listing, decided, err := applyActivityDecisions(
		ctx,
		q,
		row,
		data,
		base.InstrumentID,
		base.ListingID,
	)
	if err != nil {
		return err
	}

	authority := application.AutomaticSelection
	if decided {
		authority = application.OperatorSelection
	}

	return q.ProjectActivity(
		ctx,
		sqlc.ProjectActivityParams{
			ID:                 row.ID,
			Accepted:           data,
			Status:             string(lifecycle),
			ReviewReasons:      int64(review),
			SelectionAuthority: string(authority),
			InstrumentID:       instrument,
			ListingID:          listing,
		},
	)
}

func enrichRootFromActivity(
	ctx context.Context,
	q *sqlc.Queries,
	id sql.NullString,
	i source.Instrument,
	observations []source.Instrument,
) error {
	if !id.Valid {
		return nil
	}

	root, err := q.Instrument(ctx, id.String)
	if err != nil {
		return err
	}

	previous, err := instrumentValue(root)
	if err != nil {
		return err
	}

	known := previous.Reported()
	conflict := !application.CompatibleInstrument(known, i)
	for _, observation := range observations {
		if !known.Reference().IsZero() && !observation.Reference().IsZero() &&
			observation.Reference() != known.Reference() {
			continue
		}

		if !application.CompatibleInstrument(known, observation) {
			conflict = true
			break
		}
		// Accumulate only for comparison; disputed enrichment must not replace the root.
		known, err = application.EnrichInstrument(known, observation)
		if err != nil {
			conflict = true
			break
		}
	}

	if conflict {
		return resolveAutomaticInstrument(
			ctx,
			q,
			sqlc.ResolveInstrumentParams{
				ID:              root.ID,
				ReferenceStatus: "conflict",
				ReferenceNext:   root.ReferenceNext,
			},
		)
	}

	enriched, err := application.EnrichInstrument(previous.Reported(), i)
	if err != nil {
		return err
	}

	data, err := json.Marshal(instrumentWire(enriched))
	if err != nil {
		return err
	}

	if !bytes.Equal(data, root.Reported) {
		if err = enrichAutomaticInstrument(ctx, q,
			sqlc.EnrichInstrumentParams{
				ID:       root.ID,
				Reported: data,
				Kind:     string(enriched.Kind()),
				Name:     enriched.Name(),
				Ticker:   enriched.Ticker(),
			},
		); err != nil {
			return err
		}
	}

	if err = q.EnsureInstrumentSelection(ctx, root.ID); err != nil {
		return err
	}

	selection, err := q.InstrumentSelection(ctx, root.ID)
	if err != nil {
		return err
	}

	if !selection.UnderlierID.Valid {
		if u := enriched.Option().Fields().Underlier; u.Kind() != "" {
			underlier, _, e := acceptInstrument(ctx, q, "retained", root.ID+"/underlier", u, "")
			if e != nil {
				return e
			}

			return setAutomaticUnderlier(ctx, q,
				sqlc.SetUnderlierParams{ID: root.ID, UnderlierID: sql.NullString{String: underlier, Valid: true}},
			)
		}
	}

	return nil
}
