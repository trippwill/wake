// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package daemon owns process resources and joined shutdown.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/transport/admin"
)

type shell struct{ status admin.Status }

func (s shell) Status(context.Context) (admin.Status, error) { return s.status, nil }

func Run(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	progress ...func(string),
) (result error) {
	phase := func(value string) {
		for _, report := range progress {
			report(value)
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	phase("claiming private process paths")

	owner, err := acquireSocketOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		return err
	}
	defer owner.close()

	socket := filepath.Join(cfg.Paths.RuntimeDir, "wake.sock")
	if err = prepareSocket(socket); err != nil {
		return err
	}

	phase("opening private storage")

	store, err := persistence.Open(ctx, filepath.Join(cfg.Paths.DataDir, "wake.db"))
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, store.Close()) }()

	phase("checking credential protection")

	protected := false
	if cfg.Credentials.MasterKeyPath != "" {
		key, e := credential.LoadMasterKey(cfg.Credentials.MasterKeyPath)
		if e != nil {
			return e
		}
		defer key.Clear()

		envelope, e := credential.NewEnvelope(key)
		if e != nil {
			return e
		}
		defer envelope.Clear()

		protected = true
	}

	requestsContext, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	requests := &requestGroup{}

	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}

	defer func() { _ = listener.Close(); _ = os.Remove(socket) }()

	if err = os.Chmod(socket, 0o600); err != nil {
		return err
	}

	server := newHTTPServer(requests.handler(http.NewServeMux()))
	server.BaseContext = func(net.Listener) context.Context { return requestsContext }
	servers := []*http.Server{server}
	listeners := []net.Listener{sameUIDListener{listener}}

	auth, enabled, err := admin.Open(cfg.Paths.ConfigDir, cfg.Admin.AuthMode, cfg.Admin.Origin())
	if err != nil {
		return err
	}

	if enabled {
		defer auth.Close()

		handler, e := admin.NewHandler(
			auth,
			admin.Dependencies{
				Queries: shell{
					admin.Status{CredentialProtection: protected, StartedAt: time.Now().UTC().Format(time.RFC3339)},
				},
				Config: cfg,
			},
		)
		if e != nil {
			return e
		}

		adminListener, e := net.Listen("tcp", cfg.Admin.Address())
		if e != nil {
			return e
		}

		defer func() { _ = adminListener.Close() }()

		adminServer := newHTTPServer(requests.handler(handler))
		adminServer.BaseContext = server.BaseContext
		servers = append(servers, adminServer)
		listeners = append(listeners, adminListener)
	}

	var serving sync.WaitGroup

	failures := make(chan error, len(servers))
	for i, s := range servers {
		serving.Add(1)
		go func(s *http.Server, l net.Listener) {
			defer serving.Done()

			if e := s.Serve(l); !errors.Is(e, http.ErrServerClosed) {
				failures <- e
			}
		}(s, listeners[i])
	}

	logger.InfoContext(ctx, "daemon ready", "event", "daemon_started")
	phase("ready")

	select {
	case <-ctx.Done():
	case err = <-failures:
		result = err
	}

	phase("joining shutdown")
	shutdownHTTPServers(servers...)
	cancelRequests()
	requests.stop()
	serving.Wait()
	logger.InfoContext(context.Background(), "daemon stopped", "event", "daemon_stopped")

	return result
}

func shutdownHTTPServers(servers ...*http.Server) {
	//nolint:gosec // Shutdown has an independent bounded lifetime after process cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := s.Shutdown(ctx); err != nil {
				_ = s.Close()
			}
		}()
	}

	wg.Wait()
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           boundedReads(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
}
