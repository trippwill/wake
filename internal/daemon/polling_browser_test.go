// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package daemon

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/provider"
)

type browserPollingProvider struct{}

func (browserPollingProvider) Acquire(
	context.Context,
	provider.Request,
	provider.Access,
) (provider.Step, error) {
	return provider.Step{}, provider.ErrUnavailable
}

func TestBrowserPollingIntervals(t *testing.T) {
	cfg := shellConfig(t)

	store, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	stream, _ := provider.NewStream("sample-records", "browser-fixture")
	stream, _ = stream.WithPolling(time.Hour, 10*time.Minute)
	descriptor, _ := provider.NewDescriptor(
		"browser-fixture",
		"Polling fixture",
		[]provider.Stream{stream},
		nil,
		nil,
	)

	registration, err := provider.NewRegistration(descriptor, "v1", browserPollingProvider{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(t.Context(), store, nil, nil, nil,
		map[string]time.Duration{"browser-fixture:sample-records": 8 * time.Hour}, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	id, err := h.Configure(t.Context(), descriptor.ID(), descriptor.Name(), provider.Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()

	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic-polling-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := admin.Open(cfg.Paths.ConfigDir, origin)
	if err != nil {
		t.Fatal(err)
	}

	stopping := make(chan struct{})

	server.Config.Handler, err = admin.NewHandler(auth, admin.Dependencies{
		Workflow: application.New(store, h, nil, nil, nil), Stopping: stopping,
	})
	if err != nil {
		t.Fatal(err)
	}

	server.Start()
	defer func() { close(stopping); server.Close() }()

	ctx, deadline := context.WithTimeout(testbrowser.Start(t), 20*time.Second)
	defer deadline()

	const (
		input = "#poll-interval-sample-records"
		save  = `form[action$="/polling"] button[type="submit"]`
	)

	access := origin + "/connections/" + id + "?view=access"
	navigate := testbrowser.NewDocument

	if err := chromedp.Do(
		ctx,
		navigate(chromedp.Navigate(origin+"/login")),
		chromedp.SetValue(chromedp.CSS("#unlock"), "synthetic-polling-unlock"),
		navigate(chromedp.Click(chromedp.CSS(`form[action="/login"] button[type="submit"]`))),
		navigate(chromedp.Navigate(access)),
		chromedp.WaitVisible(chromedp.CSS(input)),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('#poll-help-sample-records').textContent.includes('default of 8h')`,
		),
		chromedp.SetValue(chromedp.CSS(input), "5m"),
		chromedp.Click(chromedp.CSS(save)),
		chromedp.WaitVisible(chromedp.CSS("#retry-interval")),
		chromedp.Poll[chromedp.Void](`document.querySelector('#retry-interval').value==='5m'`),
		chromedp.SetValue(chromedp.CSS("#retry-interval"), "6h"),
		navigate(chromedp.Click(chromedp.CSS(save))),
		chromedp.Poll[chromedp.Void](`document.querySelector('#poll-interval-sample-records').value==='6h'`),
		navigate(chromedp.Reload()),
		chromedp.Poll[chromedp.Void](`document.querySelector('#poll-interval-sample-records').value==='6h'`),
		chromedp.Clear(chromedp.CSS(input)),
		navigate(chromedp.Click(chromedp.CSS(save))),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('#poll-interval-sample-records').value===''&&document.querySelector('#poll-help-sample-records').textContent.includes('default of 8h')`,
		),
	); err != nil {
		t.Fatal(err)
	}
}
