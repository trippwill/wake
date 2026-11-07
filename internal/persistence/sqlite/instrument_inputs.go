// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"

	"go.trippwill.dev/wake/source"
)

// Current child assertions follow the automatic source relationship. Their
// eligibility does not depend on which historical appearance retained support.
func (p *rootPhase) positionUnderlierClaims(ctx context.Context, id string) ([]rootClaim, error) {
	rows, err := p.q.PositionUnderlierInputs(ctx, id)
	if err != nil {
		return nil, err
	}

	claims := []rootClaim{}
	references := map[string]map[string]source.Reference{}
	for _, row := range rows {
		reported, e := reportedRootClaim(row.Kind, row.Data, row.Location, "underlier")
		if e != nil {
			return nil, e
		}

		if reported.Kind() == "" {
			continue
		}

		refs, ok := references[row.InstrumentID]
		if !ok {
			refs = map[string]source.Reference{}

			inputs, e := p.q.RootCalculationInputs(ctx, row.InstrumentID)
			if e != nil {
				return nil, e
			}

			for _, input := range inputs {
				if input.Role != "subject" {
					continue
				}

				value, e := reportedRootClaim(input.Kind, input.Data, input.Location, input.Role)
				if e != nil {
					return nil, e
				}

				ref := value.Option().Fields().Underlier.Reference()
				if !ref.IsZero() {
					refs[referenceKey(ref)] = ref
				}
			}

			references[row.InstrumentID] = refs
		}

		selected := ""
		if row.UnderlierID != "" {
			selected, e = canonicalID(ctx, p.q, "instruments", row.UnderlierID)
			if e != nil {
				return nil, e
			}
		}

		matches := false
		ref := reported.Reference()
		if !ref.IsZero() {
			child, e := p.q.InstrumentBySource(ctx, referenceKey(ref))
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}

			matches = e == nil && child.ID == id || len(refs) == 1 && selected == id
		} else {
			matches = selected == id
			if len(refs) == 1 {
				for key := range refs {
					child, e := p.q.InstrumentBySource(ctx, key)
					if e != nil && !errors.Is(e, sql.ErrNoRows) {
						return nil, e
					}

					matches = matches || e == nil && child.ID == id
				}
			}
		}

		if matches {
			claims = append(
				claims,
				rootClaim{
					id:       row.ClaimID,
					ordinal:  row.Ordinal,
					role:     "underlier",
					sequence: row.Sequence,
					reported: reported,
				},
			)
		}
	}

	return claims, nil
}
