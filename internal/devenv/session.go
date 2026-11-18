// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devenv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/daemon"
	fixtureseed "go.trippwill.dev/wake/internal/devenv/seed"
	"go.trippwill.dev/wake/internal/devfixture"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type fixtureTransport struct {
	http.RoundTripper
	origin string
}

func (t fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host != t.origin {
		return nil, errors.New("unexpected synthetic upstream")
	}

	return t.RoundTripper.RoundTrip(req)
}

func seed(ctx context.Context, cfg config.Config, reg provider.Registration, now *time.Time) (result error) {
	if err := credential.CreateMasterKey(cfg.Credentials.KeyPath); err != nil {
		return err
	}

	key, err := credential.LoadMasterKey(cfg.Credentials.KeyPath)
	if err != nil {
		return err
	}
	defer key.Clear()

	envelope, err := credential.NewEnvelope(key)
	if err != nil {
		return err
	}
	defer envelope.Clear()

	store, err := persistence.Open(
		ctx,
		persistence.Profile{
			WarehousePath:   cfg.Storage.WarehousePath,
			ConnectionsPath: cfg.Storage.ConnectionsPath,
			SyncPath:        cfg.Storage.SyncPath,
		},
		nil,
	)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, store.Close()) }()

	clock := func() time.Time { return *now }

	h, err := host.New(ctx, store, envelope, nil, clock, nil, reg)
	if err != nil {
		return err
	}
	defer h.Close()

	workflow := application.New(store, h.Registrations(), h, nil, clock, nil)

	connection, err := fixtureseed.SeedConfirmations(ctx, workflow, now)
	if err != nil {
		return err
	}

	if err = os.WriteFile(
		filepath.Join(cfg.Paths.ConfigDir, "connection.id"),
		[]byte(connection),
		0o600,
	); err != nil {
		return err
	}

	settings, _ := provider.NewSettings(map[string]string{"query_id": "123", "activity_query_id": "456"})
	if err = workflow.UpdateConnection(ctx, connection, "API acceptance", settings); err != nil {
		return err
	}

	for range 2 {
		*now = now.Add(15 * time.Second)
		if err = workflow.RunOnce(ctx, *now); err != nil {
			return err
		}
	}

	for _, stream := range []string{ibkrflex.Activity, ibkrflex.Confirmations} {
		if err = store.Schedule(
			ctx,
			host.WorkKey{Connection: connection, Stream: stream, Operation: "acquire"},
			false,
		); err != nil {
			return err
		}
	}

	if err = settleReferences(ctx, store, *now); err != nil {
		return err
	}

	_, secret, err := clientaccess.New(clientaccess.NewFileStore(cfg.Paths.ConfigDir)).
		Create(ctx, "Synthetic client")
	if err != nil {
		return err
	}
	defer secret.Clear()

	token, err := secret.Copy()
	if err != nil {
		return err
	}
	defer clear(token)

	return os.WriteFile(filepath.Join(cfg.Paths.ConfigDir, "client.token"), token, 0o600)
}

func Session(ctx context.Context, dir string, fresh, local bool, ready *json.Encoder) error {
	initial, err := devfixture.ReadReport("confirmations/initial.xml")
	if err != nil {
		return err
	}

	activity, err := devfixture.ReadReport("activity/realized.xml")
	if err != nil {
		return err
	}

	upstream := httptest.NewTLSServer(devfixture.Upstream(func() []byte { return initial }, activity))
	defer upstream.Close()

	client := upstream.Client()
	client.Transport = fixtureTransport{client.Transport, upstream.URL}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	cfg := configuration(dir)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if fresh {
		reg, e := ibkrflex.Registration(ibkrflex.Options{HTTPClient: client, BaseURL: upstream.URL})
		if e != nil {
			return e
		}

		if e = seed(ctx, cfg, reg, &now); e != nil {
			return e
		}
	}

	notify := func(endpoint string) error {
		return ready.Encode(map[string]string{
			"endpoint":   endpoint,
			"token_file": filepath.Join(dir, "client.token"),
		})
	}

	return daemon.RunSynthetic(ctx, cfg, client, upstream.URL, func() time.Time { return now }, local, notify)
}

func configuration(dir string) config.Config {
	cfg := config.Config{
		Paths: config.Paths{ConfigDir: dir, RuntimeDir: dir},
		Storage: config.Storage{
			WarehousePath:   filepath.Join(dir, "history.sqlite"),
			ConnectionsPath: filepath.Join(dir, "connections.sqlite"),
			SyncPath:        filepath.Join(dir, "sync.sqlite"),
		},
	}
	cfg.Credentials.KeyPath = filepath.Join(dir, "master.key")

	return cfg
}

func settleReferences(ctx context.Context, store *persistence.Store, now time.Time) error {
	q, _ := application.NewListQuery(application.ListOptions{}, application.InstrumentScope)

	instruments, err := store.InstrumentList(ctx, q)
	if err != nil {
		return err
	}

	for _, item := range instruments.Items {
		if err = store.ApplyReference(
			ctx,
			item.Resource.ID,
			reference.Result{Status: "no-match", Next: now.Add(365 * 24 * time.Hour)},
		); err != nil {
			return err
		}
	}

	return nil
}
