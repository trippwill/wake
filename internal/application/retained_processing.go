// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"slices"

	"go.trippwill.dev/wake/source"
)

type RetainedClaim struct {
	ID, Location, Kind string
	Ordinal            int64
	Observation        source.ActivityObservation
	Correction         source.Correction
	Positions          source.PositionSetObservation
	PositionAccount    source.Reference
	PositionInstrument source.Instrument
}

type RetainedProcessingStore interface {
	UpdateRetainedClaims(context.Context, string, string, func(RetainedProcessingWriter) error) error
}

type RetainedProcessingWriter interface {
	RetainedSource(context.Context) (connection, provider string, account Account, err error)
	RetainedClaims(context.Context) ([]RetainedClaim, error)
	RecordClaim(context.Context, RetainedClaim) (bool, error)
	SavePositionSet(context.Context, string, RetainedClaim) error
	BindPositionInstrument(context.Context, string, RetainedClaim) (string, error)
	SaveSourceCorrection(context.Context, string, source.Correction) error
	SaveActivityObservation(context.Context, string, RetainedClaim, string, []source.Reference) error
	ProcessingReceipt(context.Context) (reinterpretingPositions bool, err error)
	BindPositionUnderliers(context.Context, []string) error
	SeedActivityBases(context.Context) error
	ActivityLinks
	ReconcileSourceCorrections(context.Context, []string) (map[string]ActivityState, error)
	ClaimActivityIDs(context.Context) ([]string, error)
	ActivityState(context.Context, string) (ActivityState, error)
	BindActivityInstrument(context.Context, string) error
	ProjectArtifactRoots(context.Context) error
	ProjectRetainedActivity(context.Context, string, ActivityState) error
	RetirePositionInterpretations(context.Context) error
}

// ProcessRetained accepts claims under the current confirmed authority and
// commits their projections and receipt together. Normalization is independent.
func ProcessRetained(ctx context.Context, store RetainedProcessingStore, id, version string) error {
	return store.UpdateRetainedClaims(ctx, id, version, func(writer RetainedProcessingWriter) error {
		connection, provider, account, err := writer.RetainedSource(ctx)
		if err != nil {
			return err
		}

		records, err := writer.RetainedClaims(ctx)
		if err != nil {
			return err
		}

		var (
			changed, hasPositions bool
			parents               []string
		)

		seenParents := map[string]bool{}
		for _, record := range records {
			hasPositions = hasPositions || record.Kind == "positions" || record.Kind == "position-instrument"

			added, err := writer.RecordClaim(ctx, record)
			if err != nil {
				return err
			}

			if !added {
				continue
			}

			switch record.Kind {
			case "positions":
				if account.AuthorizesSource(connection, provider, record.Positions.Fields().Account) {
					if err := writer.SavePositionSet(ctx, account.ID(), record); err != nil {
						return err
					}
				}
			case "position-instrument":
				if account.AuthorizesSource(connection, provider, record.PositionAccount) {
					root, err := writer.BindPositionInstrument(ctx, account.ID(), record)
					if err != nil {
						return err
					}

					if !seenParents[root] {
						parents = append(parents, root)
						seenParents[root] = true
					}

					changed = true
				}
			case "activity":
				outcome := "unselected"

				var refs []source.Reference

				fields := record.Observation.Fields()
				if account.AuthorizesSource(connection, provider, fields.Account) {
					outcome = "identity-review"
					if !fields.Event.IsZero() {
						outcome = "incomplete"
					}

					refs = activityReferences(record.Observation)
				}

				if err := writer.SaveActivityObservation(ctx, account.ID(), record, outcome, refs); err != nil {
					return err
				}

				changed = true
			case "correction":
				if account.AuthorizesSource(connection, provider, record.Correction.Fields().Account) {
					if err := writer.SaveSourceCorrection(ctx, account.ID(), record.Correction); err != nil {
						return err
					}
				}

				changed = true
			}
		}

		reinterpreting, err := writer.ProcessingReceipt(ctx)
		if err != nil {
			return err
		}

		if err := writer.BindPositionUnderliers(ctx, parents); err != nil {
			return err
		}

		if changed && account.ID() != "" {
			if err := projectRetainedArtifact(ctx, writer, account.ID()); err != nil {
				return err
			}
		} else if reinterpreting || hasPositions {
			if err := writer.ProjectArtifactRoots(ctx); err != nil {
				return err
			}
		}

		return writer.RetirePositionInterpretations(ctx)
	})
}

func projectRetainedArtifact(ctx context.Context, writer RetainedProcessingWriter, accountID string) error {
	if err := writer.SeedActivityBases(ctx); err != nil {
		return err
	}

	linked, err := LinkActivityObservations(ctx, writer, accountID)
	if err != nil {
		return err
	}

	admitted, err := writer.ClaimActivityIDs(ctx)
	if err != nil {
		return err
	}

	admitted = append(admitted, linked...)

	states, err := writer.ReconcileSourceCorrections(ctx, admitted)
	if err != nil {
		return err
	}

	affected := make(map[string]bool, len(admitted))
	for _, id := range admitted {
		affected[id] = true
	}

	for id, state := range states {
		if affected[id] {
			continue
		}

		current, err := writer.ActivityState(ctx, id)
		if err != nil {
			return err
		}

		if current.Lifecycle() != state.Lifecycle() ||
			current.ReviewReasons()&AmbiguousCorrection != state.ReviewReasons()&AmbiguousCorrection {
			affected[id] = true
		}
	}

	ordered := make([]string, 0, len(affected))
	for id := range affected {
		ordered = append(ordered, id)
	}

	slices.Sort(ordered)

	for _, id := range ordered {
		if err := writer.BindActivityInstrument(ctx, id); err != nil {
			return err
		}
	}

	if err := writer.ProjectArtifactRoots(ctx); err != nil {
		return err
	}

	for _, id := range ordered {
		if err := writer.ProjectRetainedActivity(ctx, id, states[id]); err != nil {
			return err
		}
	}

	return nil
}
