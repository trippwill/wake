// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import "go.trippwill.dev/wake/source"

type PositionTime struct {
	Date  string                   `json:"date"`
	At    *string                  `json:"at"`
	Basis source.PositionTimeBasis `json:"basis,omitempty"`
}

type PublishedPosition struct {
	InstrumentID  string                  `json:"instrument_id"`
	Quantity      PublishedQuantity       `json:"quantity"`
	Side          string                  `json:"side"`
	UnitCostBasis *PublishedUnitCostBasis `json:"unit_cost_basis,omitempty"`
	Option        *PublishedOption        `json:"option"`
	Debt          *PublishedDebt          `json:"debt"`
}

type PublishedUnitCostBasis struct {
	PublishedMoney
	Quote string `json:"quote,omitempty"`
}

// PositionsView replaces one account's complete current reported position set.
// Unavailable rows are null; an available empty set contains an empty array.
type PositionsView struct {
	WarehouseID string              `json:"warehouse_id"`
	AccountID   string              `json:"account_id"`
	Version     string              `json:"version"`
	Status      string              `json:"status"`
	AsOf        *PositionTime       `json:"as_of"`
	Reason      *string             `json:"reason"`
	Positions   []PublishedPosition `json:"positions"`
}

type PositionsRead struct {
	View      PositionsView
	Unchanged bool
}

type PositionsPublication struct {
	WarehouseID string `json:"warehouse_id"`
	AccountID   string `json:"account_id"`
	Version     string `json:"version"`
}

type PositionListItem struct {
	Key, Name, Ticker, Kind string
	Position                PublishedPosition
}

type PositionsList struct {
	Account Account
	PageInfo
	Version, Status string
	AsOf            *PositionTime
	Reason          *string
	Items           []PositionListItem
}
