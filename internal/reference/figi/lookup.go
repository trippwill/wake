// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package figi classifies OpenFIGI mapping records and prepares identifier lookups.
package figi

import (
	"encoding/json"
	"slices"

	"go.trippwill.dev/wake/source"
)

type Job struct {
	IDType  string `json:"idType"`
	IDValue string `json:"idValue"`
}

func (j Job) Key() string { b, _ := json.Marshal(j); return string(b) }

type Record struct {
	FIGI          string `json:"figi"`
	Name          string `json:"name"`
	Ticker        string `json:"ticker"`
	ShareClass    string `json:"shareClassFIGI"`
	Composite     string `json:"compositeFIGI"`
	Exchange      string `json:"exchCode"`
	Market        string `json:"marketSector"`
	SecurityType  string `json:"securityType"`
	SecurityType2 string `json:"securityType2"`
}

// InstrumentJob chooses the highest priority reported lookup identifier.
func InstrumentJob(instrument source.Instrument) (Job, bool) {
	identifiers := instrument.Identifiers()
	for _, kind := range []source.IdentifierKind{source.VenueFIGI, source.InstrumentFIGI, source.CompositeFIGI, source.ShareClassFIGI, source.UnclassifiedFIGI, source.ISIN, source.CUSIP, source.SEDOL, source.OCC} {
		for _, id := range identifiers {
			if id.Kind() == kind {
				return LookupJob(id)
			}
		}
	}

	return Job{}, false
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
