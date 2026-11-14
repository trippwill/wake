// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/transport/financialapi"
)

func validActivityDate(value string) bool {
	_, err := time.Parse(time.DateOnly, value)
	return len(value) == len(time.DateOnly) && err == nil
}

func queryActivity(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 0 {
		return usageError("activity query takes no arguments")
	}

	account, from, to := c.String("account"), c.String("from"), c.String("to")
	if c.IsSet("account") && !validFinancialID(account) {
		return usageError("--account must be a valid Wake account ID")
	}

	if c.IsSet("from") && !validActivityDate(from) || c.IsSet("to") && !validActivityDate(to) {
		return usageError("--from and --to require YYYY-MM-DD dates")
	}

	if from != "" && to != "" && from > to {
		return usageError("--from must be on or before --to")
	}

	family, state := financialapi.ActivityFamily(
		c.String("family"),
	), financialapi.ActivityState(
		c.String("state"),
	)
	if c.IsSet("family") && !family.Valid() {
		return usageError("--family must be trade, lifecycle, conversion, security-transfer or cash")
	}

	if state != "all" && !state.Valid() {
		return usageError("--state must be accepted, cancelled, replaced or all")
	}

	offset, limit := c.Int("offset"), c.Int("limit")
	if offset < 0 || limit < 1 || limit > 1000 {
		return usageError("--offset must be nonnegative and --limit must be between 1 and 1000")
	}

	collection, resources, err := readFinancialCollection(ctx, c, "activity")
	if err != nil {
		return err
	}

	matches := resources[:0]
	for _, r := range resources {
		a := r.Activity
		if a == nil || a.AccountID == "" || !validActivityDate(a.Date) || !a.Family.Valid() || !a.State.Valid() {
			return failure("invalid activity in Wake financial snapshot")
		}

		if (account == "" || a.AccountID == account) && (from == "" || a.Date >= from) &&
			(to == "" || a.Date <= to) && (family == "" || a.Family == family) && (state == "all" || a.State == state) {
			matches = append(matches, r)
		}
	}

	slices.SortFunc(matches, func(a, b financialResource) int {
		if order := cmp.Compare(b.Activity.Date, a.Activity.Date); order != 0 {
			return order
		}

		return cmp.Compare(a.ID, b.ID)
	})

	start := min(offset, len(matches))
	end := start + min(limit, len(matches)-start)
	collection.Resources = make([]json.RawMessage, 0, end-start)
	for _, r := range matches[start:end] {
		collection.Resources = append(collection.Resources, r.raw)
	}

	var next *int
	if end < len(matches) {
		next = &end
	}

	return json.NewEncoder(c.Writer).Encode(struct {
		financialCollection
		NextOffset *int `json:"next_offset"`
	}{collection, next})
}
