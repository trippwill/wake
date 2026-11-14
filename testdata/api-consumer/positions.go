// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

type positionsPublication struct {
	WarehouseID string `json:"warehouse_id"`
	AccountID   string `json:"account_id"`
	Version     string `json:"version"`
}

type positionsView struct {
	WarehouseID string            `json:"warehouse_id"`
	AccountID   string            `json:"account_id"`
	Version     string            `json:"version"`
	Status      string            `json:"status"`
	AsOf        json.RawMessage   `json:"as_of"`
	Reason      *string           `json:"reason"`
	Positions   []json.RawMessage `json:"positions"`
}

func (c *client) readPositions(ctx context.Context, account string, previous positionsView) (positionsView, error) {
	data, etag, unchanged, err := c.request(ctx, "/v1/accounts/"+url.PathEscape(account)+"/positions", previous.Version)
	if err != nil {
		return previous, err
	}
	if unchanged {
		return previous, nil
	}
	if err = validateContract("PositionsView", data); err != nil {
		return previous, err
	}
	var next positionsView
	if err = decode(data, &next); err != nil {
		return previous, err
	}
	if next.AccountID != account || next.Version == "" || strings.ContainsAny(next.Version, "\"\r\n") || etag != `"`+next.Version+`"` {
		return previous, errors.New("protocol: invalid positions identity or version")
	}
	available := next.Status == "available"
	if available && (next.Positions == nil || next.Reason != nil || string(next.AsOf) == "null") || !available && (next.Positions != nil || next.Reason == nil) {
		return previous, errors.New("protocol: inconsistent positions availability")
	}
	var values any
	if err = json.Unmarshal(data, &values); err != nil {
		return previous, err
	}
	if err = validateScalars(values); err != nil {
		return previous, err
	}
	// Resolve current identities through the public lookup boundary. No snapshot
	// or private store access is needed to consume this separate view.
	ids := map[string]string{"accounts/" + url.PathEscape(account): "accounts"}
	for _, row := range next.Positions {
		var p struct {
			InstrumentID string                       `json:"instrument_id"`
			Quantity     struct{ Value, Unit string } `json:"quantity"`
			Side         string                       `json:"side"`
		}
		if err = json.Unmarshal(row, &p); err != nil {
			return previous, err
		}
		switch p.Quantity.Unit {
		case "shares", "contracts", "nominal", "units":
		default:
			return previous, errors.New("protocol: invalid position quantity unit")
		}
		if strings.Trim(p.Quantity.Value, "-0.") == "" || (p.Side == "short") != strings.HasPrefix(p.Quantity.Value, "-") {
			return previous, errors.New("protocol: invalid position quantity or side")
		}
		ids["instruments/"+url.PathEscape(p.InstrumentID)] = "instruments"
	}
	for path, kind := range ids {
		body, _, _, e := c.request(ctx, "/v1/"+path, "")
		if e != nil {
			return previous, e
		}
		meta, e := metadata(body)
		if e != nil || meta.Kind != kind {
			return previous, errors.New("protocol: unresolved position identity")
		}
	}
	return next, nil
}

func (c *client) positionsMode(ctx context.Context, account string, follow bool, delay time.Duration, stdout io.Writer) error {
	current := positionsView{}
	synchronize := func() error {
		next, err := c.readPositions(ctx, account, current)
		if err != nil {
			return err
		}
		// Prove conditional reads for every accepted version before reporting success.
		if _, err = c.readPositions(ctx, account, next); err != nil {
			return err
		}
		if err = json.NewEncoder(stdout).Encode(next); err != nil {
			return err
		}
		current = next
		return nil
	}
	if !follow {
		return synchronize()
	}
	var err error
	current, err = c.readPositions(ctx, account, current)
	var updating historyUpdateError
	if err != nil && !errors.As(err, &updating) {
		return err
	}
	announced := false
	c.positionUpdate = func(p positionsPublication) error {
		if p.AccountID != account || announced && p.WarehouseID == current.WarehouseID && p.Version == current.Version {
			return nil
		}
		err := synchronize()
		announced = err == nil
		return err
	}
	fmt.Fprintln(c.stderr, "Following reported positions events")
	return c.follow(ctx, delay, func(publication) error { return nil })
}
