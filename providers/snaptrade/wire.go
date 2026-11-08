// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const (
	fidelity  = "FIDELITY"
	ibkr      = "INTERACTIVE-BROKERS-FLEX"
	pageLimit = 1000
)

func validateJSON(ctx context.Context, body []byte, secrets ...[]byte) error {
	if !utf8.Valid(body) {
		return provider.ErrMalformed
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	if err := jsonValue(ctx, decoder, 0, secrets); err != nil {
		return err
	}

	if _, err := decoder.Token(); err != io.EOF {
		return provider.ErrMalformed
	}

	return ctx.Err()
}

func jsonValue(ctx context.Context, decoder *json.Decoder, depth int, secrets [][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if depth > 64 {
		return provider.ErrLimit
	}

	token, err := decoder.Token()
	if err != nil {
		return provider.ErrMalformed
	}

	switch value := token.(type) {
	case string:
		return checkString(value, secrets)
	case json.Delim:
		if value != '{' && value != '[' {
			return provider.ErrMalformed
		}

		var keys map[string]bool
		if value == '{' {
			keys = map[string]bool{}
		}

		for decoder.More() {
			if value == '{' {
				key, err := decoder.Token()
				name, ok := key.(string)
				folded := strings.ToLower(name)
				if err != nil || !ok || keys[folded] || checkString(name, secrets) != nil {
					return provider.ErrMalformed
				}

				keys[folded] = true
			}

			if err := jsonValue(ctx, decoder, depth+1, secrets); err != nil {
				return err
			}
		}

		if _, err = decoder.Token(); err != nil {
			return provider.ErrMalformed
		}
	}

	return nil
}

func checkString(value string, secrets [][]byte) error {
	for _, secret := range secrets {
		if len(secret) > 0 && bytes.Contains([]byte(value), secret) {
			return provider.ErrMalformed
		}
	}

	return nil
}

type authorization struct {
	ID        string `json:"id"`
	Disabled  *bool  `json:"disabled"`
	Brokerage struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"brokerage"`
}

type accountWire struct {
	ID                 string `json:"id"`
	Connection         string `json:"connection_id"`
	Institution        string `json:"institution_id"`
	InstitutionAccount string `json:"institution_account_id"`
	Name               string `json:"display_name"`
	Kind               string `json:"kind"`
}

type accountScope struct {
	Broker             string
	Connection         string
	Institution        string
	InstitutionAccount string
}

func (scope accountScope) reference(id string) (source.Reference, error) {
	payload, err := json.Marshal(scope)
	if err != nil {
		return source.Reference{}, provider.ErrMalformed
	}

	return source.NewReference("snaptrade", "account", string(payload), id)
}

func sourceScope(ref source.Reference) (accountScope, error) {
	var scope accountScope
	if ref.Authority() != "snaptrade" || ref.Kind() != "account" || !pathID(ref.Value()) ||
		json.Unmarshal([]byte(ref.Scope()), &scope) != nil ||
		(scope.Broker != fidelity && scope.Broker != ibkr) {
		return accountScope{}, provider.ErrInvalid
	}

	return scope, nil
}

func pathID(id string) bool {
	return id != "" && len(id) <= 128 &&
		strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == ""
}

func displayName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || strings.ContainsFunc(name, unicode.IsControl) {
		return fallback
	}

	return name
}

func authorizationScopes(body []byte) (map[string]accountScope, error) {
	var rows []authorization
	if json.Unmarshal(body, &rows) != nil || rows == nil {
		return nil, provider.ErrMalformed
	}

	scopes := make(map[string]accountScope)
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !pathID(row.ID) || seen[row.ID] || row.Disabled == nil {
			return nil, provider.ErrMalformed
		}

		seen[row.ID] = true
		broker := row.Brokerage.Slug
		if *row.Disabled || (broker != fidelity && broker != ibkr) {
			continue
		}

		if !pathID(row.Brokerage.ID) {
			return nil, provider.ErrMalformed
		}

		scopes[row.ID] = accountScope{Broker: broker, Connection: row.ID, Institution: row.Brokerage.ID}
	}

	return scopes, nil
}

func candidateAccounts(body []byte, scopes map[string]accountScope) ([]provider.Candidate, error) {
	var accounts struct {
		Results []accountWire `json:"results"`
	}

	if json.Unmarshal(body, &accounts) != nil || accounts.Results == nil {
		return nil, provider.ErrMalformed
	}

	var candidates []provider.Candidate

	seen := make(map[string]bool, len(accounts.Results))
	for _, account := range accounts.Results {
		scope, ok := scopes[account.Connection]
		if !ok || account.Kind != "investment" || scope.Institution != account.Institution {
			continue
		}

		if !pathID(account.ID) || seen[account.ID] {
			return nil, provider.ErrMalformed
		}

		seen[account.ID] = true
		scope.InstitutionAccount = account.InstitutionAccount

		ref, err := scope.reference(account.ID)
		if err != nil {
			return nil, provider.ErrMalformed
		}

		name := "Fidelity US"
		if scope.Broker == ibkr {
			name = "Interactive Brokers"
		}

		candidate, err := provider.NewCandidate(
			ref,
			name+" · "+displayName(account.Name, account.ID),
			financial.Currency{},
		)
		if err != nil {
			return nil, err
		}

		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

type activityPage struct {
	Data       []json.RawMessage `json:"data"`
	Pagination struct {
		Offset *int `json:"offset"`
		Limit  *int `json:"limit"`
		Total  *int `json:"total"`
	} `json:"pagination"`
}

func parsePage(body []byte) (activityPage, []string, error) {
	var page activityPage
	if json.Unmarshal(body, &page) != nil || page.Data == nil {
		return page, nil, provider.ErrMalformed
	}

	p := page.Pagination
	if p.Offset == nil || p.Limit == nil || p.Total == nil || *p.Offset < 0 || *p.Limit < 1 ||
		*p.Limit > pageLimit || *p.Total < *p.Offset || len(page.Data) > *p.Limit ||
		len(page.Data) > *p.Total-*p.Offset || (len(page.Data) == 0 && *p.Offset < *p.Total) {
		return page, nil, provider.ErrMalformed
	}

	ids := make([]string, len(page.Data))
	seen := make(map[string]bool, len(page.Data))
	for i, raw := range page.Data {
		var row struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &row) != nil || !pathID(row.ID) {
			continue
		}

		if seen[row.ID] {
			return page, nil, provider.ErrMalformed
		}

		seen[row.ID] = true
		ids[i] = row.ID
	}

	return page, ids, nil
}
