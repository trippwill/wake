// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

//go:embed publishing.sql
var publishingSQL string

func publishingStatement(name string) string {
	marker := "-- name: " + name + "\n"

	_, tail, found := strings.Cut(publishingSQL, marker)
	if !found {
		panic("unknown publishing statement")
	}

	statement, _, _ := strings.Cut(tail, "-- name:")

	return statement
}

// commit captures final accepted values before the transaction becomes visible.
func (s *Store) commit(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, publishingStatement("Init")); err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, publishingStatement("Dirty"))
	if err != nil {
		return err
	}

	type key struct{ kind, id string }

	keys := []key{}
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.kind, &k.id); err != nil {
			_ = rows.Close()
			return err
		}

		keys = append(keys, k)
	}

	err = rows.Err()
	_ = rows.Close()

	if err != nil {
		return err
	}

	q := s.queries.WithTx(tx)
	changed := false
	for _, k := range keys {
		resource, explanation, err := canonicalSnapshot(ctx, q, k.kind, k.id)
		if err != nil {
			return err
		}

		var (
			oldRevision             int64
			oldData, oldExplanation []byte
		)

		err = tx.QueryRowContext(ctx, publishingStatement("Current"), k.kind, k.id).
			Scan(&oldRevision, &oldData, &oldExplanation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Compare without revisions; repeated processing can advance internal revisions without a public change.
		priorInternalRevision := resource.Revision
		resource.Revision, explanation.Revision = oldRevision, oldRevision

		data, err := json.Marshal(resource)
		if err != nil {
			return err
		}

		why, err := json.Marshal(explanation)
		if err != nil {
			return err
		}

		if bytes.Equal(data, oldData) && bytes.Equal(why, oldExplanation) {
			continue
		}

		resource.Revision = max(oldRevision+1, priorInternalRevision)
		explanation.Revision = resource.Revision

		data, err = json.Marshal(resource)
		if err != nil {
			return err
		}

		why, err = json.Marshal(explanation)
		if err != nil {
			return err
		}

		revisionQuery := map[string]string{"accounts": "AccountRevision", "instruments": "InstrumentRevision", "listings": "ListingRevision", "activity": "ActivityRevision"}[k.kind]
		if _, err = tx.ExecContext(
			ctx,
			publishingStatement(revisionQuery),
			resource.Revision,
			k.id,
			resource.Revision,
		); err != nil {
			return err
		}

		if _, err = tx.ExecContext(
			ctx,
			publishingStatement("Save"),
			k.kind,
			k.id,
			resource.Revision,
			data,
			why,
		); err != nil {
			return err
		}

		changed = true
	}

	if changed {
		if _, err = tx.ExecContext(ctx, publishingStatement("Advance")); err != nil {
			return err
		}
	}

	if _, err = tx.ExecContext(ctx, publishingStatement("ClearDirty")); err != nil {
		return err
	}

	return tx.Commit()
}

func known(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func publicReference(r source.Reference) *application.PublishedReference {
	if r.IsZero() {
		return nil
	}

	return &application.PublishedReference{
		Authority: r.Authority(),
		Kind:      r.Kind(),
		Scope:     r.Scope(),
		Value:     r.Value(),
	}
}

func publicMoney(m financial.Money) *application.PublishedMoney {
	if m.Amount() == "" {
		return nil
	}

	return &application.PublishedMoney{Amount: m.Amount(), Currency: m.Currency().String()}
}

func publicPrice(p financial.Price) *application.PublishedMoney {
	if p.Amount() == "" {
		return nil
	}

	return &application.PublishedMoney{Amount: p.Amount(), Currency: p.Currency().String()}
}

func publicCharges(charges []source.Charge) []application.PublishedCharge {
	out := []application.PublishedCharge{}
	for _, c := range charges {
		out = append(
			out,
			application.PublishedCharge{Role: c.Field(), Scope: c.Scope(), Money: *publicMoney(c.Money())},
		)
	}

	return out
}

func publicDate(d financial.Date) *string {
	if d.IsZero() {
		return nil
	}

	return known(d.String())
}

func publicOption(o source.OptionTerms) *application.PublishedOption {
	if o.IsZero() {
		return nil
	}

	f := o.Fields()
	out := &application.PublishedOption{
		Right:         known(string(f.Right)),
		Expiry:        publicDate(f.Expiry),
		Strike:        publicPrice(f.Strike),
		Multiplier:    known(f.Multiplier.Value()),
		EffectiveFrom: publicDate(f.EffectiveFrom),
		Deliverables:  []application.PublishedDeliverable{},
	}
	for _, d := range f.Deliverables {
		out.Deliverables = append(
			out.Deliverables,
			application.PublishedDeliverable{
				Security: publicReference(d.Security()),
				Quantity: known(d.Quantity().Value()),
				Cash:     publicMoney(d.Cash()),
			},
		)
	}

	return out
}

func publicDebt(d source.DebtTerms) *application.PublishedDebt {
	if d.IsZero() {
		return nil
	}

	f := d.Fields()
	out := &application.PublishedDebt{
		Type:      known(string(f.Type)),
		Maturity:  publicDate(f.Maturity),
		IssueDate: publicDate(f.IssueDate),
		DatedDate: publicDate(f.DatedDate),
		Coupon:    known(f.Coupon.Value()),
	}
	if f.Currency != (financial.Currency{}) {
		out.Currency = known(f.Currency.String())
	}

	if f.PaymentsPerYear != 0 || f.Type == source.TreasuryBill {
		out.PaymentsPerYear = &f.PaymentsPerYear
	}

	return out
}

func canonicalSnapshot(
	ctx context.Context,
	q *sqlc.Queries,
	kind, id string,
) (application.CanonicalResource, application.Explanation, error) {
	out := application.CanonicalResource{ID: id, Kind: kind}
	why := application.Explanation{
		ID:          id,
		Kind:        kind,
		Corrections: []application.PublishedCorrection{},
		Decisions:   []application.PublishedDecision{},
	}
	switch kind {
	case "accounts":
		a, err := q.Account(ctx)
		if err != nil {
			return out, why, err
		}

		if a.ID != id {
			return out, why, ErrCorrupt
		}

		out.Revision = a.Revision
		out.Account = &application.PublishedAccount{Name: a.Name, Currency: known(a.Currency)}
		why.Selection = a.Rule
	case "listings":
		l, err := q.Listing(ctx, id)
		if err != nil {
			return out, why, err
		}

		out.Revision = l.Revision
		out.Listing = &application.PublishedListing{
			InstrumentID: l.InstrumentID,
			Venue:        known(l.Venue),
			Ticker:       known(l.Ticker),
			Currency:     l.Currency,
			FIGI:         known(l.Figi),
		}
		why.Selection = "accepted listing"
	case "instruments":
		i, err := q.Instrument(ctx, id)
		if err != nil {
			return out, why, err
		}

		value, err := instrumentValue(i)
		if err != nil {
			return out, why, err
		}

		reported := value.Reported()
		out.Revision = i.Revision
		out.Instrument = &application.PublishedInstrument{
			Family:          i.Kind,
			Name:            known(i.Name),
			Ticker:          known(i.Ticker),
			FIGI:            known(i.ReferenceFigi),
			ReferenceStatus: i.ReferenceStatus,
			UnderlierID:     known(i.UnderlierID.String),
			Identifiers:     []application.PublishedIdentifier{},
			Option:          publicOption(reported.Option()),
			Debt:            publicDebt(reported.Debt()),
		}
		for _, identifier := range reported.Identifiers() {
			out.Instrument.Identifiers = append(
				out.Instrument.Identifiers,
				application.PublishedIdentifier{Kind: string(identifier.Kind()), Value: identifier.Value()},
			)
		}

		why.Selection = i.Rule + "; " + i.ReferenceStatus
		why.Conflict = i.ReferenceStatus == "conflict"
	case "activity":
		row, err := q.Execution(ctx, id)
		if err != nil {
			return out, why, err
		}

		out.Revision = row.Revision

		state, err := activityState(row)
		if err != nil {
			return out, why, err
		}

		a := &application.PublishedActivity{
			Family:        row.Kind,
			AccountID:     row.AccountID,
			InstrumentID:  known(row.InstrumentID.String),
			ListingID:     known(row.ListingID.String),
			State:         string(state.Lifecycle()),
			Conflict:      state.NeedsReview(),
			Relationships: []application.PublishedRelationship{},
			Charges:       []application.PublishedCharge{},
		}
		if state.Lifecycle() == application.Superseded {
			a.State = "replaced"
		}

		why.Selection = row.Rule
		why.Conflict = a.Conflict
		switch row.Kind {
		case "trade":
			t, err := decodeTrade(row.Accepted)
			if err != nil {
				return out, why, err
			}

			f := t.Fields()
			a.Date, a.At, a.Order = f.TradeDate.String(), known(f.ExecutedAt.String()), publicReference(f.Order)
			a.TradeKind, a.Side, a.PositionEffect = known(
				string(f.Kind),
			), known(
				string(f.Side),
			), known(
				string(f.PositionEffect),
			)
			a.Quantity = &application.PublishedQuantity{Value: f.Quantity.Value(), Unit: t.QuantityUnit()}
			a.Price, a.PriceBasis, a.SettlementDate = publicPrice(
				f.Price,
			), known(
				t.PriceBasis(),
			), publicDate(
				f.SettlementDate,
			)
			a.Gross, a.Proceeds, a.Net, a.NetWithBillable, a.AccruedInterest = publicMoney(
				f.Gross,
			), publicMoney(
				f.Proceeds,
			), publicMoney(
				f.Net,
			), publicMoney(
				f.NetWithBillable,
			), publicMoney(
				f.AccruedInterest,
			)
			a.Charges = publicCharges(f.Charges)
			a.Option, a.Debt = publicOption(f.Instrument.Option()), publicDebt(f.Instrument.Debt())
		case "lifecycle":
			l, err := decodeLifecycle(row.Accepted)
			if err != nil {
				return out, why, err
			}

			f := l.Fields()
			a.Date, a.At, a.Order = f.Date.String(), known(f.At.String()), publicReference(f.Order)
			a.LifecycleKind = known(string(f.Kind))
			a.Quantity = &application.PublishedQuantity{Value: f.Quantity.Value(), Unit: "contracts"}
			a.Option = publicOption(f.Instrument.Option())
		case "conversion":
			c, err := decodeConversion(row.Accepted)
			if err != nil {
				return out, why, err
			}

			f := c.Fields()
			a.Date, a.At, a.Order = f.Date.String(), known(f.At.String()), publicReference(f.Order)

			a.Sent = &application.PublishedAmount{
				Amount:   known(f.Sent.Amount()),
				Currency: f.Sent.Currency().String(),
			}
			a.Received = &application.PublishedAmount{
				Amount:   known(f.Received.Amount()),
				Currency: f.Received.Currency().String(),
			}
			a.Rate = publicPrice(f.Rate)
			if f.RateBase != (financial.Currency{}) {
				a.RateBase = known(f.RateBase.String())
			}

			a.Gross, a.Proceeds, a.Net, a.NetWithBillable = publicMoney(
				f.Gross,
			), publicMoney(
				f.Proceeds,
			), publicMoney(
				f.Net,
			), publicMoney(
				f.NetWithBillable,
			)
			a.Charges = publicCharges(f.Charges)
		default:
			return out, why, ErrCorrupt
		}

		links, err := q.Relationships(ctx, id)
		if err != nil {
			return out, why, err
		}

		for _, link := range links {
			var (
				ref  source.Reference
				data refData
			)
			if json.Unmarshal([]byte(link.Target), &data) != nil {
				return out, why, ErrCorrupt
			}

			ref, err = data.reference()
			if err != nil {
				return out, why, err
			}

			a.Relationships = append(
				a.Relationships,
				application.PublishedRelationship{
					Kind:           link.Kind,
					TargetID:       known(link.TargetID),
					ReportedTarget: publicReference(ref),
				},
			)
		}

		corrections, err := q.RelatedSourceCorrections(ctx, id)
		if err != nil {
			return out, why, err
		}

		for _, c := range corrections {
			targets, err := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: c.AccountID, Reference: c.Target})
			if err != nil {
				return out, why, err
			}

			replacements, err := q.EventLinks(
				ctx,
				sqlc.EventLinksParams{AccountID: c.AccountID, Reference: c.Replacement},
			)
			if err != nil {
				return out, why, err
			}

			relevant := false
			for _, target := range targets {
				if target == id {
					relevant = true

					for _, replacement := range replacements {
						a.Relationships = append(
							a.Relationships,
							application.PublishedRelationship{Kind: "replacement", TargetID: known(replacement)},
						)
					}
				}
			}

			for _, replacement := range replacements {
				if replacement == id {
					relevant = true

					for _, target := range targets {
						a.Relationships = append(
							a.Relationships,
							application.PublishedRelationship{Kind: "replaces", TargetID: known(target)},
						)
					}
				}
			}

			if relevant {
				why.Corrections = append(
					why.Corrections,
					application.PublishedCorrection{
						Kind:           c.Kind,
						Status:         c.Status,
						TargetIDs:      append([]string{}, targets...),
						ReplacementIDs: append([]string{}, replacements...),
					},
				)
			}
		}

		a.Relationships, err = canonicalSet(a.Relationships)
		if err != nil {
			return out, why, err
		}

		why.Corrections, err = canonicalSet(why.Corrections)
		if err != nil {
			return out, why, err
		}

		out.Activity = a
	default:
		return out, why, ErrCorrupt
	}

	if kind == "activity" || kind == "instruments" {
		decisionKind := "activity"
		if kind == "instruments" {
			decisionKind = "instrument"
		}

		decisions, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: decisionKind, TargetID: id})
		if err != nil {
			return out, why, err
		}

		active := activeDecisions(decisions)

		for _, d := range active {
			why.Decisions = append(
				why.Decisions,
				application.PublishedDecision{ID: d.ID, Scope: d.Scope, Action: d.Action},
			)
		}

		sort.Slice(why.Decisions, func(i, j int) bool { return why.Decisions[i].Scope < why.Decisions[j].Scope })
	}

	return out, why, nil
}

// Repeated source instructions retain private evidence without duplicating the public meaning.
func canonicalSet[T any](values []T) ([]T, error) {
	unique := map[string]T{}
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}

		unique[string(data)] = value
	}

	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	out := make([]T, 0, len(keys))
	for _, key := range keys {
		out = append(out, unique[key])
	}

	return out, nil
}

type publicationState struct {
	Warehouse string
	Version   int64
}

func publishingState(ctx context.Context, tx *sql.Tx) (publicationState, error) {
	var state publicationState

	err := tx.QueryRowContext(ctx, publishingStatement("State")).Scan(&state.Warehouse, &state.Version)

	return state, err
}

func (s *Store) published(
	ctx context.Context,
	kind, id string,
) (application.CanonicalResource, application.Explanation, error) {
	var (
		out application.CanonicalResource
		why application.Explanation
	)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, why, err
	}

	defer func() { _ = tx.Rollback() }()

	var (
		revision          int64
		data, explanation []byte
	)

	err = tx.QueryRowContext(ctx, publishingStatement("Current"), kind, id).Scan(&revision, &data, &explanation)
	if errors.Is(err, sql.ErrNoRows) {
		return out, why, application.ErrNotFound
	}

	if err != nil {
		return out, why, err
	}

	if json.Unmarshal(data, &out) != nil || json.Unmarshal(explanation, &why) != nil {
		return out, why, ErrCorrupt
	}

	return out, why, nil
}

func (s *Store) Resource(ctx context.Context, kind, id string) (application.CanonicalResource, error) {
	out, _, err := s.published(ctx, kind, id)
	return out, err
}

func (s *Store) Explanation(
	ctx context.Context,
	kind, id string,
	revision int64,
) (application.Explanation, error) {
	_, why, err := s.published(ctx, kind, id)
	if err != nil {
		return why, err
	}

	if revision < 1 {
		return application.Explanation{}, application.ErrInvalid
	}

	if why.Revision != revision {
		return application.Explanation{}, application.ErrStale
	}

	return why, nil
}
