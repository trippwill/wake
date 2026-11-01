// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package reference

import (
	"slices"

	"go.trippwill.dev/wake/source"
)

type Allocation string

const (
	VenueAllocation      Allocation = "venue"
	CompositeAllocation  Allocation = "composite"
	ShareClassAllocation Allocation = "share_class"
	InstrumentAllocation Allocation = "instrument"
)

type Reason string

const (
	MissingIdentifier   Reason = "missing_identifier"
	MissingParent       Reason = "missing_parent"
	CompetingIdentity   Reason = "competing_identity"
	ConflictingParents  Reason = "conflicting_parents"
	IncompatibleSubject Reason = "incompatible_subject"
	IncompleteMetadata  Reason = "incomplete_metadata"
	OperatorConflict    Reason = "operator_conflict"
	SourceConflict      Reason = "source_conflict"
)

type Selection struct {
	FIGI         string
	Allocation   Allocation
	Family       source.InstrumentKind
	Name, Ticker string
	Reason       Reason
}

// LookupJob is shared by acquisition and retained correction-candidate checks.
// Allocation comes from the identifier's scheme, never its characters or labels.
func LookupJob(id source.Identifier) (Job, bool) {
	kind := ""
	switch id.Kind() {
	case source.VenueFIGI, source.InstrumentFIGI, source.UnclassifiedFIGI:
		kind = "ID_BB_GLOBAL"
	case source.CompositeFIGI:
		kind = "COMPOSITE_ID_BB_GLOBAL"
	case source.ShareClassFIGI:
		kind = "ID_BB_GLOBAL_SHARE_CLASS_LEVEL"
	case source.ISIN:
		kind = "ID_ISIN"
	case source.CUSIP:
		kind = "ID_CUSIP"
	case source.SEDOL:
		kind = "ID_SEDOL"
	case source.OCC:
		kind = "OCC_SYMBOL"
	default:
		return Job{}, false
	}

	return Job{IDType: kind, IDValue: id.Value()}, true
}

func Matches(instrument source.Instrument, j Job) bool {
	return slices.ContainsFunc(
		instrument.Identifiers(),
		func(id source.Identifier) bool { candidate, ok := LookupJob(id); return ok && candidate == j },
	)
}

func optionRecord(r Record) bool {
	return r.SecurityType == "Equity Option" || r.SecurityType2 == "Option"
}

// SelectIdentity uses explicit parent relationships. Missing metadata is not a
// competing identity, while contradictory explicit parents cannot be outvoted.
func SelectIdentity(kind source.InstrumentKind, records []Record) Selection {
	out := Selection{Family: kind}
	composites := map[string]string{}
	shares := map[string]string{}
	for _, r := range records {
		if r.FIGI != "" && r.Composite != "" {
			if old := composites[r.FIGI]; old != "" && old != r.Composite {
				out.Reason = ConflictingParents
				return out
			}

			composites[r.FIGI] = r.Composite
		}

		if r.Composite != "" && r.ShareClass != "" {
			if old := shares[r.Composite]; old != "" && old != r.ShareClass {
				out.Reason = ConflictingParents
				return out
			}

			shares[r.Composite] = r.ShareClass
		}
	}

	names, tickers := map[string]bool{}, map[string]bool{}
	incomplete := false
	for _, r := range records {
		if r.FIGI == "" {
			incomplete = true
			continue
		}

		option := optionRecord(r)
		if kind == source.UnknownInstrument && r.Market == "Equity" && !option &&
			(r.SecurityType == "Common Stock" || r.SecurityType2 == "Common Stock") {
			out.Family = source.Equity
		}

		incompatible := false
		switch kind {
		case source.Option:
			incompatible = r.Market != "" && r.Market != "Equity" ||
				r.SecurityType != "" && r.SecurityType != "Equity Option" ||
				r.SecurityType2 != "" && r.SecurityType2 != "Option" && r.SecurityType2 != "Equity"
		case source.Equity, source.Fund, source.UnknownInstrument:
			incompatible = option || r.Market != "" && r.Market != "Equity"
		case source.Debt:
			incompatible = r.Market != "" && r.Market != "Govt" && r.Market != "Corp" && r.Market != "M-Mkt"
		default:
			incompatible = true
		}

		if incompatible {
			out.FIGI = ""
			out.Reason = IncompatibleSubject

			return out
		}

		identity := ""
		switch kind {
		case source.Option:
			identity = r.Composite
			out.Allocation = CompositeAllocation
		case source.Debt:
			identity = r.FIGI
			out.Allocation = InstrumentAllocation
		default:
			identity = r.ShareClass
			if identity == "" {
				identity = shares[r.Composite]
			}

			out.Allocation = ShareClassAllocation
		}

		if identity == "" {
			incomplete = true
			continue
		}

		if out.FIGI != "" && out.FIGI != identity {
			out.FIGI = ""
			out.Reason = CompetingIdentity

			return out
		}

		out.FIGI = identity

		if r.Name != "" {
			names[r.Name] = true
		} else {
			incomplete = true
		}

		if r.Ticker != "" {
			tickers[r.Ticker] = true
		}
	}

	if out.FIGI == "" {
		out.Reason = MissingParent
		return out
	}

	if len(names) == 1 {
		for name := range names {
			out.Name = name
		}
	}

	if len(tickers) == 1 {
		for ticker := range tickers {
			out.Ticker = ticker
		}
	}

	if incomplete {
		out.Reason = IncompleteMetadata
	}

	return out
}

// MissingComposites identifies only unresolved edges, not all listed venues.
func MissingComposites(kind source.InstrumentKind, records []Record) []Job {
	if kind != source.Equity && kind != source.Fund && kind != source.UnknownInstrument {
		return nil
	}

	known := map[string]bool{}
	for _, r := range records {
		if r.ShareClass != "" {
			known[r.Composite] = true
		}
	}

	missing := map[string]bool{}
	for _, r := range records {
		if r.Composite != "" && !known[r.Composite] {
			missing[r.Composite] = true
		}
	}

	keys := []string{}
	for key := range missing {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	out := []Job{}
	for _, key := range keys {
		out = append(out, Job{IDType: "COMPOSITE_ID_BB_GLOBAL", IDValue: key})
	}

	return out
}
