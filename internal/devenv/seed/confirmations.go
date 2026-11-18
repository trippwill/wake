// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package seed prepares synthetic data through Wake's financial workflows.
package seed

import (
	"context"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/provider"
)

type Error string

func (e Error) Error() string { return string(e) }

const ErrDiscovery Error = "synthetic discovery did not produce exactly one candidate"

func SeedConfirmations(ctx context.Context, workflow *application.Workflow, now *time.Time) (string, error) {
	advance := func() error {
		for range 2 {
			*now = now.Add(15 * time.Second)
			if err := workflow.RunOnce(ctx, *now); err != nil {
				return err
			}
		}

		return nil
	}

	settings, err := provider.NewSettings(map[string]string{"query_id": "123"})
	if err != nil {
		return "", err
	}

	secret := credential.NewSecret([]byte("api-synthetic-token"))
	defer secret.Clear()

	connection, err := workflow.CreateConnection(
		ctx,
		"API acceptance",
		"ibkr-flex",
		settings,
		map[string]*credential.Secret{"token": secret},
	)
	if err != nil {
		return "", err
	}

	if err = advance(); err != nil {
		return "", err
	}

	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)

	sourceDetail, err := workflow.SourceDetail(ctx, connection, query)
	if err != nil {
		return "", err
	}

	if len(sourceDetail.Connection.Candidates) != 1 {
		return "", ErrDiscovery
	}

	candidate := sourceDetail.Connection.Candidates[0]

	details, err := application.NewAccountDetails("API acceptance account", "")
	if err != nil {
		return "", err
	}

	if _, err = workflow.Confirm(
		ctx,
		connection,
		candidate.Artifact,
		candidate.Candidate.Reference(),
		details,
	); err != nil {
		return "", err
	}

	if err = advance(); err != nil {
		return "", err
	}

	return connection, nil
}
