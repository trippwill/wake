// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func activeDecisions(rows []sqlc.OperatorDecision) map[string]sqlc.OperatorDecision {
	active := map[string]sqlc.OperatorDecision{}
	for _, r := range rows {
		if r.Action == "release" {
			delete(active, r.Scope)
		} else {
			active[r.Scope] = r
		}
	}

	return active
}

func (s *Store) SaveDecision(ctx context.Context, d application.Decision, actor string, at time.Time) error {
	f := d.Fields()
	if _, err := application.NewDecision(f); err != nil {
		return err
	}

	if actor == "" || len(actor) > 256 || at.IsZero() {
		return application.ErrInvalid
	}

	request, err := json.Marshal(f)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	prior, err := q.DecisionSubmission(ctx, f.Submission)
	if err == nil {
		if bytes.Equal(prior.Request, request) && prior.Actor == actor {
			return nil
		}

		return application.ErrStale
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var revision int64
	if f.TargetKind == "activity" {
		r, e := q.Execution(ctx, f.TargetID)
		if e != nil {
			return e
		}

		revision = r.Revision
	} else {
		r, e := q.Instrument(ctx, f.TargetID)
		if e != nil {
			return e
		}

		revision = r.Revision
	}

	if revision != f.ExpectedRevision {
		return application.ErrStale
	}

	rows, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: f.TargetKind, TargetID: f.TargetID})
	if err != nil {
		return err
	}

	active := activeDecisions(rows)

	old := active[f.Scope]
	if f.Action == "release" && old.ID == "" {
		return application.ErrInvalid
	}

	if err = q.PutDecision(
		ctx,
		sqlc.PutDecisionParams{
			ID:         rand.Text(),
			TargetKind: f.TargetKind,
			TargetID:   f.TargetID,
			Scope:      f.Scope,
			Text:       f.Text,
			Currency:   f.Currency,
			Action:     f.Action,
			Actor:      actor,
			At:         at.UTC().Format(time.RFC3339Nano),
			Rationale:  f.Rationale,
			Supersedes: old.ID,
			Submission: f.Submission,
			Request:    request,
		},
	); err != nil {
		return err
	}

	if f.TargetKind == "activity" {
		row, e := q.Execution(ctx, f.TargetID)
		if e != nil {
			return e
		}

		states, e := correctionStates(ctx, q)
		if e != nil {
			return e
		}

		if e = projectActivity(ctx, q, row, states[row.ID]); e != nil {
			return e
		}
		// A decision changes edit authority even if its value already matches.
		if err = q.AdvanceActivityRevision(ctx, row.ID); err != nil {
			return err
		}
	} else {
		if err = projectInstrument(ctx, q, f.TargetID); err != nil {
			return err
		}

		if err = q.AdvanceInstrumentRevision(ctx, f.TargetID); err != nil {
			return err
		}
	}

	return s.commit(ctx, tx)
}

func observationKey(r sqlc.Record) string {
	b, _ := json.Marshal([3]string{r.ArtifactID, r.Version, r.Location})
	return string(b)
}

func applyActivityDecisions(
	ctx context.Context,
	q *sqlc.Queries,
	row sqlc.Execution,
	data []byte,
	instrument, listing sql.NullString,
) ([]byte, sql.NullString, sql.NullString, bool, error) {
	fail := func(err error) ([]byte, sql.NullString, sql.NullString, bool, error) {
		return nil, instrument, listing, false, err
	}

	rows, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "activity", TargetID: row.ID})
	if err != nil {
		return fail(err)
	}

	active := activeDecisions(rows)
	if d, ok := active["observation"]; ok {
		inputs, e := q.ActivityInputs(ctx, sql.NullString{String: row.ID, Valid: true})
		if e != nil {
			return fail(e)
		}

		found := false
		for _, r := range inputs {
			if observationKey(r) == d.Text && r.Kind == row.Kind {
				data = r.Data
				found = true

				break
			}
		}

		if !found {
			return fail(application.ErrInvalid)
		}
	}

	scopes := make([]string, 0, len(active))
	for scope := range active {
		scopes = append(scopes, scope)
	}

	sort.Strings(scopes)

	for _, scope := range scopes {
		d := active[scope]
		switch scope {
		case "observation":
			continue
		case "instrument":
			r, e := q.Instrument(ctx, d.Text)
			if e != nil {
				return fail(application.ErrInvalid)
			}

			if !row.InstrumentID.Valid {
				return fail(application.ErrInvalid)
			}

			old, e := q.Instrument(ctx, row.InstrumentID.String)
			if e != nil {
				return fail(e)
			}

			if r.Kind != old.Kind {
				return fail(application.ErrInvalid)
			}

			if listing.Valid {
				current, e := q.Listing(ctx, listing.String)
				if e != nil {
					return fail(e)
				}

				if current.InstrumentID != r.ID {
					listing = sql.NullString{}
				}
			}

			instrument = sql.NullString{String: r.ID, Valid: true}
		case "listing":
			if d.Text == "" {
				listing = sql.NullString{}
				continue
			}

			r, e := q.Listing(ctx, d.Text)
			if e != nil || r.InstrumentID != instrument.String {
				return fail(application.ErrInvalid)
			}

			listing = sql.NullString{String: r.ID, Valid: true}
		default:
			switch row.Kind {
			case "trade":
				t, e := decodeTrade(data)
				if e != nil {
					return fail(e)
				}

				t, e = application.ApplyTradeField(t, scope, d.Text, d.Currency)
				if e != nil {
					return fail(e)
				}

				data, err = encodeTrade(t)
			case "lifecycle":
				l, e := decodeLifecycle(data)
				if e != nil {
					return fail(e)
				}

				l, e = application.ApplyLifecycleField(l, scope, d.Text, d.Currency)
				if e != nil {
					return fail(e)
				}

				data, err = encodeLifecycle(l)
			case "conversion":
				c, e := decodeConversion(data)
				if e != nil {
					return fail(e)
				}

				c, e = application.ApplyConversionField(c, scope, d.Text, d.Currency)
				if e != nil {
					return fail(e)
				}

				data, err = encodeConversion(c)
			default:
				return fail(application.ErrInvalid)
			}

			if err != nil {
				return fail(err)
			}
		}
	}

	return data, instrument, listing, len(active) > 0, nil
}

func projectInstrument(ctx context.Context, q *sqlc.Queries, id string) error {
	if err := q.EnsureInstrumentSelection(ctx, id); err != nil {
		return err
	}

	base, err := q.InstrumentSelection(ctx, id)
	if err != nil {
		return err
	}

	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	rows, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "instrument", TargetID: id})
	if err != nil {
		return err
	}

	active := activeDecisions(rows)
	name, ticker, figi, evidence, status, underlier := base.Name, base.Ticker, base.Figi, base.Evidence, base.Status, base.UnderlierID
	if d, ok := active["reference"]; ok {
		record, selectedEvidence, e := referenceCandidate(ctx, q, id, d.Text)
		if e != nil {
			return e
		}

		r, selected, compatible := reference.Select(source.InstrumentKind(root.Kind), []reference.Record{record})
		if !compatible {
			return application.ErrInvalid
		}

		others, e := q.CompetingFIGI(ctx, sqlc.CompetingFIGIParams{CurrentID: id, Figi: selected})
		if e != nil {
			return e
		}

		if len(others) > 0 {
			return application.ErrInvalid
		}

		name, ticker, figi, evidence, status = r.Name, r.Ticker, selected, selectedEvidence, "selected by operator"
	}

	if d, ok := active["name"]; ok {
		if !validDecisionLabel(d.Text, 1024) {
			return application.ErrInvalid
		}

		name = d.Text
	}

	if d, ok := active["ticker"]; ok {
		if d.Text != "" && !validDecisionLabel(d.Text, 256) {
			return application.ErrInvalid
		}

		ticker = d.Text
	}

	if d, ok := active["underlier"]; ok {
		if root.Kind != string(source.Option) {
			return application.ErrInvalid
		}

		underlier = sql.NullString{}
		if d.Text != "" {
			u, e := q.Instrument(ctx, d.Text)
			if e != nil || (u.Kind != string(source.Equity) && u.Kind != string(source.Fund)) || u.ID == id {
				return application.ErrInvalid
			}

			underlier = sql.NullString{String: u.ID, Valid: true}
		}
	}

	for scope := range active {
		if !slices.Contains([]string{"reference", "name", "ticker", "underlier"}, scope) {
			return application.ErrInvalid
		}
	}

	return q.ProjectInstrument(
		ctx,
		sqlc.ProjectInstrumentParams{
			ID:          id,
			Name:        name,
			Ticker:      ticker,
			Figi:        figi,
			Evidence:    evidence,
			Status:      status,
			UnderlierID: underlier,
		},
	)
}

func validDecisionLabel(s string, max int) bool {
	return s != "" && len(s) <= max && strings.TrimSpace(s) == s
}

func referenceCandidate(
	ctx context.Context,
	q *sqlc.Queries,
	id, value string,
) (reference.Record, string, error) {
	var selected struct {
		Evidence    string
		Job, Record int
	}
	if json.Unmarshal([]byte(value), &selected) != nil {
		return reference.Record{}, "", application.ErrInvalid
	}

	rows, err := q.RetainedReferenceCandidates(ctx, id)
	if err != nil {
		return reference.Record{}, "", err
	}

	for _, row := range rows {
		if row.ID != selected.Evidence {
			continue
		}

		root, e := q.Instrument(ctx, id)
		if e != nil {
			return reference.Record{}, "", e
		}

		instrument, e := instrumentValue(root)
		if e != nil {
			return reference.Record{}, "", e
		}

		var jobs []reference.Job
		if json.Unmarshal(row.Request, &jobs) != nil || selected.Job < 0 || selected.Job >= len(jobs) ||
			!referenceJobMatches(instrument.Reported(), jobs[selected.Job]) {
			return reference.Record{}, "", application.ErrInvalid
		}

		var decoded []struct {
			Data []reference.Record `json:"data"`
		}
		if json.Unmarshal(row.Response, &decoded) != nil || selected.Job < 0 || selected.Job >= len(decoded) ||
			selected.Record < 0 ||
			selected.Record >= len(decoded[selected.Job].Data) {
			return reference.Record{}, "", application.ErrInvalid
		}

		return decoded[selected.Job].Data[selected.Record], row.ID, nil
	}

	return reference.Record{}, "", application.ErrInvalid
}

func referenceCandidateKey(id string, job, record int) string {
	b, _ := json.Marshal(struct {
		Evidence    string
		Job, Record int
	}{id, job, record})

	return string(b)
}

func (s *Store) CorrectionForm(ctx context.Context, kind, id string) (application.CorrectionForm, error) {
	out := application.CorrectionForm{Kind: kind, ID: id}
	family := source.InstrumentKind("")
	switch kind {
	case "activity":
		row, err := s.queries.Execution(ctx, id)
		if err != nil {
			return out, err
		}

		out.Label = observationLabel(sqlc.Record{Kind: row.Kind, Data: row.Accepted})
		out.Revision = row.Revision

		out.Values, err = comparisonValues(row.Kind, row.Accepted)
		if err != nil {
			return out, err
		}

		out.Values["instrument"] = application.CorrectionValue{Value: row.InstrumentID.String}
		out.Values["listing"] = application.CorrectionValue{Value: row.ListingID.String}
		if account, e := s.queries.Account(ctx); e == nil {
			out.AccountName = account.Name
		}

		switch row.Kind {
		case "trade":
			t, e := decodeTrade(row.Accepted)
			if e != nil {
				return out, e
			}

			family = t.Fields().Instrument.Kind()
		case "lifecycle":
			family = source.Option
		}

		for _, field := range application.ActivityFields(row.Kind) {
			if strings.HasPrefix(field, "option.") && family != source.Option {
				continue
			}

			if (strings.HasPrefix(field, "debt.") || field == "quotation_basis") && family != source.Debt {
				continue
			}

			out.Fields = append(out.Fields, field)
		}

		inputs, err := s.queries.ActivityInputs(ctx, sql.NullString{String: id, Valid: true})
		if err != nil {
			return out, err
		}

		for _, r := range inputs {
			if r.Kind == row.Kind {
				values, err := comparisonValues(r.Kind, r.Data)
				if err != nil {
					return out, err
				}

				out.Candidates = append(
					out.Candidates,
					application.CorrectionCandidate{
						Value:  observationKey(r),
						Label:  observationLabel(r),
						Fields: values,
					},
				)
			}
		}
	case "instrument":
		row, err := s.queries.Instrument(ctx, id)
		if err != nil {
			return out, err
		}

		out.Label = row.Name + " · " + row.Kind

		out.Values = map[string]application.CorrectionValue{
			"name":               {Value: row.Name},
			"reference_identity": {Value: row.ReferenceFigi},
			"ticker":             {Value: row.Ticker},
			"underlier":          {Value: row.UnderlierID.String},
		}
		out.Revision = row.Revision
		out.Fields = []string{"name", "ticker"}
		if row.Kind == string(source.Option) {
			out.Fields = append(out.Fields, "underlier")
		}

		candidates, err := s.queries.RetainedReferenceCandidates(ctx, id)
		if err != nil {
			return out, err
		}

		for _, c := range candidates {
			var jobs []reference.Job
			if json.Unmarshal(c.Request, &jobs) != nil {
				continue
			}

			value, e := instrumentValue(row)
			if e != nil {
				return out, e
			}

			var response []struct {
				Data []reference.Record `json:"data"`
			}
			if json.Unmarshal(c.Response, &response) != nil {
				continue
			}

			for j, job := range response {
				if j >= len(jobs) || !referenceJobMatches(value.Reported(), jobs[j]) {
					continue
				}

				for k, r := range job.Data {
					if _, identity, ok := reference.Select(source.InstrumentKind(row.Kind), []reference.Record{r}); ok {
						out.Candidates = append(
							out.Candidates,
							application.CorrectionCandidate{
								Value: referenceCandidateKey(c.ID, j, k),
								Label: r.Name + " · " + r.Ticker + " · " + r.FIGI,
								Fields: map[string]application.CorrectionValue{
									"name":               {Value: r.Name},
									"reference_identity": {Value: identity},
									"ticker":             {Value: r.Ticker},
								},
							},
						)
					}
				}
			}
		}
	default:
		return out, application.ErrInvalid
	}

	roots, err := s.queries.Instruments(ctx)
	if err != nil {
		return out, err
	}

	for _, root := range roots {
		for _, scope := range []string{"instrument", "underlier"} {
			if current := out.Values[scope]; current.Value == root.ID {
				current.Label = root.Kind + " · " + root.Name + " · Wake ID " + root.ID
				out.Values[scope] = current
			}
		}

		label := root.Name
		if root.Ticker != "" {
			label += " (" + root.Ticker + ")"
		}

		label += " · " + root.Kind + " · Wake ID " + root.ID
		if root.ReferenceFigi != "" {
			label += " · " + root.ReferenceFigi
		}

		if kind == "activity" && family != "" && root.Kind == string(family) {
			out.Links = append(
				out.Links,
				application.CorrectionCandidate{Value: root.ID, Scope: "instrument", Label: label},
			)
		}

		if kind == "instrument" && slices.Contains(out.Fields, "underlier") && root.ID != id &&
			(root.Kind == string(source.Equity) || root.Kind == string(source.Fund)) {
			out.Links = append(
				out.Links,
				application.CorrectionCandidate{Value: root.ID, Scope: "underlier", Label: label},
			)
		}

		if kind == "activity" && root.ID == out.Values["instrument"].Value {
			listings, e := s.queries.Listings(ctx, root.ID)
			if e != nil {
				return out, e
			}

			for _, l := range listings {
				if current := out.Values["listing"]; current.Value == l.ID {
					current.Label = root.Name + " · " + l.Venue + " · " + l.Currency + " · Wake ID " + l.ID
					out.Values["listing"] = current
				}

				out.Links = append(
					out.Links,
					application.CorrectionCandidate{
						Value: l.ID,
						Scope: "listing",
						Label: "Listing · " + root.Name + " · " + l.Venue + " · " + l.Currency + " · Wake ID " + l.ID,
					},
				)
			}
		}
	}

	rows, err := s.queries.Decisions(ctx, sqlc.DecisionsParams{TargetKind: kind, TargetID: id})
	if err != nil {
		return out, err
	}

	active := activeDecisions(rows)
	for _, r := range rows {
		out.Decisions = append(
			out.Decisions,
			application.DecisionRecord{
				ID:         r.ID,
				Scope:      r.Scope,
				Text:       r.Text,
				Currency:   r.Currency,
				Action:     r.Action,
				Actor:      r.Actor,
				At:         r.At,
				Rationale:  r.Rationale,
				Supersedes: r.Supersedes,
				Active:     active[r.Scope].ID == r.ID,
			},
		)
	}

	return out, nil
}

func referenceJobMatches(i source.Instrument, j reference.Job) bool {
	for _, id := range i.Identifiers() {
		kind := "ID_BB_GLOBAL"
		switch id.Kind() {
		case source.ISIN:
			kind = "ID_ISIN"
		case source.CUSIP:
			kind = "ID_CUSIP"
		case source.SEDOL:
			kind = "ID_SEDOL"
		}

		if j.IDType == kind && j.IDValue == id.Value() {
			return true
		}
	}

	return false
}

func resolveAutomaticInstrument(ctx context.Context, q *sqlc.Queries, p sqlc.ResolveInstrumentParams) error {
	if err := q.EnsureInstrumentSelection(ctx, p.ID); err != nil {
		return err
	}

	base, err := q.InstrumentSelection(ctx, p.ID)
	if err != nil {
		return err
	}

	if p.ReferenceStatus != "conflict" {
		base.Name, base.Ticker, base.Figi, base.Evidence = p.Name, p.Ticker, p.ReferenceFigi, p.EvidenceID
	}

	if err = q.SaveInstrumentSelection(
		ctx,
		sqlc.SaveInstrumentSelectionParams{
			InstrumentID: p.ID,
			Name:         base.Name,
			Ticker:       base.Ticker,
			Figi:         base.Figi,
			Evidence:     base.Evidence,
			Status:       p.ReferenceStatus,
			UnderlierID:  base.UnderlierID,
		},
	); err != nil {
		return err
	}

	if err = q.ReferenceNext(
		ctx,
		sqlc.ReferenceNextParams{ID: p.ID, ReferenceNext: p.ReferenceNext},
	); err != nil {
		return err
	}

	return projectInstrument(ctx, q, p.ID)
}

func setAutomaticUnderlier(ctx context.Context, q *sqlc.Queries, p sqlc.SetUnderlierParams) error {
	if err := q.EnsureInstrumentSelection(ctx, p.ID); err != nil {
		return err
	}

	base, err := q.InstrumentSelection(ctx, p.ID)
	if err != nil {
		return err
	}

	if base.UnderlierID.Valid {
		return nil
	}

	if err = q.SaveInstrumentSelection(
		ctx,
		sqlc.SaveInstrumentSelectionParams{
			InstrumentID: p.ID,
			Name:         base.Name,
			Ticker:       base.Ticker,
			Figi:         base.Figi,
			Evidence:     base.Evidence,
			Status:       base.Status,
			UnderlierID:  p.UnderlierID,
		},
	); err != nil {
		return err
	}

	return projectInstrument(ctx, q, p.ID)
}

func observationLabel(r sqlc.Record) string {
	label := r.Kind
	switch r.Kind {
	case "trade":
		if t, err := decodeTrade(r.Data); err == nil {
			f := t.Fields()
			label = f.TradeDate.String() + " · " + string(
				f.Side,
			) + " · " + f.Quantity.Value() + " " + t.QuantityUnit() + " · " + f.Price.Amount() + " " + f.Price.Currency().
				String()
		}
	case "lifecycle":
		if l, err := decodeLifecycle(r.Data); err == nil {
			label = l.Fields().Date.String() + " · " + string(
				l.Fields().Kind,
			) + " · " + l.Fields().Quantity.Value() + " contracts"
		}
	case "conversion":
		if c, err := decodeConversion(r.Data); err == nil {
			f := c.Fields()
			label = f.Date.String() + " · " + f.Sent.Amount() + " " + f.Sent.Currency().
				String() +
				" sent · " + f.Received.Amount() + " " + f.Received.Currency().
				String() +
				" received"
		}
	}

	if r.ArtifactID == "" {
		return label
	}

	return label + " · report " + r.ArtifactID
}

func enrichAutomaticInstrument(ctx context.Context, q *sqlc.Queries, p sqlc.EnrichInstrumentParams) error {
	if err := q.EnsureInstrumentSelection(ctx, p.ID); err != nil {
		return err
	}

	base, err := q.InstrumentSelection(ctx, p.ID)
	if err != nil {
		return err
	}

	if err = q.EnrichInstrument(ctx, p); err != nil {
		return err
	}

	if base.Name == "" {
		base.Name = p.Name
	}

	if base.Ticker == "" {
		base.Ticker = p.Ticker
	}

	if err = q.SaveInstrumentSelection(
		ctx,
		sqlc.SaveInstrumentSelectionParams{
			InstrumentID: p.ID,
			Name:         base.Name,
			Ticker:       base.Ticker,
			Figi:         base.Figi,
			Evidence:     base.Evidence,
			Status:       base.Status,
			UnderlierID:  base.UnderlierID,
		},
	); err != nil {
		return err
	}

	return projectInstrument(ctx, q, p.ID)
}
